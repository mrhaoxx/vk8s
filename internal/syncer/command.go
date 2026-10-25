// Package syncer 实现 `vk8s syncer` 子命令：每个虚拟集群一个进程，负责虚拟 ↔ 宿主 namespace 的资源同步、
// 假 kubelet（logs/exec/attach/port-forward）、metrics.k8s.io、Service ClusterIP 对齐 webhook，
// 以及向虚拟集群投放 CoreDNS 等自愈清单。
//
// 一个进程两个 client：虚拟端 https://127.0.0.1:6443 + syncer 证书（system:masters）；
// 宿主端 pod ServiceAccount，informer cache 限定在宿主 ns（nodes/storageclasses/ingressclasses 为集群级只读）。
package syncer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	nodev1 "k8s.io/api/node/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/bootstrap"
	"vk8s.io/vk8s/internal/syncer/kubelet"
	"vk8s.io/vk8s/internal/syncer/metrics"
	"vk8s.io/vk8s/internal/syncer/placement"
	"vk8s.io/vk8s/internal/syncer/resources"
	"vk8s.io/vk8s/internal/syncer/translate"
	"vk8s.io/vk8s/internal/syncer/webhook"
)

const (
	// bootstrapInterval 自愈清单重跑周期。
	bootstrapInterval = 5 * time.Minute
	// placementInterval 读取 VirtualCluster.spec.nodePlacement 的周期。
	placementInterval = 30 * time.Second
	// retryInterval 启动阶段等待 / 重试的间隔。
	retryInterval = 5 * time.Second
	// clientQPS / clientBurst 两端 client 的限速（默认 5/10 对同步器太小）。
	clientQPS   = 50
	clientBurst = 100
)

// Options 命令行参数，由 operator 以容器参数传入。
type Options struct {
	VClusterName     string
	HostNamespace    string
	CertsDir         string
	RunDir           string
	APIDomain        string
	AppsDomain       string
	AppsTLSSecret    string
	HostDNSServiceIP string
	HealthListen     string
	WebhookListen    string
	MetricsAPIListen string
	// ManagerURL manager 的 mTLS 地址（https://vk8s-manager.<sys>.svc:8443）；空表示不提供 kubelet 指标。
	ManagerURL string
}

// NewCommand 构造 `syncer` 子命令。
func NewCommand() *cobra.Command {
	o := &Options{}
	cmd := &cobra.Command{
		Use:   "syncer",
		Short: "虚拟集群资源同步器、假 kubelet 与 metrics.k8s.io 服务",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.VClusterName, "vcluster-name", "", "虚拟集群名")
	f.StringVar(&o.HostNamespace, "host-namespace", "", "虚拟集群在宿主上的 namespace（vc-<name>）")
	f.StringVar(&o.CertsDir, "certs-dir", names.CertsDir, "证书目录")
	f.StringVar(&o.RunDir, "run-dir", names.RunDir, "运行目录（konnectivity unix socket 所在）")
	f.StringVar(&o.APIDomain, "api-domain", "", "平台 API 域名")
	f.StringVar(&o.AppsDomain, "apps-domain", "", "平台应用域名，Ingress host 必须在其下")
	f.StringVar(&o.AppsTLSSecret, "apps-tls-secret", "", "租户 ns 内泛证书 Secret 名；非空时为未写 tls 的 Ingress 自动补上")
	f.StringVar(&o.HostDNSServiceIP, "host-dns-service-ip", "", "宿主 kube-dns 的 ClusterIP，CoreDNS forward 上游")
	f.StringVar(&o.HealthListen, "health-listen", fmt.Sprintf(":%d", names.SyncerHealthPort), "/healthz /readyz /metrics 监听地址")
	f.StringVar(&o.WebhookListen, "webhook-listen", fmt.Sprintf(":%d", names.SyncerWebhookPort), "Service 对齐 webhook 监听地址")
	f.StringVar(&o.MetricsAPIListen, "metrics-api-listen", fmt.Sprintf(":%d", names.SyncerMetricsAPIPort), "metrics.k8s.io 监听地址（同时经 konnectivity 隧道提供）")
	f.StringVar(&o.ManagerURL, "manager-url", "", "manager 的 mTLS 地址；设置后假 kubelet 提供 /metrics/cadvisor 与 /metrics/resource")
	for _, name := range []string{"vcluster-name", "host-namespace", "api-domain", "apps-domain", "host-dns-service-ip"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// run 启动顺序：建 client → 等虚拟 apiserver /readyz → 读 nodePlacement → 起 webhook → bootstrap →
// 启动 manager（reconcilers、假 kubelet、metrics、health、周期任务）。
func run(ctx context.Context, o *Options) error {
	ctrl.SetLogger(klog.NewKlogr())
	log := ctrl.Log.WithName("syncer").WithValues("vcluster", o.VClusterName)

	podIP := os.Getenv("POD_IP")
	if podIP == "" {
		return errors.New("POD_IP environment variable is required")
	}
	if o.HostNamespace == "" {
		o.HostNamespace = names.HostNamespace(o.VClusterName)
	}

	platformCA, err := os.ReadFile(filepath.Join(o.CertsDir, names.KeyPlatformCA))
	if err != nil {
		return fmt.Errorf("read platform CA: %w", err)
	}
	internalCA, err := os.ReadFile(filepath.Join(o.CertsDir, names.KeyInternalCA))
	if err != nil {
		return fmt.Errorf("read internal CA: %w", err)
	}

	hostCfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("host in-cluster config: %w", err)
	}
	hostCfg.QPS, hostCfg.Burst = clientQPS, clientBurst
	virtualCfg, err := clientcmd.BuildConfigFromFlags("", filepath.Join(o.CertsDir, names.KeySyncerKubeconfig))
	if err != nil {
		return fmt.Errorf("virtual kubeconfig: %w", err)
	}
	virtualCfg.QPS, virtualCfg.Burst = clientQPS, clientBurst

	if err := waitForAPIServer(ctx, log, virtualCfg); err != nil {
		return err
	}

	virtualScheme, hostScheme, err := buildSchemes()
	if err != nil {
		return err
	}
	virtualDirect, err := client.New(virtualCfg, client.Options{Scheme: virtualScheme})
	if err != nil {
		return fmt.Errorf("virtual client: %w", err)
	}
	hostDirect, err := client.New(hostCfg, client.Options{Scheme: hostScheme})
	if err != nil {
		return fmt.Errorf("host client: %w", err)
	}
	virtualClientset, err := kubernetes.NewForConfig(virtualCfg)
	if err != nil {
		return fmt.Errorf("virtual clientset: %w", err)
	}

	// 宿主 Service vc-<name> 的 ClusterIP = 虚拟 default/kubernetes 对齐后的 IP = Pod 内 KUBERNETES_SERVICE_HOST
	kubernetesServiceIP, err := readHostServiceIP(ctx, hostDirect, o)
	if err != nil {
		return err
	}

	// nodePlacement 是唯一硬隔离边界，首次必须读到才能继续
	holder := placement.NewHolder()
	if err := retryUntil(ctx, log, "load nodePlacement", func() error {
		return loadPlacement(ctx, hostDirect, o.VClusterName, holder)
	}); err != nil {
		return err
	}

	// webhook 先于 bootstrap 启动：bootstrap 创建 kube-dns Service 时需要它对齐 ClusterIP
	_, webhookPort, err := webhook.SplitListen(o.WebhookListen)
	if err != nil {
		return err
	}
	whSrv, err := webhook.NewServer(o.WebhookListen, o.CertsDir)
	if err != nil {
		return err
	}
	webhook.Register(whSrv, &webhook.ServiceMutator{
		Log:           log.WithName("webhook"),
		Host:          hostDirect,
		HostNamespace: o.HostNamespace,
	})
	errCh := make(chan error, 2)
	go func() { errCh <- wrapErr("webhook server", whSrv.Start(ctx)) }()
	if err := retryUntil(ctx, log, "webhook server start", func() error {
		return whSrv.StartedChecker()(&http.Request{})
	}); err != nil {
		return err
	}

	ready := &readiness{}
	boot := bootstrap.New(bootstrap.Options{
		Log:              log.WithName("bootstrap"),
		VCName:           o.VClusterName,
		HostNamespace:    o.HostNamespace,
		HostDNSServiceIP: o.HostDNSServiceIP,
		InternalCA:       internalCA,
		PodIP:            podIP,
		WebhookPort:      webhookPort,
		Virtual:          virtualDirect,
		Host:             hostDirect,
	})
	if err := retryUntil(ctx, log, "bootstrap", func() error { return boot.Run(ctx) }); err != nil {
		return err
	}
	ready.bootstrapDone.Store(true)

	mgr, err := ctrl.NewManager(virtualCfg, manager.Options{
		Scheme:                 virtualScheme,
		Logger:                 log.WithName("virtual"),
		LeaderElection:         false,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		return fmt.Errorf("virtual manager: %w", err)
	}
	hostCluster, err := cluster.New(hostCfg, func(co *cluster.Options) {
		co.Scheme = hostScheme
		co.Logger = log.WithName("host")
		co.Cache = hostCacheOptions(o.HostNamespace)
	})
	if err != nil {
		return fmt.Errorf("host cluster: %w", err)
	}
	if err := mgr.Add(hostCluster); err != nil {
		return fmt.Errorf("add host cluster: %w", err)
	}

	rc := &resources.Context{
		Log:                 log.WithName("resources"),
		VCName:              o.VClusterName,
		HostNamespace:       o.HostNamespace,
		AppsDomain:          o.AppsDomain,
		AppsTLSSecret:       o.AppsTLSSecret,
		PlatformCA:          platformCA,
		KubernetesServiceIP: kubernetesServiceIP,
		Virtual:             mgr.GetClient(),
		VirtualClientset:    virtualClientset,
		Host:                hostCluster.GetClient(),
		HostCache:           hostCluster.GetCache(),
		Recorder:            mgr.GetEventRecorderFor(resources.ControllerName),
		Placement:           holder,
	}
	if err := resources.SetupAll(ctx, mgr, rc); err != nil {
		return err
	}
	// 配额即时拒绝：两侧 cache 就绪前的请求由 Evaluate 的读错误路径放行（failurePolicy 也是 Ignore）
	webhook.RegisterQuota(whSrv, &webhook.QuotaValidator{
		Log:           log.WithName("quota-webhook"),
		Host:          hostCluster.GetClient(),
		Virtual:       mgr.GetClient(),
		HostNamespace: o.HostNamespace,
		Clock:         clock.RealClock{},
	})
	// 先把 ResourceQuota 的 informer 登记进宿主 cache（cache 尚未启动，不阻塞），随 cache 一起启动，
	// 否则第一次判定才建 informer，要等它同步
	if _, err := hostCluster.GetCache().GetInformer(ctx, &corev1.ResourceQuota{}); err != nil {
		return fmt.Errorf("resourcequota informer: %w", err)
	}

	// 假 kubelet 与 metrics.k8s.io：经 konnectivity 隧道按端口分流；metrics 另在 TCP 上监听作为直连兜底
	kubeletTLS, err := kubelet.ServerTLSConfig(
		filepath.Join(o.CertsDir, names.KeyFakeKubeletCert), filepath.Join(o.CertsDir, names.KeyFakeKubeletKey), internalCA, "")
	if err != nil {
		return fmt.Errorf("fake kubelet tls: %w", err)
	}
	metricsTLS, err := kubelet.ServerTLSConfig(
		filepath.Join(o.CertsDir, names.KeySyncerServingCert), filepath.Join(o.CertsDir, names.KeySyncerServingKey), internalCA, names.CNFrontProxyClient)
	if err != nil {
		return fmt.Errorf("metrics tls: %w", err)
	}
	fakeKubelet, err := kubelet.NewFakeKubelet(log.WithName("kubelet"), hostCfg, o.HostNamespace, func(ctx context.Context, hostPod string) bool {
		pod := &corev1.Pod{}
		return hostCluster.GetClient().Get(ctx, client.ObjectKey{Namespace: o.HostNamespace, Name: hostPod}, pod) == nil
	})
	if err != nil {
		return err
	}
	if o.ManagerURL != "" {
		fetch, err := managerMetrics(o.ManagerURL, o.VClusterName, o.CertsDir, platformCA)
		if err != nil {
			return fmt.Errorf("kubelet metrics: %w", err)
		}
		fakeKubelet.ManagerMetrics = fetch
		fakeKubelet.NodeForIP = nodeForIP(mgr.GetClient())
		fakeKubelet.HostPodLookup = hostPodLookup(hostCluster.GetClient(), o.HostNamespace)
	}
	metricsSrv, err := metrics.NewServer(log.WithName("metrics"), hostCfg, o.HostNamespace, hostCluster.GetClient(), mgr.GetClient())
	if err != nil {
		return err
	}
	connect := &kubelet.ConnectServer{
		Log:            log.WithName("konnectivity"),
		SocketPath:     filepath.Join(o.RunDir, filepath.Base(names.KonnectivitySocket)),
		KubeletHandler: fakeKubelet.Handler(),
		KubeletTLS:     kubeletTLS,
		MetricsHandler: metricsSrv.Handler(),
		MetricsTLS:     metricsTLS,
		MetricsAddr:    podIP,
	}

	runnables := []manager.Runnable{
		connect,
		manager.RunnableFunc(serveTLS(log.WithName("metrics-tcp"), o.MetricsAPIListen, metricsTLS, metricsSrv.Handler())),
		&healthServer{log: log.WithName("health"), listen: o.HealthListen, ready: ready},
		manager.RunnableFunc(boot.RunPeriodically(bootstrapInterval)),
		manager.RunnableFunc(pollPlacement(log, hostDirect, o.VClusterName, holder)),
		manager.RunnableFunc(func(ctx context.Context) error {
			if mgr.GetCache().WaitForCacheSync(ctx) && hostCluster.GetCache().WaitForCacheSync(ctx) {
				ready.cachesSynced.Store(true)
				log.Info("caches synced, syncer ready")
			}
			<-ctx.Done()
			return nil
		}),
	}
	for _, r := range runnables {
		if err := mgr.Add(r); err != nil {
			return fmt.Errorf("add runnable: %w", err)
		}
	}

	go func() { errCh <- wrapErr("manager", mgr.Start(ctx)) }()
	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		// 等 manager 自己退出
		return <-errCh
	}
}

func wrapErr(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", what, err)
}

// buildSchemes 虚拟端：client-go 内建类型 + apiregistration；宿主端：client-go 内建类型 + vk8s CRD。
func buildSchemes() (*runtime.Scheme, *runtime.Scheme, error) {
	virtual := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(virtual); err != nil {
		return nil, nil, err
	}
	if err := apiregistrationv1.AddToScheme(virtual); err != nil {
		return nil, nil, err
	}
	host := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(host); err != nil {
		return nil, nil, err
	}
	if err := v1alpha1.AddToScheme(host); err != nil {
		return nil, nil, err
	}
	return virtual, host, nil
}

// hostCacheOptions 宿主 cache：限定在宿主 ns、只含 managed=true 的对象；
// Event 与 operator 建的 LimitRange 不带我们的标签故放开；Node / StorageClass / IngressClass / RuntimeClass 为集群级只读。
func hostCacheOptions(hostNamespace string) cache.Options {
	everything := labels.Everything()
	return cache.Options{
		DefaultNamespaces:    map[string]cache.Config{hostNamespace: {}},
		DefaultLabelSelector: translate.ManagedSelector(),
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Event{}:              {Label: everything},
			&corev1.LimitRange{}:         {Label: everything},
			&corev1.ResourceQuota{}:      {Label: everything}, // operator 建的 vk8s-quota，配额 webhook 读 hard/used
			&corev1.Node{}:               {Label: everything},
			&storagev1.StorageClass{}:    {Label: everything},
			&networkingv1.IngressClass{}: {Label: everything},
			&nodev1.RuntimeClass{}:       {Label: everything},
		},
	}
}

// waitForAPIServer 轮询虚拟 apiserver /readyz 直到 200。
func waitForAPIServer(ctx context.Context, log logr.Logger, cfg *rest.Config) error {
	httpClient, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return fmt.Errorf("virtual http client: %w", err)
	}
	url := strings.TrimSuffix(cfg.Host, "/") + "/readyz"
	return retryUntil(ctx, log, "virtual apiserver /readyz", func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("status %d", resp.StatusCode)
		}
		return nil
	})
}

// retryUntil 每 retryInterval 重试 fn 直到成功或 ctx 结束。
func retryUntil(ctx context.Context, log logr.Logger, what string, fn func() error) error {
	for {
		err := fn()
		if err == nil {
			return nil
		}
		log.Info("waiting", "for", what, "error", err.Error())
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", what, ctx.Err())
		case <-time.After(retryInterval):
		}
	}
}

// readHostServiceIP 读宿主 Service vc-<name> 的 ClusterIP。
func readHostServiceIP(ctx context.Context, host client.Client, o *Options) (string, error) {
	svc := &corev1.Service{}
	if err := host.Get(ctx, client.ObjectKey{Namespace: o.HostNamespace, Name: names.ServiceName(o.VClusterName)}, svc); err != nil {
		return "", fmt.Errorf("read host service %s: %w", names.ServiceName(o.VClusterName), err)
	}
	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == corev1.ClusterIPNone {
		return "", fmt.Errorf("host service %s has no ClusterIP", names.ServiceName(o.VClusterName))
	}
	return svc.Spec.ClusterIP, nil
}

// loadPlacement 读自己的 VirtualCluster（RBAC 仅允许 get 这一个），把 spec.nodePlacement 与 status.leasedNodes 写入 holder。
func loadPlacement(ctx context.Context, host client.Client, vcName string, holder *placement.Holder) error {
	vc := &v1alpha1.VirtualCluster{}
	if err := host.Get(ctx, client.ObjectKey{Name: vcName}, vc); err != nil {
		return fmt.Errorf("get VirtualCluster %s: %w", vcName, err)
	}
	holder.Set(placement.FromVCluster(vc))
	return nil
}

// pollPlacement 每 30s 重新读取 nodePlacement。
func pollPlacement(log logr.Logger, host client.Client, vcName string, holder *placement.Holder) func(context.Context) error {
	return func(ctx context.Context) error {
		ticker := time.NewTicker(placementInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if err := loadPlacement(ctx, host, vcName, holder); err != nil {
					log.Error(err, "refresh nodePlacement")
				}
			}
		}
	}
}

// serveTLS 在 TCP 地址上以给定 TLS 配置提供 handler（metrics.k8s.io 的直连兜底）。
func serveTLS(log logr.Logger, listen string, tlsCfg *tls.Config, handler http.Handler) func(context.Context) error {
	return func(ctx context.Context) error {
		ln, err := net.Listen("tcp", listen)
		if err != nil {
			return fmt.Errorf("listen %s: %w", listen, err)
		}
		srv := &http.Server{
			Handler:           handler,
			TLSConfig:         tlsCfg,
			ReadHeaderTimeout: 30 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      60 * time.Second,
		}
		errCh := make(chan error, 1)
		go func() {
			log.Info("tls server listening", "addr", listen)
			errCh <- srv.ServeTLS(ln, "", "")
		}()
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			return nil
		case err := <-errCh:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		}
	}
}
