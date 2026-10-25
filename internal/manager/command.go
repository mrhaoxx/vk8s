// Package manager 实现 `vk8s manager`：Web UI、REST API、登录、TokenReview webhook、
// 全部 CRD 的 reconciler 与平台 CA，单进程、可多副本（leader election 只作用于 reconciler）。
package manager

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"k8s.io/client-go/kubernetes"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/config"
	"vk8s.io/vk8s/internal/controllers"
	"vk8s.io/vk8s/internal/manager/api"
	"vk8s.io/vk8s/internal/manager/auth"
	"vk8s.io/vk8s/internal/manager/tokens"
	"vk8s.io/vk8s/internal/manager/ui"
	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/pki"
)

const (
	leaderElectionID = "vk8s-manager.vk8s.io"
	// certRotateInterval manager 服务端证书的检查周期。
	certRotateInterval = 12 * time.Hour
)

type options struct {
	configPath    string
	httpListen    string
	tlsListen     string
	metricsListen string
	leaderElect   bool
}

// NewCommand 返回 `manager` 子命令。
func NewCommand() *cobra.Command {
	o := &options{
		configPath:    "/etc/vk8s/config.yaml",
		httpListen:    fmt.Sprintf(":%d", names.ManagerHTTPPort),
		tlsListen:     fmt.Sprintf(":%d", names.ManagerTLSPort),
		metricsListen: ":9090",
	}
	cmd := &cobra.Command{
		Use:   "manager",
		Short: "平台管理服务：UI、API、认证、TokenReview webhook、operator",
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.configPath, "config", o.configPath, "平台配置文件路径")
	f.StringVar(&o.httpListen, "http-listen", o.httpListen, "UI / API 监听地址（HTTP，TLS 由 Ingress 终结）")
	f.StringVar(&o.tlsListen, "tls-listen", o.tlsListen, "TokenReview webhook 监听地址（TLS + 必需的客户端证书）")
	f.StringVar(&o.metricsListen, "metrics-listen", o.metricsListen, "Prometheus /metrics 监听地址，\"0\" 关闭")
	f.BoolVar(&o.leaderElect, "leader-elect", false, "多副本时启用 leader election（仅 reconciler 受其约束）")
	return cmd
}

func run(ctx context.Context, o *options) error {
	ctrl.SetLogger(klog.NewKlogr())
	log := ctrl.Log.WithName("manager")
	ctx = ctrl.LoggerInto(ctx, log)

	cfg, err := config.Load(o.configPath)
	if err != nil {
		return err
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme)) // core/apps/rbac/networking/policy 等内建类型
	utilruntime.Must(v1alpha1.AddToScheme(scheme))

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("kubeconfig: %w", err)
	}
	restCfg.UserAgent = "vk8s-manager"

	// 启动期引导用直连 client（cache 尚未启动）。
	kubeClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("kubernetes clientset: %w", err)
	}
	direct, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}

	// 宿主网络参数：配置为空时自动探测。
	if cfg.HostServiceCIDR == "" {
		cidr, err := detectServiceCIDR(ctx, direct, cfg.SystemNamespace)
		if err != nil {
			return err
		}
		cfg.HostServiceCIDR = cidr
		log.Info("detected host service CIDR", "cidr", cidr)
	}
	if cfg.HostDNSServiceIP == "" {
		ip, err := detectDNSServiceIP(ctx, direct)
		if err != nil {
			return err
		}
		cfg.HostDNSServiceIP = ip
		log.Info("detected host DNS service IP", "ip", ip)
	}

	// 平台 CA、会话密钥、manager 服务端证书、引导管理员。
	platformCA, err := ensurePlatformCA(ctx, direct, cfg.SystemNamespace)
	if err != nil {
		return err
	}
	sessionKey, err := ensureSessionKey(ctx, direct, cfg.SystemNamespace)
	if err != nil {
		return err
	}
	managerCert, err := ensureManagerCert(ctx, direct, cfg.SystemNamespace, platformCA)
	if err != nil {
		return err
	}
	if err := ensureBootstrapAdmin(ctx, direct, cfg); err != nil {
		// 不阻塞启动：webhook 与 reconciler 不依赖它；日志里提示管理员修复。
		log.Error(err, "bootstrap admin failed; fix the password secret and restart to create the admin user")
	}

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                        scheme,
		Metrics:                       metricsserver.Options{BindAddress: o.metricsListen},
		HealthProbeBindAddress:        "0", // 健康检查走 HTTP 监听的 /healthz /readyz
		LeaderElection:                o.leaderElect,
		LeaderElectionID:              leaderElectionID,
		LeaderElectionNamespace:       cfg.SystemNamespace,
		LeaderElectionReleaseOnCancel: true,
		Client: client.Options{
			Cache: &client.CacheOptions{
				// Secret/ConfigMap 数量不受控且含敏感数据，Pod 只在就绪判定时读一次：都直连 apiserver。
				DisableFor: []client.Object{&corev1.Secret{}, &corev1.ConfigMap{}, &corev1.Pod{}},
			},
		},
		Cache: cache.Options{ByObject: managedCacheSelectors(cfg.SystemNamespace)},
	})
	if err != nil {
		return fmt.Errorf("create controller manager: %w", err)
	}

	if err := auth.RegisterIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("register user indexes: %w", err)
	}
	if err := tokens.RegisterIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("register token indexes: %w", err)
	}
	if err := controllers.RegisterBookingIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("register booking indexes: %w", err)
	}
	if err := controllers.Setup(mgr, cfg, platformCA); err != nil {
		return err
	}

	// 认证组件。
	secureCookie := strings.HasPrefix(strings.ToLower(cfg.Auth.PublicURL), "https://")
	session := auth.NewSession(sessionKey, cfg.Auth.SessionTTL.Duration, secureCookie)
	authn := &auth.Authenticator{Session: session, Reader: mgr.GetClient()}
	static := auth.NewStatic(mgr.GetClient(), cfg.SystemNamespace)
	oidcLogin := auth.NewOIDC(&cfg.Auth, mgr.GetClient(), cfg.SystemNamespace, session, secureCookie)
	if oidcLogin != nil {
		if err := oidcLogin.Init(ctx); err != nil {
			log.Error(err, "oidc provider discovery failed; will retry on first login")
		}
	}
	tokenSvc := &tokens.Service{Client: mgr.GetClient(), Config: cfg, PlatformCAPEM: platformCA.CertPEM}

	// 8080：UI + /auth + /api。
	httpMux := http.NewServeMux()
	auditRT, err := setupAudit(ctx, direct, cfg, mgr)
	if err != nil {
		return err
	}
	api.New(api.Deps{
		Client:   mgr.GetClient(),
		Config:   cfg,
		Auth:     authn,
		Static:   static,
		OIDC:     oidcLogin,
		Tokens:   tokenSvc,
		Audit:    auditRT.Querier,
		Recorder: audit.NewRecorder(auditRT.Pipeline),
		VirtualClients: &controllers.AdminKubeconfigClients{
			Reader: direct, Scheme: scheme, SystemNamespace: cfg.SystemNamespace,
		},
		PodLogs: podLogsFunc(kubeClient),
	}).Register(httpMux)
	httpMux.HandleFunc("GET /healthz", okHandler)
	// readyz：平台 CA / 会话密钥 / manager 证书在此之前已就绪，这里再确认 cache 已同步。
	httpMux.HandleFunc("GET /readyz", readyzHandler(mgr))
	httpMux.Handle("/", ui.Handler())

	// 8443：TokenReview 与审计 webhook，mTLS 必需。
	tlsMux := http.NewServeMux()
	(&TokenReviewHandler{Client: mgr.GetClient(), Tokens: tokenSvc}).Register(tlsMux)
	(&KubeletMetricsHandler{Client: mgr.GetClient(), Fetch: HostKubeletFetch(kubeClient)}).Register(tlsMux)
	if auditRT.Enabled {
		(&AuditWebhookHandler{Pipeline: auditRT.Pipeline}).Register(tlsMux)
	}
	holder := &atomic.Pointer[tls.Certificate]{}
	holder.Store(managerCert)
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  platformCA.CertPool(),
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return holder.Load(), nil
		},
	}

	if err := mgr.Add(&httpServer{name: "http", addr: o.httpListen, handler: httpMux}); err != nil {
		return err
	}
	if err := mgr.Add(&httpServer{name: "webhook", addr: o.tlsListen, handler: tlsMux, tlsConfig: tlsCfg}); err != nil {
		return err
	}
	if err := mgr.Add(&certRotator{client: direct, sysNS: cfg.SystemNamespace, ca: platformCA, holder: holder}); err != nil {
		return err
	}

	log.Info("starting manager",
		"http", o.httpListen, "tls", o.tlsListen, "metrics", o.metricsListen,
		"apiDomain", cfg.APIDomain, "appsDomain", cfg.AppsDomain, "leaderElect", o.leaderElect)
	return mgr.Start(ctx)
}

// managedCacheSelectors 本 manager 创建的宿主对象都带 app.kubernetes.io/managed-by=vk8s-manager，
// 这些类型的 informer 只缓存带该标签的对象，避免把全集群的 Service/RBAC 等都装进内存。
// Node、Namespace 与自有 CRD 不受限。
func managedCacheSelectors(systemNamespace string) map[client.Object]cache.ByObject {
	sel := labels.SelectorFromSet(labels.Set{controllers.LabelManagedBy: controllers.ManagedByValue})
	restricted := []client.Object{
		&appsv1.StatefulSet{},
		&corev1.Service{},
		&corev1.ResourceQuota{},
		&corev1.LimitRange{},
		&corev1.ServiceAccount{},
		&networkingv1.NetworkPolicy{},
		&rbacv1.Role{},
		&rbacv1.RoleBinding{},
		&rbacv1.ClusterRole{},
		&rbacv1.ClusterRoleBinding{},
	}
	out := make(map[client.Object]cache.ByObject, len(restricted))
	for _, obj := range restricted {
		out[obj] = cache.ByObject{Label: sel}
	}
	// 应用目录的安装 Job：只在 vk8s-system、只看带 vk8s.io/addon-install 标签的（RBAC 是该 ns 的 Role）
	addonJobs, _ := labels.Parse(names.LabelAddonInstall)
	out[&batchv1.Job{}] = cache.ByObject{Label: addonJobs, Namespaces: map[string]cache.Config{systemNamespace: {}}}
	return out
}

func okHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok"))
}

// readyzHandler cache 同步完成才返回 200；Deployment 的 readiness 探针用它。
func readyzHandler(mgr ctrl.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			http.Error(w, "cache not synced", http.StatusServiceUnavailable)
			return
		}
		okHandler(w, r)
	}
}

// httpServer 作为 manager Runnable 运行的 HTTP(S) server；不受 leader election 约束，每个副本都服务。
type httpServer struct {
	name      string
	addr      string
	handler   http.Handler
	tlsConfig *tls.Config
}

// Start 实现 manager.Runnable。
func (s *httpServer) Start(ctx context.Context) error {
	log := ctrl.Log.WithName("server").WithValues("name", s.name, "addr", s.addr)
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.addr, err)
	}
	srv := &http.Server{
		Handler:           s.handler,
		TLSConfig:         s.tlsConfig,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errCh := make(chan error, 1)
	go func() {
		if s.tlsConfig != nil {
			errCh <- srv.ServeTLS(ln, "", "")
		} else {
			errCh <- srv.Serve(ln)
		}
	}()
	log.Info("listening")
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("server %s: %w", s.name, err)
	}
}

// NeedLeaderElection HTTP 服务在每个副本上都运行。
func (s *httpServer) NeedLeaderElection() bool { return false }

// certRotator 周期性检查 manager 服务端证书，剩余不足 30 天时换发并热替换。
type certRotator struct {
	client client.Client
	sysNS  string
	ca     *pki.CA
	holder *atomic.Pointer[tls.Certificate]
}

// Start 实现 manager.Runnable。
func (c *certRotator) Start(ctx context.Context) error {
	ticker := time.NewTicker(certRotateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			cert, err := ensureManagerCert(ctx, c.client, c.sysNS, c.ca)
			if err != nil {
				ctrl.Log.WithName("cert-rotator").Error(err, "check manager certificate")
				continue
			}
			c.holder.Store(cert)
		}
	}
}

// NeedLeaderElection 每个副本各自持有并刷新证书。
func (c *certRotator) NeedLeaderElection() bool { return false }

// podLogsFunc 读安装 Job 的 Pod 日志（末尾 tail 行）。
func podLogsFunc(cs kubernetes.Interface) func(ctx context.Context, ns, pod string, tail int64) (string, error) {
	return func(ctx context.Context, ns, pod string, tail int64) (string, error) {
		b, err := cs.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{TailLines: &tail}).DoRaw(ctx)
		return string(b), err
	}
}
