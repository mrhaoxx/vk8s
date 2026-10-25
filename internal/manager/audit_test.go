package manager

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/config"
)

// collectSink 把写入的事件收进切片。
type collectSink struct {
	mu     sync.Mutex
	events []audit.Event
}

func (c *collectSink) Write(_ context.Context, evs []audit.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, evs...)
	return nil
}
func (c *collectSink) Ping(context.Context) error         { return nil }
func (c *collectSink) Migrate(context.Context, int) error { return nil }
func (c *collectSink) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}
func (c *collectSink) first() audit.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.events[0]
}

// flakySink Ping 前 failPings 次失败。
type flakySink struct {
	mu                sync.Mutex
	failPings         int
	pings, migrations int
}

func (f *flakySink) Write(context.Context, []audit.Event) error { return nil }
func (f *flakySink) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pings++
	if f.pings <= f.failPings {
		return errors.New("refused")
	}
	return nil
}
func (f *flakySink) Migrate(context.Context, int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.migrations++
	return nil
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

func TestAuditMigratorRetriesUntilSinkIsUp(t *testing.T) {
	s := &flakySink{failPings: 2}
	m := &auditMigrator{sink: s, retentionDays: 7, interval: 5 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if s.pings < 3 || s.migrations != 1 {
		t.Fatalf("pings=%d migrations=%d", s.pings, s.migrations)
	}
	if testutil.ToFloat64(audit.SinkUp) != 1 {
		t.Fatal("sink_up must be 1 after success")
	}
}

func TestAuditMigratorStopsOnContextWithoutSuccess(t *testing.T) {
	s := &flakySink{failPings: 1 << 30}
	m := &auditMigrator{sink: s, retentionDays: 7, interval: 5 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("ctx end must not be an error: %v", err)
	}
	if s.migrations != 0 || testutil.ToFloat64(audit.SinkUp) != 0 {
		t.Fatalf("migrations=%d sinkUp=%v", s.migrations, testutil.ToFloat64(audit.SinkUp))
	}
}

func TestSetupAuditDisabledIsInert(t *testing.T) {
	rt, err := setupAudit(context.Background(), nil, &config.Config{}, nil)
	if err != nil || rt.Enabled || rt.Pipeline != nil || rt.Querier != nil {
		t.Fatalf("rt=%+v err=%v", rt, err)
	}
}
