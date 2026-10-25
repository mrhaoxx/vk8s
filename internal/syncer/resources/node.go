package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"vk8s.io/vk8s/internal/syncer/placement"
	"vk8s.io/vk8s/internal/syncer/translate"
)

// nodeReconciler 宿 → 虚 Node 镜像，仅 nodePlacement 允许的节点：
// 复制 labels/taints/status（真实地址、容量、conditions、nodeInfo），kubeletEndpoint.Port=10250；
// 不再允许的节点删除；用户对虚拟 Node 的改动被覆盖。
type nodeReconciler struct {
	c *Context
	// placementEvents nodePlacement 变化时把全部宿主节点重新入队
	placementEvents chan event.TypedGenericEvent[*corev1.Node]
}

// Reconcile 实现 reconcile.Reconciler；请求键是节点名（两侧相同）。
func (r *nodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	host := &corev1.Node{}
	herr := r.c.Host.Get(ctx, client.ObjectKey{Name: req.Name}, host)
	if herr != nil && !isNotFound(herr) {
		return ctrl.Result{}, herr
	}
	v := &corev1.Node{}
	verr := r.c.Virtual.Get(ctx, client.ObjectKey{Name: req.Name}, v)
	if verr != nil && !isNotFound(verr) {
		return ctrl.Result{}, verr
	}

	allowed := herr == nil && placement.Allows(r.c.Placement.Get(), host)
	if !allowed {
		if verr == nil && translate.IsManaged(v) {
			if err := r.c.Virtual.Delete(ctx, v); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
		}
		// 节点不再可用：其上的虚拟 Pod 一并驱逐，由各自的控制器按新约束重建（见 evict.go）
		return ctrl.Result{}, r.c.evictPodsOnNode(ctx, req.Name)
	}

	desired := translate.VirtualNode(host)
	status := translate.VirtualNodeStatus(host)
	if isNotFound(verr) {
		if err := r.c.Virtual.Create(ctx, desired); err != nil {
			if isAlreadyExists(err) {
				return ctrl.Result{RequeueAfter: cacheLagRequeue}, nil
			}
			return ctrl.Result{}, err
		}
		orig := desired.DeepCopy()
		desired.Status = status
		return ctrl.Result{}, r.c.Virtual.Status().Patch(ctx, desired, client.MergeFrom(orig))
	}

	if translate.ApplyNodeMutations(desired, v) {
		if err := r.c.Virtual.Update(ctx, v); err != nil {
			return ctrl.Result{}, err
		}
	}
	orig := v.DeepCopy()
	if translate.NodeStatusChanged(v, status) {
		if err := r.c.Virtual.Status().Patch(ctx, v, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}
	return ctrl.Result{}, nil
}

// onPlacementChange nodePlacement 变化：把全部宿主节点推进 channel 重新评估。
func (r *nodeReconciler) onPlacementChange(_ placement.Effective) {
	go func() {
		list := &corev1.NodeList{}
		if err := r.c.Host.List(context.Background(), list); err != nil {
			r.c.Log.Error(err, "list host nodes after placement change")
			return
		}
		for i := range list.Items {
			r.placementEvents <- event.TypedGenericEvent[*corev1.Node]{Object: &list.Items[i]}
		}
	}()
}

// setupNode 注册 Node controller：虚拟 Node（覆盖用户改动）+ 宿主 Node + placement 变化事件。
func setupNode(mgr ctrl.Manager, c *Context) error {
	r := &nodeReconciler{
		c:               c,
		placementEvents: make(chan event.TypedGenericEvent[*corev1.Node], 1024),
	}
	c.Placement.OnChange(r.onPlacementChange)
	return ctrl.NewControllerManagedBy(mgr).
		Named("node").
		For(&corev1.Node{}).
		WatchesRawSource(hostSourceSameName[*corev1.Node](c.HostCache, &corev1.Node{})).
		WatchesRawSource(source.Channel[*corev1.Node](r.placementEvents, &handler.TypedEnqueueRequestForObject[*corev1.Node]{})).
		Complete(r)
}
