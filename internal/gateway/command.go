// Package gateway 实现 kubectl 流量的 L4 SNI 网关（docs 第 8 节）：
// 读 TLS ClientHello 的 server_name，取第一个 label 作为虚拟集群名，查 VirtualCluster informer
// （要求 phase=Running），把已读字节连同后续流量原样透传到 vc-<vc>.vc-<vc>.svc:443。
// 不终结 TLS、不看 token、无状态。
package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"vk8s.io/vk8s/internal/names"
)

// Options 网关启动参数。
type Options struct {
	// Listen 数据面监听地址，默认 :443。
	Listen string
	// HealthListen /healthz /readyz /metrics 监听地址，默认 :8081。
	HealthListen string
	// APIDomain 平台 API 域名；SNI 必须形如 <vc>.<APIDomain>。
	APIDomain string
	// Kubeconfig 为空时使用 in-cluster 配置。
	Kubeconfig string

	// HandshakeTimeout 从接受连接到读完 ClientHello 的上限。
	HandshakeTimeout time.Duration
	// DialTimeout 连接后端控制平面的上限。
	DialTimeout time.Duration
	// IdleTimeout 双向都无数据的空闲上限；0 = 不限。
	IdleTimeout time.Duration
	// ShutdownGrace 收到停止信号后等待现有连接自然结束的时间，之后强制关闭。
	ShutdownGrace time.Duration
}

// Validate 校验并规范化。
func (o *Options) Validate() error {
	o.APIDomain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(o.APIDomain)), ".")
	if o.APIDomain == "" {
		return errors.New("gateway: --api-domain is required")
	}
	if strings.HasPrefix(o.APIDomain, ".") || strings.Contains(o.APIDomain, "..") {
		return fmt.Errorf("gateway: invalid --api-domain %q", o.APIDomain)
	}
	if o.HandshakeTimeout <= 0 {
		return errors.New("gateway: --handshake-timeout must be > 0")
	}
	if o.DialTimeout <= 0 {
		return errors.New("gateway: --dial-timeout must be > 0")
	}
	return nil
}

// NewCommand 返回 `vk8s gateway` 子命令。
func NewCommand() *cobra.Command {
	o := Options{
		Listen:           fmt.Sprintf(":%d", names.GatewayPort),
		HealthListen:     fmt.Sprintf(":%d", names.GatewayHealthPort),
		HandshakeTimeout: 10 * time.Second,
		DialTimeout:      10 * time.Second,
		IdleTimeout:      time.Hour,
		ShutdownGrace:    30 * time.Second,
	}
	cmd := &cobra.Command{
		Use:           "gateway",
		Short:         "kubectl 流量的 L4 SNI 网关：<vc>.<apiDomain>:443 → vc-<vc>.vc-<vc>.svc:443",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := o.Validate(); err != nil {
				return err
			}
			return Run(cmd.Context(), o)
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&o.Listen, "listen", o.Listen, "TLS 透传监听地址")
	fs.StringVar(&o.HealthListen, "health-listen", o.HealthListen, "/healthz /readyz /metrics 监听地址")
	fs.StringVar(&o.APIDomain, "api-domain", "", "平台 API 域名，SNI 必须以 .<api-domain> 结尾（必填）")
	fs.StringVar(&o.Kubeconfig, "kubeconfig", "", "宿主集群 kubeconfig；空 = in-cluster")
	fs.DurationVar(&o.HandshakeTimeout, "handshake-timeout", o.HandshakeTimeout, "读取 ClientHello 的超时")
	fs.DurationVar(&o.DialTimeout, "dial-timeout", o.DialTimeout, "连接后端控制平面的超时")
	fs.DurationVar(&o.IdleTimeout, "idle-timeout", o.IdleTimeout, "连接空闲超时，0 = 不限")
	fs.DurationVar(&o.ShutdownGrace, "shutdown-grace", o.ShutdownGrace, "停止时等待现有连接结束的时间")
	_ = cmd.MarkFlagRequired("api-domain")
	return cmd
}

// Run 启动路由表 informer、健康/指标服务与 TCP 代理，直到 ctx 取消。
func Run(ctx context.Context, o Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	klog.InfoS("starting vk8s gateway", "listen", o.Listen, "healthListen", o.HealthListen,
		"apiDomain", o.APIDomain, "idleTimeout", o.IdleTimeout)

	restCfg, err := buildRESTConfig(o.Kubeconfig)
	if err != nil {
		return err
	}
	metrics := NewMetrics()

	router, err := NewRouter(restCfg, o.APIDomain, metrics)
	if err != nil {
		return err
	}

	errc := make(chan error, 3)
	go func() { errc <- wrap("router", router.Run(ctx)) }()

	health := NewHealthServer(o.HealthListen, router.HasSynced, metrics)
	go func() { errc <- wrap("health server", health.Run(ctx)) }()

	// 数据面不等待 informer 同步：同步前所有 SNI 都查不到并被拒绝，readyz 为 503，
	// Service 不会把流量导过来。
	proxy := NewProxy(ProxyConfig{
		Listen:           o.Listen,
		HandshakeTimeout: o.HandshakeTimeout,
		DialTimeout:      o.DialTimeout,
		IdleTimeout:      o.IdleTimeout,
		ShutdownGrace:    o.ShutdownGrace,
	}, router, metrics)
	go func() { errc <- wrap("proxy", proxy.Run(ctx)) }()

	select {
	case <-ctx.Done():
		klog.InfoS("shutdown signal received")
		// 等待三个子系统收尾（proxy 内部有 ShutdownGrace 上限）
		for i := 0; i < 3; i++ {
			if err := <-errc; err != nil {
				klog.ErrorS(err, "subsystem exited with error during shutdown")
			}
		}
		return nil
	case err := <-errc:
		if err == nil {
			err = errors.New("gateway: subsystem exited unexpectedly")
		}
		klog.ErrorS(err, "gateway subsystem failed; shutting down")
		cancel()
		return err
	}
}

func wrap(name string, err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("%s: %w", name, err)
}

// buildRESTConfig 空路径走 in-cluster；否则读 kubeconfig。
func buildRESTConfig(kubeconfig string) (*rest.Config, error) {
	var (
		cfg *rest.Config
		err error
	)
	if kubeconfig == "" {
		cfg, err = rest.InClusterConfig()
	} else {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	if err != nil {
		return nil, fmt.Errorf("gateway: build rest config: %w", err)
	}
	cfg.UserAgent = "vk8s-gateway"
	// 只 watch 一种资源，默认限速足够；放宽一点避免重连风暴时被自己限住
	cfg.QPS = 20
	cfg.Burst = 40
	return cfg, nil
}
