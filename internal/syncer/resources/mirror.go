package resources

import (
	"context"

	networkingv1 "k8s.io/api/networking/v1"
	nodev1 "k8s.io/api/node/v1"
	storagev1 "k8s.io/api/storage/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/internal/syncer/translate"
)

// setupRuntimeClass RuntimeClass 只读镜像。
func setupRuntimeClass(mgr ctrl.Manager, c *Context) error {
	u := &upSync[*nodev1.RuntimeClass]{
		c:         c,
		resource:  "runtimeclass",
		newObj:    func() *nodev1.RuntimeClass { return &nodev1.RuntimeClass{} },
		translate: translate.VirtualRuntimeClass,
		mutate:    translate.ApplyRuntimeClassMutations,
	}
	return u.setup(mgr)
}

// upSync 「宿 → 虚」只读镜像的通用 reconciler（StorageClass / IngressClass）：两侧同名，
// 宿主对象消失时删除由 syncer 创建的虚拟对象；用户对镜像对象的改动被覆盖；用户自建的同类对象不碰。
type upSync[T client.Object] struct {
	c         *Context
	resource  string
	newObj    func() T
	translate func(host T) T
	// mutate 返回 (changed, needRecreate)。
	mutate func(desired, actual T) (bool, bool)
}

// Reconcile 实现 reconcile.Reconciler。
func (u *upSync[T]) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	host := u.newObj()
	herr := u.c.Host.Get(ctx, client.ObjectKey{Name: req.Name}, host)
	if herr != nil && !isNotFound(herr) {
		return ctrl.Result{}, herr
	}
	v := u.newObj()
	verr := u.c.Virtual.Get(ctx, client.ObjectKey{Name: req.Name}, v)
	if verr != nil && !isNotFound(verr) {
		return ctrl.Result{}, verr
	}

	if isNotFound(herr) {
		if verr == nil && translate.IsManaged(v) {
			return ctrl.Result{}, client.IgnoreNotFound(u.c.Virtual.Delete(ctx, v))
		}
		return ctrl.Result{}, nil
	}

	desired := u.translate(host)
	if isNotFound(verr) {
		if err := u.c.Virtual.Create(ctx, desired); err != nil {
			if isAlreadyExists(err) {
				return ctrl.Result{RequeueAfter: cacheLagRequeue}, nil
			}
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if !translate.IsManaged(v) {
		// 用户自己建的同名对象：不接管，宿主对象在虚拟侧不可见
		return ctrl.Result{}, nil
	}
	changed, recreate := u.mutate(desired, v)
	if recreate {
		if err := u.c.Virtual.Delete(ctx, v); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: cacheLagRequeue}, nil
	}
	if changed {
		if err := u.c.Virtual.Update(ctx, v); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}
	return ctrl.Result{}, nil
}

func (u *upSync[T]) setup(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(u.resource).
		For(u.newObj()).
		WatchesRawSource(hostSourceSameName[T](u.c.HostCache, u.newObj())).
		Complete(u)
}

// setupStorageClass StorageClass 只读镜像。
func setupStorageClass(mgr ctrl.Manager, c *Context) error {
	u := &upSync[*storagev1.StorageClass]{
		c:         c,
		resource:  "storageclass",
		newObj:    func() *storagev1.StorageClass { return &storagev1.StorageClass{} },
		translate: translate.VirtualStorageClass,
		mutate:    translate.ApplyStorageClassMutations,
	}
	return u.setup(mgr)
}

// setupIngressClass IngressClass 只读镜像。
func setupIngressClass(mgr ctrl.Manager, c *Context) error {
	u := &upSync[*networkingv1.IngressClass]{
		c:         c,
		resource:  "ingressclass",
		newObj:    func() *networkingv1.IngressClass { return &networkingv1.IngressClass{} },
		translate: translate.VirtualIngressClass,
		mutate: func(desired, actual *networkingv1.IngressClass) (bool, bool) {
			return translate.ApplyIngressClassMutations(desired, actual), false
		},
	}
	return u.setup(mgr)
}
