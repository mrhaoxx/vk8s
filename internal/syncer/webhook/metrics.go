package webhook

import (
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// 配额 webhook 的指标，挂在 syncer 的 /metrics（controller-runtime registry）。
var (
	// QuotaDenied 被拒绝的对象数，按超出的资源名计（一个对象超出两项计两次）。
	QuotaDenied = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vk8s_syncer_quota_denied_total",
		Help: "Objects denied by the quota webhook, by exceeded resource name.",
	}, []string{"resource"})
	// QuotaSkipped webhook 自身原因放行的请求数：no_quota（宿主 RQ 不存在）、error（解码 / 读缓存 / 求值失败）。
	QuotaSkipped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vk8s_syncer_quota_skipped_total",
		Help: "Admission requests the quota webhook allowed without a verdict, by reason.",
	}, []string{"reason"})
)

func init() {
	crmetrics.Registry.MustRegister(QuotaDenied, QuotaSkipped)
}
