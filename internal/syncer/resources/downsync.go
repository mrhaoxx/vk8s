package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/internal/syncer/translate"
)

// downSync 「虚 → 宿」同步的通用 reconciler，适用于 ConfigMap / Secret / PVC / Ingress / Endpoints / EndpointSlice
// 这类「宿主对象 = 翻译(虚拟对象)」的资源。Pod 与 Service 因生命周期特殊单独实现。
type downSync[T client.Object] struct {
	c        *Context
	resource string
	newObj   func() T
	// skip 返回 true 表示该虚拟对象不下沉（并删除可能存在的宿主对象）。可为 nil。
	skip func(ctx context.Context, v T) (bool, error)
	// translate 产生期望的宿主对象；可返回 RejectError / NotReadyError。
	translate func(ctx context.Context, v T) (T, error)
	// mutate 把期望值的可变字段应用到现有宿主对象上，返回是否有改动。
	mutate func(desired, actual T) bool
	// needRecreate 不可变字段变化（如 Secret type）时返回 true，宿主对象删除后重建。可为 nil。
	needRecreate func(desired, actual T) bool
	// writeback 把宿主 status 回写进虚拟对象，返回是否有改动；可为 nil。
	writeback func(v, actual T) bool
}

// Reconcile 实现 reconcile.Reconciler。
func (d *downSync[T]) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	v := d.newObj()
	if err := d.c.Virtual.Get(ctx, req.NamespacedName, v); err != nil {
		if !isNotFound(err) {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, d.deleteHost(ctx, req.NamespacedName)
	}
	if !v.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, d.deleteHost(ctx, req.NamespacedName)
	}
	if d.skip != nil {
		skip, err := d.skip(ctx, v)
		if err != nil {
			return ctrl.Result{}, err
		}
		if skip {
			return ctrl.Result{}, d.deleteHost(ctx, req.NamespacedName)
		}
	}

	desired, err := d.translate(ctx, v)
	if err != nil {
		return d.c.handleSyncError(d.resource, v, err)
	}

	actual := d.newObj()
	err = d.c.Host.Get(ctx, d.c.HostKey(desired.GetName()), actual)
	switch {
	case isNotFound(err):
		if err := d.c.Host.Create(ctx, desired); err != nil {
			return d.c.handleSyncError(d.resource, v, err)
		}
		actual = desired
	case err != nil:
		return ctrl.Result{}, err
	default:
		if !translate.IsManagedFor(actual, v) {
			d.c.Recorder.Eventf(v, corev1.EventTypeWarning, EventHostConflict,
				"host object %s/%s exists but is not managed for this object", d.c.HostNamespace, desired.GetName())
			return ctrl.Result{}, nil
		}
		if d.needRecreate != nil && d.needRecreate(desired, actual) {
			if err := d.c.Host.Delete(ctx, actual); client.IgnoreNotFound(err) != nil {
				return d.c.handleSyncError(d.resource, v, err)
			}
			return ctrl.Result{RequeueAfter: cacheLagRequeue}, nil
		}
		if d.mutate(desired, actual) {
			if err := d.c.Host.Update(ctx, actual); err != nil {
				return d.c.handleSyncError(d.resource, v, err)
			}
		}
	}

	if d.writeback != nil {
		orig := v.DeepCopyObject().(T)
		if d.writeback(v, actual) {
			if err := d.c.Virtual.Status().Patch(ctx, v, client.MergeFrom(orig)); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
		}
	}
	return ctrl.Result{}, nil
}

// deleteHost 删除虚拟对象对应的宿主对象。cache 只含 managed 对象，所以 Get 到的一定是我们建的。
func (d *downSync[T]) deleteHost(ctx context.Context, vkey types.NamespacedName) error {
	obj := d.newObj()
	if err := d.c.Host.Get(ctx, d.c.HostKey(translate.HostName(vkey.Name, vkey.Namespace)), obj); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !translate.IsManaged(obj) {
		return nil
	}
	return client.IgnoreNotFound(d.c.Host.Delete(ctx, obj))
}

// setup 注册 controller：For 虚拟对象 + 宿主对象 raw source。
func (d *downSync[T]) setup(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(d.resource).
		For(d.newObj()).
		WatchesRawSource(hostSource[T](d.c.HostCache, d.newObj())).
		Complete(d)
}
