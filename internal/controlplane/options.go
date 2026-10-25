package controlplane

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiserverv1beta1 "k8s.io/apiserver/pkg/apis/apiserver/v1beta1"
	"sigs.k8s.io/yaml"

	"vk8s.io/vk8s/internal/names"
)

// Options 控制平面进程的启动参数，由 cobra flags 与环境变量填充。
// operator 以固定的 flag 集合启动本进程（见 docs 第 5 节第 10 步），字段名与 flag 一一对应。
type Options struct {
	// VClusterName 所属虚拟集群名，用作 kcm 的 --cluster-name。
	VClusterName string
	// CertsDir 证书 Secret 的挂载目录，默认 names.CertsDir。
	CertsDir string
	// DataDir sqlite 数据目录（PVC），默认 names.DataDir。
	DataDir string
	// RunDir 运行时目录（emptyDir），放 kine 与 konnectivity 的 unix socket 以及 egress selector 配置。
	RunDir string
	// ServiceClusterIPRange 必须与宿主集群一致（见 docs 7.4）。
	ServiceClusterIPRange string
	// PodIP 来自环境变量 POD_IP，作为 apiserver 的 --advertise-address。
	PodIP string
	// BindAddress apiserver 的 --bind-address。容器内为 0.0.0.0；本机冒烟测试可设为 127.0.0.1 以免对外监听。
	BindAddress string
	// Verbosity 透传给 apiserver 与 kcm 的 --v。两者必须相同（见 init 中 ReapplyHandling 的说明）。
	Verbosity string
	// LoggingFormat 透传给 apiserver 与 kcm 的 --logging-format（json | text）。
	LoggingFormat string
	// AuditWebhookKubeconfig apiserver 审计 webhook 的 kubeconfig 路径；非空即启用审计（docs 2026-10-24 审计 §3.2）。
	AuditWebhookKubeconfig string
}

// Paths 由 Options 推导出的全部路径与端点。目录取默认值时与 names 包常量完全一致。
type Paths struct {
	SQLiteDSN          string // kine 后端 DSN
	KineListener       string // kine 监听地址（unix://）
	EtcdServer         string // apiserver --etcd-servers
	KineSocket         string // kine socket 文件路径
	KonnectivitySocket string // egress selector 的 UDS 路径
	EgressSelectorFile string // 写入 run-dir 的 egress selector 配置
	SQLiteDir          string // sqlite 文件所在目录

	PlatformCA        string
	InternalCA        string
	APIServerCert     string
	APIServerKey      string
	KubeletClientCert string
	KubeletClientKey  string
	FrontProxyCert    string
	FrontProxyKey     string
	SAPub             string
	SAKey             string
	KCMKubeconfig     string
	WebhookKubeconfig string
	AuditPolicyFile   string // 写入 run-dir 的审计策略（审计启用时）
}

// Paths 计算全部路径。
func (o Options) Paths() Paths {
	p := Paths{
		PlatformCA:        o.certFile(names.KeyPlatformCA),
		InternalCA:        o.certFile(names.KeyInternalCA),
		APIServerCert:     o.certFile(names.KeyAPIServerCert),
		APIServerKey:      o.certFile(names.KeyAPIServerKey),
		KubeletClientCert: o.certFile(names.KeyKubeletClientCert),
		KubeletClientKey:  o.certFile(names.KeyKubeletClientKey),
		FrontProxyCert:    o.certFile(names.KeyFrontProxyCert),
		FrontProxyKey:     o.certFile(names.KeyFrontProxyKey),
		SAPub:             o.certFile(names.KeyServiceAccountPub),
		SAKey:             o.certFile(names.KeyServiceAccountKey),
		KCMKubeconfig:     o.certFile(names.KeyKCMKubeconfig),
		WebhookKubeconfig: o.certFile(names.KeyWebhookKubeconfig),
	}

	// 目录为默认值时直接用 names 常量，保证与 operator / syncer 的约定字面相同。
	if o.DataDir == names.DataDir {
		p.SQLiteDSN = names.KineEndpoint
		p.SQLiteDir = filepath.Dir(names.SQLitePath)
	} else {
		db := filepath.Join(o.DataDir, "db", "state.db")
		p.SQLiteDSN = "sqlite://" + db + "?_journal=WAL&_busy_timeout=30000"
		p.SQLiteDir = filepath.Dir(db)
	}
	if o.RunDir == names.RunDir {
		p.KineSocket = names.KineSocket
		p.KineListener = names.KineListener
		p.EtcdServer = names.APIServerEtcdServer
		p.KonnectivitySocket = names.KonnectivitySocket
	} else {
		p.KineSocket = filepath.Join(o.RunDir, "kine.sock")
		p.KineListener = "unix://" + p.KineSocket
		p.EtcdServer = "unix://" + p.KineSocket
		p.KonnectivitySocket = filepath.Join(o.RunDir, "konnectivity.sock")
	}
	p.EgressSelectorFile = filepath.Join(o.RunDir, names.KeyEgressSelectorConf)
	p.AuditPolicyFile = filepath.Join(o.RunDir, names.AuditPolicyFileName)
	return p
}

func (o Options) certFile(key string) string { return filepath.Join(o.CertsDir, key) }

func (o Options) bindAddress() string {
	if o.BindAddress == "" {
		return "0.0.0.0"
	}
	return o.BindAddress
}

// Validate 校验必填项。
func (o Options) Validate() error {
	var missing []string
	if o.VClusterName == "" {
		missing = append(missing, "--vcluster-name")
	}
	if o.ServiceClusterIPRange == "" {
		missing = append(missing, "--service-cluster-ip-range")
	}
	if len(missing) > 0 {
		return fmt.Errorf("control-plane: missing required flags: %s", strings.Join(missing, ", "))
	}
	switch o.LoggingFormat {
	case "", "json", "text":
	default:
		return fmt.Errorf("control-plane: --logging-format must be json or text, got %q", o.LoggingFormat)
	}
	return nil
}

// ServiceAccountIssuer 虚拟集群内 SA token 的 issuer，同时也是 --api-audiences。
const ServiceAccountIssuer = "https://kubernetes.default.svc.cluster.local"

// EgressSelectionCluster egress selector 中代表 apiserver → 节点/Pod 流量的网络上下文名。
const EgressSelectionCluster = "cluster"

// ShutdownWatchGracePeriod apiserver 优雅退出时等待 watch 请求结束的宽限期（见 APIServerArgs）。
const ShutdownWatchGracePeriod = 5 * time.Second

// loggingArgs 两个组件共用的日志参数。必须保持完全一致：component-base 的日志配置在进程内
// 只允许应用一次，第二次应用仅在配置相同的情况下被忽略（见 init）。
func loggingArgs(o Options) []string {
	args := []string{}
	if o.Verbosity != "" {
		args = append(args, "--v="+o.Verbosity)
	}
	if o.LoggingFormat != "" {
		args = append(args, "--logging-format="+o.LoggingFormat)
	}
	return args
}

// APIServerArgs 组装 kube-apiserver 的完整参数（docs 第 6 节）。纯函数，便于测试。
func APIServerArgs(o Options) []string {
	p := o.Paths()
	args := []string{
		// 存储：kine 提供的 etcd 兼容端点
		"--etcd-servers=" + p.EtcdServer,

		// 监听
		"--secure-port=" + fmt.Sprint(names.APIServerPort),
		"--bind-address=" + o.bindAddress(),
	}
	if o.PodIP != "" {
		args = append(args, "--advertise-address="+o.PodIP)
	}
	args = append(args,
		// 服务端证书：平台 CA 签发，与所有 kubeconfig 共用同一 CA
		"--tls-cert-file="+p.APIServerCert,
		"--tls-private-key-file="+p.APIServerKey,

		// 客户端证书认证：集群内部 CA（kcm、syncer、manager 修复用证书）
		"--client-ca-file="+p.InternalCA,

		// 平台 token：回调 manager 的 TokenReview webhook
		"--authentication-token-webhook-config-file="+p.WebhookKubeconfig,
		"--authentication-token-webhook-cache-ttl=2m",
		"--authentication-token-webhook-version=v1",
		// /readyz 对匿名开放，供 kubelet 探针使用
		"--anonymous-auth=true",
		"--authorization-mode=Node,RBAC",

		// Service CIDR 与宿主一致（docs 7.4）
		"--service-cluster-ip-range="+o.ServiceClusterIPRange,

		// ServiceAccount token
		"--service-account-issuer="+ServiceAccountIssuer,
		"--service-account-signing-key-file="+p.SAKey,
		"--service-account-key-file="+p.SAPub,
		"--api-audiences="+ServiceAccountIssuer,

		// apiserver → kubelet（实际是经隧道到达 syncer 的假 kubelet，docs 7.7）。
		// 不设 --kubelet-certificate-authority：连接不出 pod，无需校验假 kubelet 的证书。
		"--kubelet-client-certificate="+p.KubeletClientCert,
		"--kubelet-client-key="+p.KubeletClientKey,
		// 优先用 IP 连接 kubelet，使 CONNECT 目标为 <IP>:10250，便于 syncer 按端口分流
		"--kubelet-preferred-address-types=InternalIP,ExternalIP,Hostname",

		// cluster 网络上下文 → HTTPConnect over UDS → syncer
		"--egress-selector-config-file="+p.EgressSelectorFile,

		// 聚合层：直接路由到 Endpoints（syncer pod IP:8443），经 egress 隧道
		"--enable-aggregator-routing=true",
		"--requestheader-client-ca-file="+p.InternalCA,
		"--requestheader-allowed-names="+names.CNFrontProxyClient,
		"--requestheader-username-headers=X-Remote-User",
		"--requestheader-group-headers=X-Remote-Group",
		"--requestheader-extra-headers-prefix=X-Remote-Extra-",
		"--proxy-client-cert-file="+p.FrontProxyCert,
		"--proxy-client-key-file="+p.FrontProxyKey,

		// default/kubernetes 的 Endpoints 由 syncer 接管（docs 7.4）
		"--endpoint-reconciler-type=none",

		// 虚拟集群允许 privileged Pod；是否真的允许由宿主 ns 的 PSA 标签决定（docs 7.3 第 6 步）
		"--allow-privileged=true",

		// 优雅退出：默认（0）不主动终止 watch，http.Server.Shutdown 要等到 --request-timeout（60s）
		// 才强制关闭仍有 watch 流的连接；同进程的 kcm 与 syncer 一直在 watch，SIGTERM 后 apiserver
		// 必然卡满 60s。设一个短的 watch 终止宽限期，让连接空闲后 Shutdown 立刻返回。
		"--shutdown-watch-termination-grace-period="+ShutdownWatchGracePeriod.String(),

		// 小集群资源预算
		"--default-watch-cache-size=20",
		"--max-requests-inflight=100",
		"--max-mutating-requests-inflight=50",
		"--profiling=false",
	)
	args = append(args, auditArgs(o, p)...)
	return append(args, loggingArgs(o)...)
}

// auditArgs 审计启用（给了 webhook kubeconfig）时追加的 apiserver 参数（docs 2026-10-24 审计 §3.2）：
// batch 模式不阻塞租户请求；缓冲 20000 条、单事件 100 KiB、单批 10 MiB 的截断由 apiserver 侧完成。
func auditArgs(o Options, p Paths) []string {
	if o.AuditWebhookKubeconfig == "" {
		return nil
	}
	return []string{
		"--audit-policy-file=" + p.AuditPolicyFile,
		"--audit-webhook-config-file=" + o.AuditWebhookKubeconfig,
		"--audit-webhook-mode=batch",
		"--audit-webhook-batch-max-size=400",
		"--audit-webhook-batch-max-wait=2s",
		"--audit-webhook-batch-buffer-size=20000",
		"--audit-webhook-initial-backoff=2s",
		"--audit-webhook-truncate-enabled=true",
		"--audit-webhook-truncate-max-event-size=102400",
		"--audit-webhook-truncate-max-batch-size=10485760",
		"--audit-webhook-version=audit.k8s.io/v1",
	}
}

// DisabledControllers kcm 中禁用的控制器（docs 第 6 节）。
// 节点、存储卷、CSR 相关控制器由宿主集群负责；Node 对象在虚拟集群中只是只读镜像。
// taint-eviction 不在设计清单中，但与 nodelifecycle 同理：节点 taint 驱逐由宿主完成，
// 虚拟侧重复驱逐无意义，故一并禁用。
var DisabledControllers = []string{
	"nodeipam",
	"nodelifecycle",
	"taint-eviction-controller",
	"persistentvolume-binder",
	"attachdetach",
	"persistentvolume-expander",
	"cloud-node-lifecycle",
	"ttl",
	"csrsigning",
}

// ControllerManagerArgs 组装 kube-controller-manager 的完整参数（docs 第 6 节）。纯函数。
func ControllerManagerArgs(o Options) []string {
	p := o.Paths()
	controllers := []string{"*"}
	for _, c := range DisabledControllers {
		controllers = append(controllers, "-"+c)
	}
	args := []string{
		"--kubeconfig=" + p.KCMKubeconfig,
		// kcm 自身 10257 端口的 authn/authz 委托；kcm 证书为 system:masters，可执行 TokenReview/SAR
		"--authentication-kubeconfig=" + p.KCMKubeconfig,
		"--authorization-kubeconfig=" + p.KCMKubeconfig,
		"--controllers=" + strings.Join(controllers, ","),

		// 注入到每个 ns 的 kube-root-ca.crt；Pod 内 client 用它校验 apiserver，必须是签服务端证书的平台 CA
		"--root-ca-file=" + p.PlatformCA,
		"--service-account-private-key-file=" + p.SAKey,
		"--use-service-account-credentials=true",

		// 单副本，无需选举
		"--leader-elect=false",
		"--cluster-name=" + o.VClusterName,

		// 只在本地回环提供 /healthz 与 /metrics，证书在内存中自签（CertDirectory 为空）
		"--bind-address=127.0.0.1",
		"--secure-port=10257",
	}
	return append(args, loggingArgs(o)...)
}

// EgressSelectorConfigYAML 生成 apiserver 的 egress selector 配置：
// cluster 网络上下文的连接以 HTTPConnect 发到 syncer 监听的 unix socket。
func EgressSelectorConfigYAML(udsPath string) ([]byte, error) {
	cfg := apiserverv1beta1.EgressSelectorConfiguration{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apiserverv1beta1.SchemeGroupVersion.String(),
			Kind:       "EgressSelectorConfiguration",
		},
		EgressSelections: []apiserverv1beta1.EgressSelection{{
			Name: EgressSelectionCluster,
			Connection: apiserverv1beta1.Connection{
				ProxyProtocol: apiserverv1beta1.ProtocolHTTPConnect,
				Transport: &apiserverv1beta1.Transport{
					UDS: &apiserverv1beta1.UDSTransport{UDSName: udsPath},
				},
			},
		}},
	}
	return yaml.Marshal(&cfg)
}
