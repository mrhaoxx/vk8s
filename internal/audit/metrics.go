package audit

import (
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// EventsTotal 入队的事件数，按来源。
	EventsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vk8s_audit_events_total",
		Help: "Audit events accepted into the pipeline, by source.",
	}, []string{"source"})
	// DroppedTotal 丢弃的事件数：queue_full（入队时队列已满）、shutdown（退出时没刷完）。
	DroppedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vk8s_audit_dropped_total",
		Help: "Audit events dropped, by reason.",
	}, []string{"reason"})
	// QueueDepth 队列里等待写入的事件数。
	QueueDepth = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vk8s_audit_queue_depth",
		Help: "Audit events waiting in the in-memory queue.",
	})
	// WriteDuration 一批写入的耗时。
	WriteDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "vk8s_audit_write_duration_seconds",
		Help:    "Duration of one batch write to the audit sink.",
		Buckets: prometheus.DefBuckets,
	})
	// WriteErrors 写入失败次数（每次重试算一次）。
	WriteErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vk8s_audit_write_errors_total",
		Help: "Failed batch writes to the audit sink (each retry counts).",
	})
	// SinkUp 存储是否可用（最近一次 Ping / 迁移 / 写入的结果）。
	SinkUp = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "vk8s_audit_sink_up",
		Help: "1 when the audit sink is reachable, 0 otherwise.",
	})
)

func init() {
	ctrlmetrics.Registry.MustRegister(EventsTotal, DroppedTotal, QueueDepth, WriteDuration, WriteErrors, SinkUp)
}
