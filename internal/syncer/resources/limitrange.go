package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/translate"
)

// limitRangeReconciler 把宿主租户 ns 的 LimitRange（operator 按平台配置写入）镜像到每个虚拟 namespace：
// 宿主只在创建 Pod 时补默认 request/limit，虚拟 Pod 若没有同样的默认值，HPA 会因 missing request 失败、
// `describe node` 的已分配资源不准、两侧 qosClass 不同。以虚拟 Namespace 为 reconcile 单位；
// 用户删了镜像对象会被补回，用户自建的同名对象不接管。
type limitRangeReconciler struct {
	c *Context
}

// Reconcile req.Name 为虚拟 namespace 名。
func (r *limitRangeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ns := &corev1.Namespace{}
	if err := r.c.Virtual.Get(ctx, client.ObjectKey{Name: req.Name}, ns); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if ns.Status.Phase == corev1.NamespaceTerminating {
		return ctrl.Result{}, nil
	}

	host := &corev1.LimitRange{}
	herr := r.c.Host.Get(ctx, r.c.HostKey(names.LimitRangeName), host)
	if herr != nil && !isNotFound(herr) {
		return ctrl.Result{}, herr
	}
	key := client.ObjectKey{Namespace: req.Name, Name: names.LimitRangeName}
	v := &corev1.LimitRange{}
	verr := r.c.Virtual.Get(ctx, key, v)
	if verr != nil && !isNotFound(verr) {
		return ctrl.Result{}, verr
	}

	if isNotFound(herr) {
		if verr == nil && translate.IsManaged(v) {
			return ctrl.Result{}, client.IgnoreNotFound(r.c.Virtual.Delete(ctx, v))
		}
		return ctrl.Result{}, nil
	}

	desired := translate.VirtualLimitRange(host, req.Name)
	if isNotFound(verr) {
		if err := r.c.Virtual.Create(ctx, desired); err != nil {
			if isAlreadyExists(err) {
				return ctrl.Result{RequeueAfter: cacheLagRequeue}, nil
			}
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if !translate.IsManaged(v) {
		return ctrl.Result{}, nil
	}
	if translate.ApplyLimitRangeMutations(desired, v) {
		if err := r.c.Virtual.Update(ctx, v); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}
	return ctrl.Result{}, nil
}

// setupLimitRange 注册：虚拟 Namespace、虚拟 LimitRange（映射到其 namespace）、宿主 LimitRange（映射到全部 namespace）。
func setupLimitRange(mgr ctrl.Manager, c *Context) error {
	r := &limitRangeReconciler{c: c}
	byNamespace := handler.TypedEnqueueRequestsFromMapFunc[*corev1.LimitRange, reconcile.Request](
		func(_ context.Context, lr *corev1.LimitRange) []reconcile.Request {
			return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: lr.Namespace}}}
		})
	allNamespaces := handler.TypedEnqueueRequestsFromMapFunc[*corev1.LimitRange, reconcile.Request](
		func(ctx context.Context, _ *corev1.LimitRange) []reconcile.Request {
			list := &corev1.NamespaceList{}
			if err := c.Virtual.List(ctx, list); err != nil {
				c.Log.Error(err, "list virtual namespaces for limitrange sync")
				return nil
			}
			reqs := make([]reconcile.Request, 0, len(list.Items))
			for i := range list.Items {
				reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKey{Name: list.Items[i].Name}})
			}
			return reqs
		})
	ourName := predicate.NewTypedPredicateFuncs[*corev1.LimitRange](func(lr *corev1.LimitRange) bool {
		return lr.Name == names.LimitRangeName
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("limitrange").
		For(&corev1.Namespace{}).
		WatchesRawSource(source.Kind[*corev1.LimitRange](mgr.GetCache(), &corev1.LimitRange{}, byNamespace, ourName)).
		WatchesRawSource(source.Kind[*corev1.LimitRange](c.HostCache, &corev1.LimitRange{}, allNamespaces, ourName)).
		Complete(r)
}
