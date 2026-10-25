package gateway

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/names"
)

// 拒绝原因，作为指标 label；集合必须是有限的，不含用户输入。
const (
	RejectNoSNI          = "no_sni"
	RejectBadRequest     = "bad_client_hello"
	RejectWrongDomain    = "wrong_domain"
	RejectUnknownCluster = "unknown_vcluster"
	RejectNotRunning     = "not_running"
	RejectDialFailed     = "dial_failed"
	RejectHandshakeTimeo = "handshake_timeout"
	RejectShuttingDown   = "shutting_down"
)

// route 路由表的一项。
type route struct {
	backend string
	phase   v1alpha1.VirtualClusterPhase
}

// Router 由 VirtualCluster informer 驱动的路由表。
type Router struct {
	apiDomain string
	suffix    string // "." + apiDomain
	metrics   *Metrics

	cache    cache.Cache
	informer cache.Informer

	mu     sync.RWMutex
	routes map[string]route // key 为虚拟集群名；只记录已知集群，Running 的才可路由
}

// NewRouter 创建路由表并注册 informer 事件处理。实际 watch 在 Run 中开始。
func NewRouter(cfg *rest.Config, apiDomain string, metrics *Metrics) (*Router, error) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("router: add scheme: %w", err)
	}
	c, err := cache.New(cfg, cache.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("router: create cache: %w", err)
	}
	r := &Router{
		apiDomain: apiDomain,
		suffix:    "." + apiDomain,
		metrics:   metrics,
		cache:     c,
		routes:    map[string]route{},
	}
	// GetInformer 在 cache 未启动时只注册 informer，不阻塞
	inf, err := c.GetInformer(context.Background(), &v1alpha1.VirtualCluster{})
	if err != nil {
		return nil, fmt.Errorf("router: get informer: %w", err)
	}
	if _, err := inf.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { r.upsert(obj) },
		UpdateFunc: func(_, obj interface{}) { r.upsert(obj) },
		DeleteFunc: func(obj interface{}) { r.delete(obj) },
	}); err != nil {
		return nil, fmt.Errorf("router: add event handler: %w", err)
	}
	r.informer = inf
	return r, nil
}

// Run 启动 informer 并阻塞到 ctx 取消。
func (r *Router) Run(ctx context.Context) error {
	klog.InfoS("starting VirtualCluster informer")
	go func() {
		if r.cache.WaitForCacheSync(ctx) {
			r.mu.RLock()
			n := len(r.routes)
			r.mu.RUnlock()
			klog.InfoS("VirtualCluster informer synced", "known", n, "running", r.runningCount())
		}
	}()
	return r.cache.Start(ctx)
}

// HasSynced informer 是否完成首次 list。
func (r *Router) HasSynced() bool {
	return r.informer != nil && r.informer.HasSynced()
}

// Resolve 把 SNI 解析为后端地址。失败时返回拒绝原因。
func (r *Router) Resolve(serverName string) (backend string, reason string, ok bool) {
	if serverName == "" {
		return "", RejectNoSNI, false
	}
	if !strings.HasSuffix(serverName, r.suffix) {
		return "", RejectWrongDomain, false
	}
	vc := strings.TrimSuffix(serverName, r.suffix)
	// 只允许一个 label：<vc>.<apiDomain>
	if vc == "" || strings.Contains(vc, ".") {
		return "", RejectWrongDomain, false
	}
	r.mu.RLock()
	rt, found := r.routes[vc]
	r.mu.RUnlock()
	if !found {
		return "", RejectUnknownCluster, false
	}
	if rt.phase != v1alpha1.PhaseRunning {
		return "", RejectNotRunning, false
	}
	return rt.backend, "", true
}

// backendFor 控制平面 Service 的集群内地址。
func backendFor(vc string) string {
	return fmt.Sprintf("%s:%d", names.ControlPlaneServiceDNS(vc), names.ServicePort)
}

func (r *Router) upsert(obj interface{}) {
	vc, ok := obj.(*v1alpha1.VirtualCluster)
	if !ok {
		klog.V(2).InfoS("router: unexpected object type", "type", fmt.Sprintf("%T", obj))
		return
	}
	// 正在删除的集群立即摘除，避免把连接送到即将消失的 Service
	phase := vc.Status.Phase
	if vc.DeletionTimestamp != nil {
		phase = v1alpha1.PhaseDeleting
	}
	r.mu.Lock()
	prev, existed := r.routes[vc.Name]
	r.routes[vc.Name] = route{backend: backendFor(vc.Name), phase: phase}
	r.mu.Unlock()
	if !existed || prev.phase != phase {
		klog.InfoS("route updated", "vcluster", vc.Name, "phase", phase, "routable", phase == v1alpha1.PhaseRunning)
	}
	r.metrics.SetRoutes(r.runningCount())
}

func (r *Router) delete(obj interface{}) {
	if tomb, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
		obj = tomb.Obj
	}
	vc, ok := obj.(*v1alpha1.VirtualCluster)
	if !ok {
		return
	}
	r.mu.Lock()
	delete(r.routes, vc.Name)
	r.mu.Unlock()
	klog.InfoS("route removed", "vcluster", vc.Name)
	r.metrics.SetRoutes(r.runningCount())
}

func (r *Router) runningCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, rt := range r.routes {
		if rt.phase == v1alpha1.PhaseRunning {
			n++
		}
	}
	return n
}
