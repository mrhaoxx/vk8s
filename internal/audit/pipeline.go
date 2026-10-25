package audit

import (
	"context"
	"errors"
	"sync"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Options 管线参数；零值取默认。
type Options struct {
	QueueSize     int           // 默认 50000
	BatchSize     int           // 默认 2000
	BatchInterval time.Duration // 默认 2s
	MaxBackoff    time.Duration // 默认 30s
	FlushTimeout  time.Duration // 默认 10s：ctx 取消后最多等这么久把队列刷完
}

// maxRejectedRetries 存储明确拒绝（非 ErrUnavailable）的一批最多重试几次，之后整批丢弃。
const maxRejectedRetries = 3

func (o Options) withDefaults() Options {
	if o.QueueSize <= 0 {
		o.QueueSize = 50000
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 2000
	}
	if o.BatchInterval <= 0 {
		o.BatchInterval = 2 * time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 30 * time.Second
	}
	if o.FlushTimeout <= 0 {
		o.FlushTimeout = 10 * time.Second
	}
	return o
}

// Pipeline 有界内存队列 + 单写入协程：凑够一批或到时间就写；存储不可用时退避重试同一批，不丢；
// 存储明确拒绝的批有限重试后丢弃并计数，避免毒丸卡死整条管线。
type Pipeline struct {
	sink  Sink
	opts  Options
	queue chan Event
	mu    sync.Mutex // 保护 Enqueue 的「检查 + 全部入队」，保证全有或全无
}

// NewPipeline 构造管线；Run 之前也可以 Enqueue（受 QueueSize 限制）。
func NewPipeline(sink Sink, opts Options) *Pipeline {
	opts = opts.withDefaults()
	return &Pipeline{sink: sink, opts: opts, queue: make(chan Event, opts.QueueSize)}
}

// Enqueue 全部入队或全部拒绝；拒绝时计入 dropped{queue_full}。
func (p *Pipeline) Enqueue(events ...Event) bool {
	if len(events) == 0 {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if cap(p.queue)-len(p.queue) < len(events) {
		DroppedTotal.WithLabelValues("queue_full").Add(float64(len(events)))
		return false
	}
	// 只有消费者取、只有持锁者放，空间检查通过后发送不会阻塞。
	for _, e := range events {
		p.queue <- e
		EventsTotal.WithLabelValues(string(e.Source)).Inc()
	}
	QueueDepth.Set(float64(len(p.queue)))
	return true
}

// Depth 当前排队的事件数。
func (p *Pipeline) Depth() int { return len(p.queue) }

// Start 实现 controller-runtime 的 Runnable。
func (p *Pipeline) Start(ctx context.Context) error { return p.Run(ctx) }

// NeedLeaderElection 每个 manager 副本各自写自己收到的事件。
func (p *Pipeline) NeedLeaderElection() bool { return false }

// Run 写入循环；ctx 取消后最多 FlushTimeout（总预算）刷完队列，刷不完的计入 dropped{shutdown}。
func (p *Pipeline) Run(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("audit-pipeline")
	var deadline time.Time // ctx 取消后的总预算截止时刻（writeWithRetry 在退避中遇到取消时也会设置它）
	for {
		batch, closed := p.collect(ctx)
		if ctx.Err() != nil && deadline.IsZero() {
			deadline = time.Now().Add(p.opts.FlushTimeout)
		}
		if len(batch) > 0 && !p.writeWithRetry(ctx, log, batch, &deadline) {
			DroppedTotal.WithLabelValues("shutdown").Add(float64(len(batch)))
			p.drainDropped()
			return nil
		}
		if closed {
			return nil
		}
	}
}

// collect 阻塞到凑够 BatchSize、到 BatchInterval、或 ctx 取消；ctx 取消后把队列里已有的取出，closed 表示队列已空。
func (p *Pipeline) collect(ctx context.Context) ([]Event, bool) {
	var batch []Event
	timer := time.NewTimer(p.opts.BatchInterval)
	defer timer.Stop()
	for {
		select {
		case e := <-p.queue:
			batch = append(batch, e)
			QueueDepth.Set(float64(len(p.queue)))
			if len(batch) >= p.opts.BatchSize {
				return batch, false
			}
		case <-timer.C:
			if len(batch) > 0 {
				return batch, false
			}
			timer.Reset(p.opts.BatchInterval)
		case <-ctx.Done():
			for len(p.queue) > 0 && len(batch) < p.opts.BatchSize {
				batch = append(batch, <-p.queue)
			}
			QueueDepth.Set(float64(len(p.queue)))
			return batch, len(p.queue) == 0
		}
	}
}

// writeWithRetry 写一批。存储不可用：1s 起倍增退避重试，不丢；存储明确拒绝：最多 maxRejectedRetries 次后整批丢弃（dropped{rejected}），返回 true 让管线继续。
// ctx 取消后退避会被打断，所有等待与写入都受 *deadline（整个退出过程的总预算，跨批共享）约束；预算耗尽返回 false。
func (p *Pipeline) writeWithRetry(ctx context.Context, log interface{ Error(error, string, ...any) }, batch []Event, deadline *time.Time) bool {
	backoff := min(time.Second, p.opts.MaxBackoff)
	rejected := 0
	for {
		wctx, cancel := context.WithoutCancel(ctx), func() {}
		if !deadline.IsZero() {
			remaining := time.Until(*deadline)
			if remaining <= 0 {
				return false
			}
			wctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), remaining)
		}
		start := time.Now()
		err := p.sink.Write(wctx, batch)
		cancel()
		WriteDuration.Observe(time.Since(start).Seconds())
		if err == nil {
			SinkUp.Set(1)
			return true
		}
		WriteErrors.Inc()
		if errors.Is(err, ErrUnavailable) {
			SinkUp.Set(0)
		} else {
			rejected++
			if rejected > maxRejectedRetries {
				log.Error(err, "audit batch rejected by sink, dropping it", "events", len(batch), "retries", rejected-1)
				DroppedTotal.WithLabelValues("rejected").Add(float64(len(batch)))
				return true
			}
		}
		log.Error(err, "audit batch write failed, will retry", "events", len(batch), "backoff", backoff.String())
		if ctx.Err() != nil && deadline.IsZero() {
			*deadline = time.Now().Add(p.opts.FlushTimeout)
		}
		wait := backoff
		if !deadline.IsZero() {
			if remaining := time.Until(*deadline); remaining <= 0 {
				return false
			} else if wait > remaining {
				wait = remaining
			}
		}
		timer := time.NewTimer(wait)
		if deadline.IsZero() {
			// 正常运行：退避可被 ctx 取消打断，随后进入有预算的刷写
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				*deadline = time.Now().Add(p.opts.FlushTimeout)
			}
		} else {
			<-timer.C
		}
		if backoff *= 2; backoff > p.opts.MaxBackoff {
			backoff = p.opts.MaxBackoff
		}
	}
}

func (p *Pipeline) drainDropped() {
	n := 0
	for len(p.queue) > 0 {
		<-p.queue
		n++
	}
	if n > 0 {
		DroppedTotal.WithLabelValues("shutdown").Add(float64(n))
	}
	QueueDepth.Set(0)
}
