package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"vk8s.io/vk8s/internal/syncer/translate"
)

// eventReconciler 宿 → 虚 Event：只处理 involvedObject 是已同步对象的事件，involvedObject 翻回虚拟对象。
// 请求键是宿主 Event 的 ns/name。
type eventReconciler struct {
	c *Context
}

// Reconcile 实现 reconcile.Reconciler。
func (r *eventReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	hev := &corev1.Event{}
	if err := r.c.Host.Get(ctx, req.NamespacedName, hev); err != nil {
		// 宿主事件过期消失不影响虚拟侧（虚拟 apiserver 自己按 TTL 清理）
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if hev.InvolvedObject.Namespace != r.c.HostNamespace {
		return ctrl.Result{}, nil
	}
	vkey, vuid, ok := r.resolveInvolved(ctx, hev.InvolvedObject)
	if !ok {
		return ctrl.Result{}, nil
	}

	desired := translate.VirtualEvent(hev, vkey, vuid)
	actual := &corev1.Event{}
	err := r.c.Virtual.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, actual)
	switch {
	case isNotFound(err):
		if err := r.c.Virtual.Create(ctx, desired); err != nil && !isAlreadyExists(err) {
			// 虚拟 ns 正在删除等情况：静默放弃，事件不值得退避重试
			r.c.Log.V(1).Info("create virtual event failed", "event", desired.Name, "error", err.Error())
		}
	case err != nil:
		return ctrl.Result{}, err
	default:
		if translate.ApplyEventMutations(desired, actual) {
			if err := r.c.Virtual.Update(ctx, actual); err != nil {
				return ctrl.Result{}, client.IgnoreNotFound(err)
			}
		}
	}
	return ctrl.Result{}, nil
}

// resolveInvolved 按 kind 在宿主 cache 里找到被引用的对象（只有 managed 对象在 cache 中），反查虚拟键与 UID。
func (r *eventReconciler) resolveInvolved(ctx context.Context, ref corev1.ObjectReference) (types.NamespacedName, types.UID, bool) {
	var obj client.Object
	switch ref.Kind {
	case "Pod":
		obj = &corev1.Pod{}
	case "PersistentVolumeClaim":
		obj = &corev1.PersistentVolumeClaim{}
	case "Service":
		obj = &corev1.Service{}
	case "Ingress":
		obj = &networkingv1.Ingress{}
	case "ConfigMap":
		obj = &corev1.ConfigMap{}
	case "Secret":
		obj = &corev1.Secret{}
	case "Endpoints":
		obj = &corev1.Endpoints{}
	case "EndpointSlice":
		obj = &discoveryv1.EndpointSlice{}
	default:
		return types.NamespacedName{}, "", false
	}
	if err := r.c.Host.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, obj); err != nil {
		return types.NamespacedName{}, "", false
	}
	if ref.UID != "" && obj.GetUID() != ref.UID {
		// 同名对象已重建，旧事件不再对应
		return types.NamespacedName{}, "", false
	}
	if s, ok := obj.(*corev1.Secret); ok && IsSATokenSecret(s) {
		return types.NamespacedName{}, "", false
	}
	key, ok := translate.VirtualKey(obj)
	if !ok {
		return types.NamespacedName{}, "", false
	}
	return key, translate.VirtualUID(obj), true
}

// setupEvent 注册 Event controller：只 watch 宿主 Event（cache 对 Event 不按 managed 过滤）。
func setupEvent(mgr ctrl.Manager, c *Context) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("event").
		WatchesRawSource(source.Kind[*corev1.Event](c.HostCache, &corev1.Event{}, &handler.TypedEnqueueRequestForObject[*corev1.Event]{})).
		Complete(&eventReconciler{c: c})
}
