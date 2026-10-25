package audit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeSink struct {
	mu        sync.Mutex
	writes    [][]Event
	failFirst int
	calls     int
}

func (f *fakeSink) Write(ctx context.Context, evs []Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.failFirst {
		return errors.New("down")
	}
	f.writes = append(f.writes, append([]Event(nil), evs...))
	return nil
}
func (f *fakeSink) Ping(context.Context) error         { return nil }
func (f *fakeSink) Migrate(context.Context, int) error { return nil }
func (f *fakeSink) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, w := range f.writes {
		n += len(w)
	}
	return n
}
func (f *fakeSink) write(i int) []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes[i]
}

func ev(id string) Event { return Event{ID: id, Source: SourcePlatform} }

func runPipeline(t *testing.T, p *Pipeline) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = p.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestPipelineBatchesBySize(t *testing.T) {
	s := &fakeSink{}
	p := NewPipeline(s, Options{BatchSize: 2, BatchInterval: time.Hour})
	runPipeline(t, p)
	for i := 0; i < 5; i++ {
		p.Enqueue(ev(fmt.Sprint(i)))
	}
	waitFor(t, func() bool { return s.total() >= 4 })
	if len(s.write(0)) != 2 || len(s.write(1)) != 2 {
		t.Fatalf("writes = %v", s.writes)
	}
}

func TestPipelineFlushesByInterval(t *testing.T) {
	s := &fakeSink{}
	p := NewPipeline(s, Options{BatchSize: 100, BatchInterval: 20 * time.Millisecond})
	runPipeline(t, p)
	p.Enqueue(ev("a"))
	waitFor(t, func() bool { return s.total() == 1 })
}

func TestPipelineRetriesFailedBatchWithoutLoss(t *testing.T) {
	s := &fakeSink{failFirst: 2}
	p := NewPipeline(s, Options{BatchSize: 10, BatchInterval: 10 * time.Millisecond, MaxBackoff: 20 * time.Millisecond})
	runPipeline(t, p)
	p.Enqueue(ev("a"), ev("b"))
	waitFor(t, func() bool { return s.total() == 2 })
	if w := s.write(0); w[0].ID != "a" || w[1].ID != "b" {
		t.Fatalf("order lost: %v", s.writes)
	}
}

func TestEnqueueRejectsWhenFull(t *testing.T) {
	p := NewPipeline(&fakeSink{}, Options{QueueSize: 3}) // 不启动 Run
	if !p.Enqueue(ev("1"), ev("2"), ev("3")) {
		t.Fatal("first three must fit")
	}
	if p.Enqueue(ev("4")) {
		t.Fatal("fourth must be rejected")
	}
	if p.Depth() != 3 {
		t.Fatalf("depth = %d", p.Depth())
	}
}

func TestRunFlushesOnCancel(t *testing.T) {
	s := &fakeSink{}
	p := NewPipeline(s, Options{BatchSize: 100, BatchInterval: time.Hour, FlushTimeout: time.Second})
	cancel := runPipeline(t, p)
	p.Enqueue(ev("a"), ev("b"))
	cancel()
	waitFor(t, func() bool { return s.total() == 2 })
}

// 并发入队：每个调用要么全部入队要么全部拒绝；队列里的条数必须等于所有「返回 true」的调用的事件之和。
func TestEnqueueIsAllOrNothingUnderConcurrency(t *testing.T) {
	for round := 0; round < 50; round++ {
		p := NewPipeline(&fakeSink{}, Options{QueueSize: 10}) // 不启动 Run
		var wg sync.WaitGroup
		var accepted int64
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				evs := make([]Event, 6)
				for i := range evs {
					evs[i] = ev(fmt.Sprintf("%d-%d", g, i))
				}
				if p.Enqueue(evs...) {
					atomic.AddInt64(&accepted, 6)
				}
			}(g)
		}
		wg.Wait()
		if int(accepted) != p.Depth() {
			t.Fatalf("round %d: accepted %d events but queue holds %d (partial enqueue)", round, accepted, p.Depth())
		}
	}
}

type alwaysFailSink struct {
	err   error
	calls int64
}

func (s *alwaysFailSink) Write(context.Context, []Event) error {
	atomic.AddInt64(&s.calls, 1)
	return s.err
}
func (s *alwaysFailSink) Ping(context.Context) error         { return nil }
func (s *alwaysFailSink) Migrate(context.Context, int) error { return nil }

// 存储一直不可用时，取消后 Run 必须在 FlushTimeout 内返回，并把没刷出去的计入 dropped{shutdown}。
func TestRunReturnsWithinFlushTimeoutWhenSinkKeepsFailing(t *testing.T) {
	before := testutil.ToFloat64(DroppedTotal.WithLabelValues("shutdown"))
	s := &alwaysFailSink{err: fmt.Errorf("%w: down", ErrUnavailable)}
	p := NewPipeline(s, Options{BatchSize: 1, BatchInterval: 5 * time.Millisecond, MaxBackoff: 3 * time.Second, FlushTimeout: 200 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = p.Run(ctx); close(done) }()
	p.Enqueue(ev("a"), ev("b"), ev("c"))
	waitFor(t, func() bool { return atomic.LoadInt64(&s.calls) >= 1 })
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if el := time.Since(start); el > 600*time.Millisecond {
		t.Fatalf("Run took %v after cancel, FlushTimeout is 200ms", el)
	}
	if got := testutil.ToFloat64(DroppedTotal.WithLabelValues("shutdown")) - before; got != 3 {
		t.Fatalf("dropped{shutdown} = %v, want 3", got)
	}
}

// 存储明确拒绝某一批（非「不可用」错误）：重试有限次后整批丢弃并计数，管线继续处理后面的批。
func TestPipelineDropsRejectedBatchAfterRetries(t *testing.T) {
	before := testutil.ToFloat64(DroppedTotal.WithLabelValues("rejected"))
	s := &rejectOnceSink{}
	p := NewPipeline(s, Options{BatchSize: 1, BatchInterval: 5 * time.Millisecond, MaxBackoff: 10 * time.Millisecond})
	runPipeline(t, p)
	p.Enqueue(ev("poison"))
	waitFor(t, func() bool { return testutil.ToFloat64(DroppedTotal.WithLabelValues("rejected"))-before == 1 })
	p.Enqueue(ev("good"))
	waitFor(t, func() bool { return s.total() == 1 })
	if s.write(0)[0].ID != "good" {
		t.Fatalf("after dropping the poison batch the next batch must be written, got %v", s.writes)
	}
}

// rejectOnceSink：ID 为 poison 的批返回不可重试错误，其余成功。
type rejectOnceSink struct{ fakeSink }

func (s *rejectOnceSink) Write(ctx context.Context, evs []Event) error {
	if len(evs) > 0 && evs[0].ID == "poison" {
		return errors.New("Code: 27. DB::Exception: Cannot parse input")
	}
	return s.fakeSink.Write(ctx, evs)
}
