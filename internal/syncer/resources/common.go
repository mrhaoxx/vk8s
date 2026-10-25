// Package resources 实现每种资源的 controller-runtime reconciler。
//
// 统一模式：watch 虚拟对象 → 创建/更新宿主对象；watch 宿主对象（label `vk8s.io/managed=true`）
// → 入队对应的虚拟对象并回写 status。宿主对象靠 label 反查虚拟对象，没有本地状态库。
// 虚拟对象是 spec 的真相源，宿主对象是 status 的真相源。
package resources

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/placement"
	"vk8s.io/vk8s/internal/syncer/translate"
)

const (
	// EventSyncRejected 翻译阶段拒绝，对象不变则不再重试。
	EventSyncRejected = "SyncRejected"
	// EventSyncError 宿主拒绝或其他错误，带宿主原话，指数退避重试。
	EventSyncError = "SyncError"
	// EventHostConflict 宿主 ns 内已有同名但非 syncer 管理的对象。
	EventHostConflict = "HostNameConflict"
	// EventClusterIPRealigned Service 两侧 ClusterIP 不一致，按宿主重建了虚拟对象。
	EventClusterIPRealigned = "ClusterIPRealigned"
	// EventHostPodGone 宿主 Pod 被外力删除，虚拟 Pod 随之删除以便控制器重建。
	EventHostPodGone = "HostPodGone"
	// EventEvictedByNodePlacement Pod 所在节点不再被 nodePlacement 允许，虚拟 Pod 被删除以便控制器按新约束重建。
	EventEvictedByNodePlacement = "EvictedByNodePlacement"

	// ControllerName Event source / field manager。
	ControllerName = "vk8s-syncer"

	// notReadyRequeue 依赖未就绪时的重试间隔。
	notReadyRequeue = 5 * time.Second
	// cacheLagRequeue cache 滞后导致的 AlreadyExists/Conflict 的重试间隔。
	cacheLagRequeue = time.Second
)

// Context 所有 reconciler 共享的依赖。
type Context struct {
	Log logr.Logger

	VCName        string
	HostNamespace string
	AppsDomain    string
	AppsTLSSecret string
	// PlatformCA 写进 SA token Secret 的 ca.crt，Pod 内 client 用它校验虚拟 apiserver。
	PlatformCA []byte
	// KubernetesServiceIP 宿主 Service vc-<name> 的 ClusterIP，即虚拟 default/kubernetes 对齐后的 IP。
	KubernetesServiceIP string

	// Virtual 虚拟集群 client（manager 的 cache 读 / 直写）。
	Virtual client.Client
	// VirtualClientset 用于 TokenRequest 与 pods/binding。
	VirtualClientset kubernetes.Interface
	// Host 宿主集群 client，cache 限定在宿主 ns 且只含 managed=true 的对象（Node/Event 等除外）。
	Host client.Client
	// HostCache 宿主 cache，用于构造 watch source。
	HostCache cache.Cache
	// Recorder 往虚拟集群写 Event。
	Recorder record.EventRecorder
	// Placement 当前 nodePlacement。
	Placement *placement.Holder
}

// HostKey 宿主 ns 内的对象键。
func (c *Context) HostKey(name string) client.ObjectKey {
	return client.ObjectKey{Namespace: c.HostNamespace, Name: name}
}

// nodeAllowedNow 节点是否被当前 placement 允许：宿主节点不存在视为不允许；读取出错原样返回，
// 调用方据此决定是重试还是把「不确定」当成「不允许」。
func (c *Context) nodeAllowedNow(ctx context.Context, name string) (bool, error) {
	node := &corev1.Node{}
	if err := c.Host.Get(ctx, client.ObjectKey{Name: name}, node); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return placement.Allows(c.Placement.Get(), node), nil
}

// nodeAllowed 翻译器用的判定闭包：读取出错时按不允许处理（只影响创建，拒绝比放行安全）。
func (c *Context) nodeAllowed(ctx context.Context) func(string) bool {
	return func(name string) bool {
		allowed, err := c.nodeAllowedNow(ctx, name)
		return err == nil && allowed
	}
}

// podEnv 组装 Pod 翻译上下文；kube-dns 尚未分配 ClusterIP 时 DNSServiceIP 为空，由翻译器决定是否需要。
func (c *Context) podEnv(ctx context.Context) translate.PodEnv {
	env := translate.PodEnv{
		HostNamespace:       c.HostNamespace,
		Placement:           c.Placement.Get(),
		NodeAllowed:         c.nodeAllowed(ctx),
		KubernetesServiceIP: c.KubernetesServiceIP,
	}
	dns := &corev1.Service{}
	key := types.NamespacedName{Namespace: names.VirtualSystemNamespace, Name: names.CoreDNSServiceName}
	if err := c.Virtual.Get(ctx, key, dns); err == nil && dns.Spec.ClusterIP != corev1.ClusterIPNone {
		env.DNSServiceIP = dns.Spec.ClusterIP
	}
	return env
}

// handleSyncError 统一错误分类：
//   - RejectError → 虚拟对象 Event SyncRejected，不重试；
//   - NotReadyError → 静默短暂重试；
//   - AlreadyExists / Conflict → cache 滞后，短暂重试，不记 Event；
//   - 其他（宿主 API 错误）→ Event SyncError 带原文，返回 err 让 controller-runtime 指数退避。
func (c *Context) handleSyncError(resource string, vobj client.Object, err error) (ctrl.Result, error) {
	switch {
	case err == nil:
		return ctrl.Result{}, nil
	case translate.IsReject(err):
		c.Recorder.Event(vobj, corev1.EventTypeWarning, EventSyncRejected, err.Error())
		TranslateRejected.WithLabelValues(resource).Inc()
		return ctrl.Result{}, nil
	case translate.IsNotReady(err):
		return ctrl.Result{RequeueAfter: notReadyRequeue}, nil
	case apierrors.IsAlreadyExists(err), apierrors.IsConflict(err):
		return ctrl.Result{RequeueAfter: cacheLagRequeue}, nil
	default:
		c.Recorder.Event(vobj, corev1.EventTypeWarning, EventSyncError, err.Error())
		SyncErrors.WithLabelValues(resource).Inc()
		return ctrl.Result{}, err
	}
}

// managedPredicate 只放行带 managed 标签的宿主对象（cache 已按该标签过滤，这里是双保险）。
func managedPredicate[T client.Object]() predicate.TypedPredicate[T] {
	return predicate.NewTypedPredicateFuncs[T](func(o T) bool { return translate.IsManaged(o) })
}

// hostSource 宿主对象 watch：按 label 反查并入队对应虚拟对象。
func hostSource[T client.Object](c cache.Cache, obj T) source.Source {
	return source.Kind[T](c, obj,
		handler.TypedEnqueueRequestsFromMapFunc[T, reconcile.Request](func(_ context.Context, o T) []reconcile.Request {
			key, ok := translate.VirtualKey(o)
			if !ok {
				return nil
			}
			return []reconcile.Request{{NamespacedName: key}}
		}),
		managedPredicate[T]())
}

// hostSourceSameName 宿主集群级对象（Node/StorageClass/IngressClass）watch：两侧同名，直接入队。
func hostSourceSameName[T client.Object](c cache.Cache, obj T) source.Source {
	return source.Kind[T](c, obj, &handler.TypedEnqueueRequestForObject[T]{})
}

// isNotFound 忽略 NotFound 的辅助。
func isNotFound(err error) bool { return apierrors.IsNotFound(err) }
