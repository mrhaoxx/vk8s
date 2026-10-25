// Package metrics 定义 manager 的 Prometheus 指标，注册到 controller-runtime 自带的 registry，
// 与 reconcile 指标一起在 metrics 端口暴露。
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// TokenReviewDuration TokenReview webhook 处理时长，按结果分布。
	TokenReviewDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "vk8s_tokenreview_duration_seconds",
		Help:    "TokenReview webhook 处理时长（秒）。",
		Buckets: prometheus.DefBuckets,
	}, []string{"result"})

	// LoginTotal 登录次数，按方式与结果计数。
	LoginTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vk8s_login_total",
		Help: "登录尝试次数。method=static|oidc，result=success|failure|ratelimited。",
	}, []string{"method", "result"})

	// TokensIssuedTotal 签发的 kubectl token 数。
	TokensIssuedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vk8s_tokens_issued_total",
		Help: "签发的 AccessToken 数。",
	})

	// TokensRevokedTotal 通过 API 撤销的 token 数。
	TokensRevokedTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vk8s_tokens_revoked_total",
		Help: "通过管理 API 撤销的 AccessToken 数。",
	})
)

// TokenReview 结果标签取值。
const (
	ResultAuthenticated   = "authenticated"
	ResultUnauthenticated = "unauthenticated"
	ResultError           = "error"
	ResultRejected        = "rejected" // mTLS 身份与路径不符、方法/请求体非法
)

// BookingsActive 当前生效中的节点预约数。
var BookingsActive = prometheus.NewGauge(prometheus.GaugeOpts{
	Name: "vk8s_bookings_active",
	Help: "Number of node bookings currently active (lease in effect).",
})

// BookingsTotal 进入终态的节点预约数，按结果分（completed / rejected / revoked）。
var BookingsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "vk8s_bookings_total",
	Help: "Node bookings that reached a terminal phase, by result.",
}, []string{"result"})

func init() {
	ctrlmetrics.Registry.MustRegister(TokenReviewDuration, LoginTotal, TokensIssuedTotal, TokensRevokedTotal, BookingsActive, BookingsTotal)
}
