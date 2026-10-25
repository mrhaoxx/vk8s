// Package placement 持有当前 VirtualCluster 生效的节点约束（静态放置 + 租约），并提供节点过滤判定。
// syncer 每 30s 从宿主读取一次 VirtualCluster 的 spec.nodePlacement 与 status.leasedNodes 写入 Holder，
// 翻译器与 Node 镜像控制器从这里读取，避免各处各自查询。
package placement

import (
	"sync"

	"k8s.io/apimachinery/pkg/api/equality"
)

// Holder 线程安全的 Effective 容器。
type Holder struct {
	mu        sync.RWMutex
	placement Effective
	loaded    bool
	listeners []func(Effective)
}

// NewHolder 构造空 Holder。
func NewHolder() *Holder { return &Holder{} }

// Get 返回当前 placement 的副本。
func (h *Holder) Get() Effective {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.placement.clone()
}

// Loaded 是否至少成功加载过一次。
func (h *Holder) Loaded() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.loaded
}

// Set 更新 placement；有变化时通知订阅者并返回 true。
func (h *Holder) Set(p Effective) bool {
	h.mu.Lock()
	changed := !h.loaded || !equality.Semantic.DeepEqual(h.placement, p)
	h.placement = p.clone()
	h.loaded = true
	listeners := append([]func(Effective){}, h.listeners...)
	h.mu.Unlock()
	if changed {
		for _, fn := range listeners {
			fn(p)
		}
	}
	return changed
}

// OnChange 注册变更回调（在 Set 的调用 goroutine 中同步执行，回调应快速返回）。
func (h *Holder) OnChange(fn func(Effective)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listeners = append(h.listeners, fn)
}
