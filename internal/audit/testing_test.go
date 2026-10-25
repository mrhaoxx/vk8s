package audit

import (
	"context"
	"sync"
)

// collectSink 把写入的事件收进切片，供本包测试共用。
type collectSink struct {
	mu     sync.Mutex
	events []Event
}

func (c *collectSink) Write(_ context.Context, evs []Event) error {
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
func (c *collectSink) first() Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.events[0]
}
func (c *collectSink) at(i int) Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.events[i]
}
