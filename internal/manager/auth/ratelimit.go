package auth

import (
	"sync"
	"time"
)

// 限速默认值：同一用户名或同一来源 IP 连续失败 5 次锁 5 分钟。
const (
	DefaultMaxFailures = 5
	DefaultLockout     = 5 * time.Minute
)

type limitEntry struct {
	failures    int
	lockedUntil time.Time
	lastSeen    time.Time
}

// RateLimiter 内存级登录失败限速，按 key（用户名、IP）分别计数。
type RateLimiter struct {
	mu          sync.Mutex
	entries     map[string]*limitEntry
	maxFailures int
	lockout     time.Duration
	now         func() time.Time
	lastSweep   time.Time
}

// NewRateLimiter 参数为 0 时取默认值。
func NewRateLimiter(maxFailures int, lockout time.Duration) *RateLimiter {
	if maxFailures <= 0 {
		maxFailures = DefaultMaxFailures
	}
	if lockout <= 0 {
		lockout = DefaultLockout
	}
	return &RateLimiter{
		entries:     map[string]*limitEntry{},
		maxFailures: maxFailures,
		lockout:     lockout,
		now:         time.Now,
	}
}

// Allowed 任一 key 处于锁定期则拒绝。
func (l *RateLimiter) Allowed(keys ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	for _, k := range keys {
		if e, ok := l.entries[k]; ok && now.Before(e.lockedUntil) {
			return false
		}
	}
	return true
}

// Fail 记录一次失败；达到阈值即锁定。
func (l *RateLimiter) Fail(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for _, k := range keys {
		e, ok := l.entries[k]
		if !ok {
			e = &limitEntry{}
			l.entries[k] = e
		}
		// 锁定期已过则重新计数。
		if !e.lockedUntil.IsZero() && now.After(e.lockedUntil) {
			e.failures = 0
			e.lockedUntil = time.Time{}
		}
		e.failures++
		e.lastSeen = now
		if e.failures >= l.maxFailures {
			e.lockedUntil = now.Add(l.lockout)
		}
	}
}

// Reset 登录成功后清零。
func (l *RateLimiter) Reset(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range keys {
		delete(l.entries, k)
	}
}

// sweep 定期清理长时间无活动的条目，防止 map 无限增长。
func (l *RateLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	for k, e := range l.entries {
		if now.After(e.lockedUntil) && now.Sub(e.lastSeen) > 2*l.lockout {
			delete(l.entries, k)
		}
	}
}
