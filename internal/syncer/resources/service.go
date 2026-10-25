package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/internal/syncer/translate"
)

// serviceReconciler Service 虚 → 宿；宿 → 虚 status（LB IP）。
// ClusterIP 由 webhook 在 CREATE 时对齐（见 webhook 包）；这里负责存量对象、可变字段跟随、
// 以及「两侧 IP 不一致则按宿主重建虚拟 Service」的兜底。
type serviceReconciler struct {
	c *Context
}

// Reconcile 实现 reconcile.Reconciler。
func (r *serviceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if translate.IsSystemService(req.Namespace, req.Name) {
		return ctrl.Result{}, nil
	}
	hostName := translate.HostName(req.Name, req.Namespace)

	v := &corev1.Service{}
	if err := r.c.Virtual.Get(ctx, req.NamespacedName, v); err != nil {
		if !isNotFound(err) {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.deleteHost(ctx, hostName)
	}
	if v.DeletionTimestamp != nil {
		return ctrl.Result{}, r.deleteHost(ctx, hostName)
	}

	desired := translate.TranslateService(v, r.c.HostNamespace)
	actual := &corev1.Service{}
	err := r.c.Host.Get(ctx, r.c.HostKey(hostName), actual)
	switch {
	case isNotFound(err):
		// webhook 未生效期间创建的 Service，或宿主对象被人删了：以虚拟侧 IP 在宿主重建；IP 被占时宿主拒绝 → Event + 退避
		if err := r.c.Host.Create(ctx, desired); err != nil {
			return r.c.handleSyncError("service", v, err)
		}
		actual = desired
	case err != nil:
		return ctrl.Result{}, err
	default:
		if !translate.IsManagedFor(actual, v) {
			r.c.Recorder.Eventf(v, corev1.EventTypeWarning, EventHostConflict,
				"host service %s/%s exists but is not managed for this service", r.c.HostNamespace, hostName)
			return ctrl.Result{}, nil
		}
		if translate.ServiceClusterIPMismatch(v, actual) {
			return r.realign(ctx, v, actual)
		}
		if translate.ApplyServiceMutations(desired, actual) {
			if err := r.c.Host.Update(ctx, actual); err != nil {
				return r.c.handleSyncError("service", v, err)
			}
		}
	}

	orig := v.DeepCopy()
	if translate.ServiceStatusChanged(v, actual) {
		if err := r.c.Virtual.Status().Patch(ctx, v, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}
	return ctrl.Result{}, nil
}

// realign 两侧 ClusterIP 不一致（webhook 被删期间创建的 Service）：删除并按宿主 IP 重建虚拟 Service。
func (r *serviceReconciler) realign(ctx context.Context, v, h *corev1.Service) (ctrl.Result, error) {
	r.c.Recorder.Eventf(v, corev1.EventTypeWarning, EventClusterIPRealigned,
		"clusterIP %s differs from host clusterIP %s; recreating the service with the host IP", v.Spec.ClusterIP, h.Spec.ClusterIP)
	recreated := translate.RealignedVirtualService(v, h)
	if err := r.c.Virtual.Delete(ctx, v); client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}
	if err := r.c.Virtual.Create(ctx, recreated); err != nil {
		return r.c.handleSyncError("service", v, err)
	}
	return ctrl.Result{}, nil
}

func (r *serviceReconciler) deleteHost(ctx context.Context, hostName string) error {
	obj := &corev1.Service{}
	if err := r.c.Host.Get(ctx, r.c.HostKey(hostName), obj); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !translate.IsManaged(obj) {
		return nil
	}
	return client.IgnoreNotFound(r.c.Host.Delete(ctx, obj))
}

// setupService 注册 Service controller。
func setupService(mgr ctrl.Manager, c *Context) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("service").
		For(&corev1.Service{}).
		WatchesRawSource(hostSource[*corev1.Service](c.HostCache, &corev1.Service{})).
		Complete(&serviceReconciler{c: c})
}
