package controlplane

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/k3s-io/kine/pkg/drivers/generic"
	"github.com/k3s-io/kine/pkg/endpoint"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/klog/v2"
	apiserverapp "k8s.io/kubernetes/cmd/kube-apiserver/app"
	kcmapp "k8s.io/kubernetes/cmd/kube-controller-manager/app"

	auditpolicy "vk8s.io/vk8s/internal/audit/policy"
	"vk8s.io/vk8s/internal/names"
)

const (
	componentKine      = "kine"
	componentAPIServer = "kube-apiserver"
	componentKCM       = "kube-controller-manager"

	// readyTimeout apiserver 从启动到 /readyz 通过的最长等待；超时则进程退出交给 kubelet 重启。
	readyTimeout = 10 * time.Minute
	// readyPollInterval /readyz 轮询间隔。
	readyPollInterval = time.Second
	// shutdownTimeout 收到 SIGTERM 后等待 apiserver 优雅退出的上限；pod 的 terminationGracePeriodSeconds 必须更大。
	shutdownTimeout = names.ControlPlaneShutdownTimeout
	// exitSettleDelay 组件以 nil 错误退出时，等待 SIGTERM 在本进程 ctx 上可见的时间，用于区分
	// 「信号触发的正常退出」与「意外的干净退出」。
	exitSettleDelay = time.Second
)

// procExit 一个组件 goroutine 的退出通知。
type procExit struct {
	name string
	err  error
}

// Run 按 docs 第 6 节的顺序启动三件事并监督它们，直到 ctx 取消（SIGTERM）或任一组件退出。
// 返回 nil 表示优雅停止；返回非 nil 时 main 以非 0 退出。
func Run(ctx context.Context, o Options) error {
	p := o.Paths()
	klog.InfoS("starting vk8s control-plane", "vcluster", o.VClusterName,
		"certsDir", o.CertsDir, "dataDir", o.DataDir, "runDir", o.RunDir,
		"serviceClusterIPRange", o.ServiceClusterIPRange, "podIP", o.PodIP)
	if o.PodIP == "" {
		klog.Warning("POD_IP is empty; kube-apiserver will auto-detect --advertise-address")
	}

	// 1. 目录与残留 socket
	if err := prepareDirs(p); err != nil {
		return err
	}

	// kine 的生命周期独立于本函数的 ctx：apiserver 优雅退出期间仍需要存储可用，
	// 因此在 apiserver 退出之后才停止 kine。
	kineCtx, stopKine := context.WithCancel(context.Background())
	defer stopKine()

	klog.InfoS("starting kine", "endpoint", redactDSN(p.SQLiteDSN), "listener", p.KineListener)
	// 以库方式调用 kine 时没有任何默认值，全部字段取 kine CLI（pkg/app）的默认值：
	// NotifyInterval 为 0 会在第一个 Watch 上 rand.Int63n(0) panic；EmulatedETCDVersion 为空则
	// apiserver 判定 etcd 不支持 watch progress notify；CompactBatchSize 为 0 启动即被拒绝。
	etcdCfg, err := endpoint.Listen(kineCtx, endpoint.Config{
		Endpoint:            p.SQLiteDSN,
		Listener:            p.KineListener,
		NotifyInterval:      5 * time.Second,
		EmulatedETCDVersion: "3.6.11",
		CompactTimeout:      5 * time.Second,
		CompactMinRetain:    1000,
		CompactBatchSize:    1000,
		PollBatchSize:       500,
		ConnectionPoolConfig: generic.ConnectionPoolConfig{
			MaxIdle:     20,
			MaxIdleTime: 2 * time.Minute,
		},
	})
	if err != nil {
		return fmt.Errorf("%s: %w", componentKine, err)
	}
	klog.InfoS("kine ready", "etcdEndpoints", etcdCfg.Endpoints)

	// 2. egress selector 配置；审计启用时再写策略文件
	if err := writeEgressSelectorConfig(p.EgressSelectorFile, p.KonnectivitySocket); err != nil {
		return err
	}
	if o.AuditWebhookKubeconfig != "" {
		if err := writeFileAtomic(p.AuditPolicyFile, auditpolicy.YAML); err != nil {
			return err
		}
		klog.InfoS("wrote audit policy", "path", p.AuditPolicyFile, "webhookKubeconfig", o.AuditWebhookKubeconfig)
	}

	// 3. kube-apiserver
	//
	// 注意：NewAPIServerCommand 内部调用 genericapiserver.SetupSignalContext()，自行注册 SIGTERM/SIGINT
	// 处理并把那个 ctx 捕获进 RunE——cobra 传入的 ctx 不会被它使用。因此：
	//   - 收到信号时 apiserver 自己开始优雅退出，本函数只需等待；
	//   - 需要主动停止它（kcm 失败等）时调用 genericapiserver.RequestShutdown()；
	//   - SetupSignalContext 在进程内只能调用一次，本进程只允许构造一个 apiserver 命令。
	exits := make(chan procExit, 2)
	apiserverCmd := apiserverapp.NewAPIServerCommand()
	apiserverArgs := APIServerArgs(o)
	apiserverCmd.SetArgs(apiserverArgs)
	klog.V(2).InfoS("kube-apiserver args", "args", apiserverArgs)
	klog.InfoS("starting kube-apiserver")
	go func() {
		exits <- procExit{componentAPIServer, apiserverCmd.ExecuteContext(ctx)}
	}()

	// 4. 等待 /readyz
	readyzURL := fmt.Sprintf("https://127.0.0.1:%d/readyz", names.APIServerPort)
	if err := waitForReadyz(ctx, p.PlatformCA, readyzURL, exits); err != nil {
		genericapiserver.RequestShutdown()
		return err
	}
	klog.InfoS("kube-apiserver ready", "url", readyzURL)

	// 5. kube-controller-manager
	//
	// kcm 的 RunE 使用 context.Background()，不响应 cobra ctx，也不会因信号自行退出；
	// 它随进程一起结束。--leader-elect=false 时不会触发 SetupSignalContext。
	kcmCmd := kcmapp.NewControllerManagerCommand()
	kcmArgs := ControllerManagerArgs(o)
	kcmCmd.SetArgs(kcmArgs)
	klog.V(2).InfoS("kube-controller-manager args", "args", kcmArgs)
	klog.InfoS("starting kube-controller-manager")
	go func() {
		exits <- procExit{componentKCM, kcmCmd.ExecuteContext(ctx)}
	}()

	// 6. 监督
	select {
	case <-ctx.Done():
		klog.InfoS("shutdown signal received; waiting for kube-apiserver to stop")
		return awaitAPIServerExit(exits, shutdownTimeout)

	case e := <-exits:
		if e.err == nil {
			// 可能是 SIGTERM 先到达了 apiserver 自己的信号处理器，本进程的 ctx 还没来得及取消。
			select {
			case <-ctx.Done():
				klog.InfoS("component stopped by shutdown signal", "component", e.name)
				if e.name != componentAPIServer {
					return awaitAPIServerExit(exits, shutdownTimeout)
				}
				return nil
			case <-time.After(exitSettleDelay):
			}
		}
		klog.ErrorS(e.err, "component exited unexpectedly; stopping control-plane", "component", e.name)
		if e.name != componentAPIServer {
			genericapiserver.RequestShutdown()
			_ = awaitAPIServerExit(exits, shutdownTimeout)
		}
		if e.err == nil {
			return fmt.Errorf("%s exited unexpectedly", e.name)
		}
		return fmt.Errorf("%s exited: %w", e.name, e.err)
	}
}

// awaitAPIServerExit 等待 apiserver goroutine 结束；kcm 的退出通知被忽略。
func awaitAPIServerExit(exits <-chan procExit, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case e := <-exits:
			if e.name != componentAPIServer {
				klog.V(2).InfoS("component exited during shutdown", "component", e.name, "err", e.err)
				continue
			}
			if e.err != nil && !errors.Is(e.err, context.Canceled) {
				klog.ErrorS(e.err, "kube-apiserver returned error during shutdown")
				return fmt.Errorf("%s shutdown: %w", componentAPIServer, e.err)
			}
			klog.InfoS("kube-apiserver stopped")
			return nil
		case <-timer.C:
			return fmt.Errorf("%s did not stop within %s", componentAPIServer, timeout)
		}
	}
}

// prepareDirs 创建 sqlite 与运行时目录，清理上次残留的 unix socket。
func prepareDirs(p Paths) error {
	if err := os.MkdirAll(p.SQLiteDir, 0o700); err != nil {
		return fmt.Errorf("create sqlite dir %s: %w", p.SQLiteDir, err)
	}
	runDir := filepath.Dir(p.EgressSelectorFile)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return fmt.Errorf("create run dir %s: %w", runDir, err)
	}
	if err := os.Remove(p.KineSocket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale kine socket %s: %w", p.KineSocket, err)
	}
	return nil
}

// writeEgressSelectorConfig 原子写入 egress selector 配置文件。
func writeEgressSelectorConfig(path, udsPath string) error {
	data, err := EgressSelectorConfigYAML(udsPath)
	if err != nil {
		return fmt.Errorf("marshal egress selector config: %w", err)
	}
	if err := writeFileAtomic(path, data); err != nil {
		return err
	}
	klog.InfoS("wrote egress selector config", "path", path, "uds", udsPath)
	return nil
}

// writeFileAtomic 先写临时文件再 rename，避免 apiserver 读到半个文件。
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

// waitForReadyz 轮询 apiserver 的 /readyz，用平台 CA 校验服务端证书（SAN 含 127.0.0.1）。
// 期间任一组件退出立即返回错误。
func waitForReadyz(ctx context.Context, caFile, url string, exits <-chan procExit) error {
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		return fmt.Errorf("read platform CA %s: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("platform CA %s contains no certificates", caFile)
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
			DisableKeepAlives: true,
		},
	}

	deadline := time.NewTimer(readyTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(readyPollInterval)
	defer tick.Stop()

	var lastErr error
	lastLog := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-exits:
			if e.err == nil {
				return fmt.Errorf("%s exited during startup", e.name)
			}
			return fmt.Errorf("%s exited during startup: %w", e.name, e.err)
		case <-deadline.C:
			return fmt.Errorf("kube-apiserver not ready within %s: %v", readyTimeout, lastErr)
		case <-tick.C:
			if lastErr = probeReadyz(ctx, client, url); lastErr == nil {
				return nil
			}
			if time.Since(lastLog) >= 10*time.Second {
				klog.InfoS("waiting for kube-apiserver", "url", url, "lastError", lastErr.Error())
				lastLog = time.Now()
			}
		}
	}
}

func probeReadyz(ctx context.Context, client *http.Client, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// redactDSN 日志里不打印 DSN 中可能的凭据（sqlite 无凭据，但保持习惯）。
func redactDSN(dsn string) string {
	if i := strings.IndexByte(dsn, '?'); i >= 0 {
		return dsn[:i]
	}
	return dsn
}
