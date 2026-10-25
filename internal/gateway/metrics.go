package gateway

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics 网关的 Prometheus 指标（docs 第 8 / 11 节）：每集群活跃连接数、累计字节、拒绝计数。
// 拒绝计数不带集群 label：未知 SNI 是用户可控输入，不能进入 label 以免基数爆炸。
type Metrics struct {
	Registry *prometheus.Registry

	active      *prometheus.GaugeVec
	connections *prometheus.CounterVec
	bytes       *prometheus.CounterVec
	rejected    *prometheus.CounterVec
	routes      prometheus.Gauge
}

// NewMetrics 创建并注册全部指标到独立的 registry（附带 Go 运行时与进程指标）。
func NewMetrics() *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		active: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "vk8s", Subsystem: "gateway", Name: "active_connections",
			Help: "当前透传中的连接数。",
		}, []string{"vcluster"}),
		connections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "vk8s", Subsystem: "gateway", Name: "connections_total",
			Help: "累计成功建立到后端的连接数。",
		}, []string{"vcluster"}),
		bytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "vk8s", Subsystem: "gateway", Name: "bytes_total",
			Help: "累计透传字节数；direction=in 为客户端→控制平面，out 为控制平面→客户端。",
		}, []string{"vcluster", "direction"}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "vk8s", Subsystem: "gateway", Name: "rejected_total",
			Help: "被拒绝的连接数，按原因分类。",
		}, []string{"reason"}),
		routes: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "vk8s", Subsystem: "gateway", Name: "routes",
			Help: "当前可路由（phase=Running）的虚拟集群数。",
		}),
	}
	m.Registry.MustRegister(
		m.active, m.connections, m.bytes, m.rejected, m.routes,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	// 预置全部拒绝原因，让曲线从 0 开始而不是首次出现才有值
	for _, r := range []string{
		RejectNoSNI, RejectBadRequest, RejectWrongDomain, RejectUnknownCluster,
		RejectNotRunning, RejectDialFailed, RejectHandshakeTimeo, RejectShuttingDown,
	} {
		m.rejected.WithLabelValues(r)
	}
	return m
}

// ConnOpened 一条连接成功接到后端。
func (m *Metrics) ConnOpened(vc string) {
	m.active.WithLabelValues(vc).Inc()
	m.connections.WithLabelValues(vc).Inc()
}

// ConnClosed 连接结束。
func (m *Metrics) ConnClosed(vc string) {
	m.active.WithLabelValues(vc).Dec()
}

// AddBytes 累加字节数。
func (m *Metrics) AddBytes(vc string, in, out int64) {
	if in > 0 {
		m.bytes.WithLabelValues(vc, "in").Add(float64(in))
	}
	if out > 0 {
		m.bytes.WithLabelValues(vc, "out").Add(float64(out))
	}
}

// Reject 记录一次拒绝。
func (m *Metrics) Reject(reason string) {
	m.rejected.WithLabelValues(reason).Inc()
}

// SetRoutes 更新可路由集群数。
func (m *Metrics) SetRoutes(n int) {
	m.routes.Set(float64(n))
}

// DeleteVCluster 集群删除后清理其 label 系列，避免长期堆积。
// v2: 在 Router.delete 中调用；v1 保留系列以便观察删除前后的曲线。
func (m *Metrics) DeleteVCluster(vc string) {
	m.active.DeleteLabelValues(vc)
	m.connections.DeleteLabelValues(vc)
	m.bytes.DeleteLabelValues(vc, "in")
	m.bytes.DeleteLabelValues(vc, "out")
}
