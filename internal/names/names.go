// Package names 集中定义平台在两侧（宿主集群与虚拟集群）使用的全部名字、标签、
// 挂载路径、端口。operator 写入、control-plane / syncer 读取，任何一方改名都必须改这里。
package names

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	Group = "vk8s.io"

	// 默认平台 namespace，可由配置覆盖。
	DefaultSystemNamespace = "vk8s-system"

	// 标签 / 注解
	LabelVCluster   = "vk8s.io/vcluster"   // 宿主 ns 与平台对象上：所属虚拟集群
	LabelOwner      = "vk8s.io/owner"      // 宿主 ns 上：owner 用户名
	LabelManaged    = "vk8s.io/managed"    // syncer 创建的宿主对象 = "true"
	LabelVName      = "vk8s.io/vname"      // 宿主对象上：虚拟对象名
	LabelVNamespace = "vk8s.io/vnamespace" // 宿主对象上：虚拟 namespace；也注入 Service selector
	AnnotationVUID  = "vk8s.io/vuid"       // 宿主对象上：虚拟对象 UID
	LabelPrefix     = "vk8s.io/"           // 用户对象上同前缀的 label/annotation 被丢弃

	FinalizerCleanup = "vk8s.io/cleanup"

	// 节点预约（docs 2026-10-24 节点预约设计）。
	LabelBookable     = "vk8s.io/bookable" // 宿主 Node 上：=true 表示只能通过 NodeBooking 租约使用
	BookableTrue      = "true"
	FinalizerLease    = "vk8s.io/lease" // NodeBooking 上：删除前先撤销租约
	BookingNamePrefix = "nb-"

	// 审计（docs 2026-10-24 审计设计）。
	KeyAuditWebhookKubeconfig = "audit-webhook.kubeconfig" // 证书 Secret 里：apiserver 审计 webhook 的 kubeconfig（审计启用时才有）
	AuditWebhookPathPrefix    = "/webhook/audit/"          // manager 8443：POST /webhook/audit/{vc}
	AuditPolicyFileName       = "audit-policy.yaml"        // 控制平面 run-dir 里的策略文件

	// TokenReview 返回的 group；虚拟集群内绑定到 cluster-admin
	GroupAdmins = "vk8s:admins"
	// 虚拟集群内由 syncer 自愈的 ClusterRoleBinding 名
	AdminsClusterRoleBinding = "vk8s:admins"

	// vk8s-system 内的 Secret
	PlatformCASecret   = "vk8s-ca"
	SessionKeySecret   = "vk8s-session-key"
	ManagerCertsSecret = "vk8s-manager-certs"
	// 租户 ns 内的泛证书副本
	AppsTLSSecretName = "vk8s-apps-tls"
	// 租户 ns 内的 LimitRange 名；syncer 把它镜像进每个虚拟 namespace
	LimitRangeName = "vk8s-limits"

	// 容器名
	ContainerControlPlane = "control-plane"
	ContainerSyncer       = "syncer"

	// 容器内挂载路径
	CertsDir = "/certs"
	DataDir  = "/data"
	RunDir   = "/run/vk8s"

	KineSocket          = RunDir + "/kine.sock"
	KonnectivitySocket  = RunDir + "/konnectivity.sock"
	SQLitePath          = DataDir + "/db/state.db"
	KineEndpoint        = "sqlite://" + SQLitePath + "?_journal=WAL&_busy_timeout=30000"
	KineListener        = "unix://" + KineSocket
	APIServerEtcdServer = "unix://" + KineSocket

	// 证书 Secret（vc-<name>-certs）里的 key，同时是 /certs 下的文件名
	KeyPlatformCA         = "platform-ca.crt"
	KeyInternalCA         = "internal-ca.crt"
	KeyAPIServerCert      = "apiserver.crt"
	KeyAPIServerKey       = "apiserver.key"
	KeyKubeletClientCert  = "kubelet-client.crt"
	KeyKubeletClientKey   = "kubelet-client.key"
	KeyFakeKubeletCert    = "fake-kubelet.crt"
	KeyFakeKubeletKey     = "fake-kubelet.key"
	KeyFrontProxyCert     = "front-proxy-client.crt"
	KeyFrontProxyKey      = "front-proxy-client.key"
	KeySyncerServingCert  = "syncer-serving.crt" // webhook 与 metrics 服务端
	KeySyncerServingKey   = "syncer-serving.key"
	KeyServiceAccountPub  = "sa.pub"
	KeyServiceAccountKey  = "sa.key"
	KeyKCMKubeconfig      = "kcm.kubeconfig"
	KeySyncerKubeconfig   = "syncer.kubeconfig"
	KeyWebhookKubeconfig  = "webhook.kubeconfig"
	KeyEgressSelectorConf = "egress-selector.yaml"
	KeyWebhookClientCert  = "webhook-client.crt" // 虚拟 apiserver → manager 的 mTLS 客户端证书
	KeyWebhookClientKey   = "webhook-client.key"

	// 证书主体
	CNFrontProxyClient = "front-proxy-client"
	CNKubeletClient    = "vk8s:apiserver-kubelet-client"
	CNSyncer           = "vk8s:syncer"
	CNKCM              = "system:kube-controller-manager"
	CNManagerAdmin     = "vk8s:manager"
	OrgSystemMasters   = "system:masters"

	// 端口
	APIServerPort        = 6443
	ServicePort          = 443
	SyncerHealthPort     = 8080
	SyncerWebhookPort    = 9443
	SyncerMetricsAPIPort = 8443
	FakeKubeletPort      = 10250
	ManagerHTTPPort      = 8080
	ManagerTLSPort       = 8443
	GatewayPort          = 443
	GatewayHealthPort    = 8081

	// 虚拟集群内由 syncer 投放的对象
	VirtualSystemNamespace = "kube-system"
	CoreDNSName            = "coredns"
	CoreDNSServiceName     = "kube-dns"
	MetricsServiceName     = "metrics-server"
	MetricsAPIServiceName  = "v1beta1.metrics.k8s.io"
	ServiceWebhookName     = "vk8s-service-clusterip"
	ServiceWebhookPath     = "/mutate-service"
	// 平台配额在虚拟 apiserver 侧即时拒绝（设计 2026-10-25）：syncer 的验证 webhook 读宿主 RQ 的 hard/used。
	QuotaWebhookName        = "vk8s-quota"             // ValidatingWebhookConfiguration 名
	QuotaWebhookPath        = "/validate-quota"        // syncer webhook 服务上的路径
	HostResourceQuotaName   = "vk8s-quota"             // 宿主租户 ns 里 operator 维护的 ResourceQuota
	AnnotationQuotaOverhead = "vk8s.io/quota-overhead" // RQ 注解：hard 里控制平面开销的 JSON ResourceList，webhook 用它换算用户视角数字
	// TokenIDKey 访问令牌 ID 的键：TokenReview 放进 UserInfo.Extra，apiserver 原样写进审计事件的 user.extra，
	// 审计管线再落到事件 annotations，让每条 kube 操作都能对应到具体令牌。
	TokenIDKey = "vk8s.io/token-id"
	// 应用目录（设计 2026-10-25）。
	AddonFinalizer            = "vk8s.io/addon-uninstall"
	AnnotationAddonRetry      = "vk8s.io/addon-retry"
	LabelAddonInstall         = "vk8s.io/addon-install"
	AddonRunnerServiceAccount = "vk8s-addon-runner"

	// manager 路径
	TokenReviewPathPrefix = "/webhook/tokenreview/"
	TokenPrefix           = "vk8s_"

	// 域名约束
	MaxVClusterNameLength = 30
	HostLabelSeparator    = "--"

	ServiceAccountTokenTTLSeconds = 3600

	// PlacementEvictionMaxGracePeriodSeconds 节点不再被 nodePlacement 允许时，驱逐其上虚拟 Pod 的 gracePeriod 上限（秒）。
	PlacementEvictionMaxGracePeriodSeconds int64 = 30
)

// HostNamespace 返回虚拟集群在宿主集群上的 namespace。
func HostNamespace(vc string) string { return "vc-" + vc }

// ServiceName 宿主 ns 内指向控制平面的 Service。
func ServiceName(vc string) string { return "vc-" + vc }

// StatefulSetName 控制平面 StatefulSet。
func StatefulSetName(vc string) string { return "vc-" + vc }

// CertsSecretName 租户 ns 内的证书 Secret。
func CertsSecretName(vc string) string { return "vc-" + vc + "-certs" }

// InternalCASecretName vk8s-system 内保存集群内部 CA 私钥的 Secret。
func InternalCASecretName(vc string) string { return "vc-" + vc + "-ca" }

// ManagerAdminKubeconfigSecret vk8s-system 内 manager 访问该虚拟集群的 kubeconfig。
func ManagerAdminKubeconfigSecret(vc string) string { return "vc-" + vc + "-admin-kubeconfig" }

// SyncerServiceAccount syncer 的 ServiceAccount / Role / RoleBinding 名。
func SyncerServiceAccount(vc string) string { return "vc-" + vc + "-syncer" }

// SyncerClusterRole syncer 的只读 ClusterRole / ClusterRoleBinding 名。
func SyncerClusterRole(vc string) string { return "vk8s:syncer:" + vc }

// ControlPlaneServiceDNS 控制平面 Service 的集群内 FQDN。
func ControlPlaneServiceDNS(vc string) string {
	return fmt.Sprintf("%s.%s.svc", ServiceName(vc), HostNamespace(vc))
}

// APIHost 虚拟集群对外的 API 主机名。
func APIHost(vc, apiDomain string) string { return vc + "." + apiDomain }

// AppsDomain 虚拟集群的应用域名。
func AppsDomain(vc, appsDomain string) string { return vc + "." + appsDomain }

// UserPasswordSecret 静态用户密码哈希 Secret。
func UserPasswordSecret(user string) string { return "user-" + user }

// TokenHash 返回 "sha256:<hex>"。
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// AccessTokenName 由 hash 得到 AccessToken 对象名。
func AccessTokenName(tokenHash string) string {
	h := tokenHash
	if len(h) > 7 && h[:7] == "sha256:" {
		h = h[7:]
	}
	if len(h) > 16 {
		h = h[:16]
	}
	return "tok-" + h
}

// SATokenSecretName 宿主侧承载虚拟 SA token 的 Secret。
func SATokenSecretName(hostPodName string) string { return hostPodName + "-sa" }

// control-plane 进程的退出时序。apiserver 优雅退出正常只需几秒（watch 终止宽限期），
// 最坏情况是其 --request-timeout（60s）；进程等待上限与 pod 的 terminationGracePeriodSeconds 依次留余量。
const (
	ControlPlaneShutdownTimeout                     = 70 * time.Second
	ControlPlaneTerminationGracePeriodSeconds int64 = 75
)

// AddonInstallName AddonInstall 的固定名字。
func AddonInstallName(vc, template string) string { return vc + "-" + template }

// AddonSpecConfigMap 安装 Job 的 spec ConfigMap 名。
func AddonSpecConfigMap(install string) string { return "addon-" + install + "-spec" }
