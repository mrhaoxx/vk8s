package resources

import (
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Prometheus 指标，注册进 controller-runtime 的 Registry，由 health.go 的 /metrics 暴露。
var (
	// SyncErrors 各资源同步错误计数（宿主 API 拒绝等，会重试）。
	SyncErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vk8s_syncer_sync_errors_total",
		Help: "宿主 API 返回错误的同步次数，按资源类型统计。",
	}, []string{"resource"})
	// TranslateRejected 翻译阶段拒绝计数（不重试）。
	TranslateRejected = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vk8s_syncer_translate_rejected_total",
		Help: "翻译阶段拒绝（nodeName 不在白名单、Ingress host 不合规等）的次数，按资源类型统计。",
	}, []string{"resource"})
	// SATokenRenewFailures SA token 签发 / 续签失败计数。
	SATokenRenewFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vk8s_syncer_sa_token_renew_failures_total",
		Help: "虚拟 ServiceAccount token 签发或续签失败的次数。",
	})
	// OrphansDeleted 启动 / 周期清理删除的宿主孤儿对象数。
	OrphansDeleted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vk8s_syncer_orphans_deleted_total",
		Help: "宿主 ns 内 managed=true 但虚拟对象已不存在而被删除的对象数，按资源类型统计。",
	}, []string{"resource"})
	// PlacementEvictions 因所在节点不再被 nodePlacement 允许而被删除的虚拟 Pod 数。
	PlacementEvictions = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vk8s_syncer_placement_evictions_total",
		Help: "所在节点不再被 nodePlacement 允许而被驱逐（删除）的虚拟 Pod 数。",
	})
)

func init() {
	crmetrics.Registry.MustRegister(SyncErrors, TranslateRejected, SATokenRenewFailures, OrphansDeleted, PlacementEvictions)
}
