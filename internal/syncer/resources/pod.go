package resources

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/translate"
)

// podReconciler Pod 同步：
//   - 虚 → 宿 spec（翻译后创建；可变字段跟随）；
//   - 宿 → 虚 status 整体回写，调度结果经 pods/binding 写入 spec.nodeName；
//   - 虚拟 Pod 删除 → 同 gracePeriod 删宿主 Pod → 宿主消失后以 gracePeriod=0 收尾虚拟 Pod；
//   - 宿主 Pod 被外力删除 → 删虚拟 Pod，让虚拟集群里的控制器重建；
//   - SA token 的签发与续签（见 satoken.go）。
type podReconciler struct {
	c *Context
}

// Reconcile 实现 reconcile.Reconciler。
func (r *podReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	hostName := translate.HostName(req.Name, req.Namespace)

	vpod := &corev1.Pod{}
	verr := r.c.Virtual.Get(ctx, req.NamespacedName, vpod)
	if verr != nil && !isNotFound(verr) {
		return ctrl.Result{}, verr
	}
	hpod := &corev1.Pod{}
	herr := r.c.Host.Get(ctx, r.c.HostKey(hostName), hpod)
	if herr != nil && !isNotFound(herr) {
		return ctrl.Result{}, herr
	}
	hostExists := herr == nil
	if hostExists && !translate.IsManaged(hpod) {
		// cache 已按 managed 过滤，理论上不会到这里；保险起见不碰非我们建的对象
		hostExists = false
	}

	// 虚拟 Pod 已不存在：清理宿主 Pod 与 token Secret
	if isNotFound(verr) {
		if hostExists && hpod.DeletionTimestamp == nil {
			if err := r.c.Host.Delete(ctx, hpod); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, r.deleteSATokenSecret(ctx, hostName)
	}

	// 虚拟 Pod 正在删除
	if vpod.DeletionTimestamp != nil {
		if hostExists {
			if hpod.DeletionTimestamp == nil {
				grace := int64(30)
				if vpod.DeletionGracePeriodSeconds != nil {
					grace = *vpod.DeletionGracePeriodSeconds
				} else if vpod.Spec.TerminationGracePeriodSeconds != nil {
					grace = *vpod.Spec.TerminationGracePeriodSeconds
				}
				if err := r.c.Host.Delete(ctx, hpod, client.GracePeriodSeconds(grace)); client.IgnoreNotFound(err) != nil {
					return ctrl.Result{}, err
				}
			}
			// 等宿主 Pod 真正消失（宿主 watch 会再次触发）
			return ctrl.Result{}, nil
		}
		// 宿主已消失：替代 kubelet 的收尾职责
		if err := r.c.Virtual.Delete(ctx, vpod, client.GracePeriodSeconds(0)); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.deleteSATokenSecret(ctx, hostName)
	}

	// 宿主 Pod 不存在
	if !hostExists {
		if translate.IsTerminal(vpod) {
			// 终态 Pod 只是历史记录，不重建
			return ctrl.Result{}, nil
		}
		if translate.HostPodEverExisted(vpod) {
			// 宿主 Pod 被外力删除（驱逐、节点故障、宿主 GC）→ 删虚拟 Pod 让控制器重建
			r.c.Recorder.Event(vpod, corev1.EventTypeNormal, EventHostPodGone,
				"host pod was deleted outside the virtual cluster; deleting virtual pod so its controller can recreate it")
			if err := r.c.Virtual.Delete(ctx, vpod, client.GracePeriodSeconds(0)); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.deleteSATokenSecret(ctx, hostName)
		}
		return r.create(ctx, vpod)
	}

	// 宿主正在被外力删除（驱逐等）：同步删除虚拟 Pod，宿主消失后进入上面的收尾分支
	if hpod.DeletionTimestamp != nil {
		if err := r.c.Virtual.Delete(ctx, vpod); client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	return r.update(ctx, vpod, hpod)
}

// create 翻译并创建宿主 Pod；需要 SA token 时先建 Secret，避免 Pod 卡在等 volume。
func (r *podReconciler) create(ctx context.Context, vpod *corev1.Pod) (ctrl.Result, error) {
	desired, err := translate.TranslatePod(vpod, r.c.podEnv(ctx))
	if err != nil {
		return r.c.handleSyncError("pod", vpod, err)
	}
	var requeue time.Duration
	if translate.NeedsSAToken(vpod) {
		requeue, err = r.ensureSAToken(ctx, vpod, desired.Name, nil)
		if err != nil {
			return r.c.handleSyncError("pod", vpod, err)
		}
	}
	if err := r.c.Host.Create(ctx, desired); err != nil {
		// PSA、配额、校验等宿主拒绝在这里以 Event 带原话呈现给用户
		return r.c.handleSyncError("pod", vpod, err)
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// update 宿主 Pod 已存在：可变字段跟随、绑定节点、回写 status、续签 token。
func (r *podReconciler) update(ctx context.Context, vpod, hpod *corev1.Pod) (ctrl.Result, error) {
	// 宿主把 Pod 调度到了不再允许的节点（placement 变化前创建、变化后才被调度）：驱逐，不做 binding。
	// 节点读取出错时返回错误等重试，不把「不确定」当成「不允许」去做不可逆的删除。
	if node := hpod.Spec.NodeName; node != "" && !translate.IsTerminal(hpod) {
		allowed, err := r.c.nodeAllowedNow(ctx, node)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("check node %s: %w", node, err)
		}
		if !allowed {
			return ctrl.Result{}, r.c.evictPod(ctx, vpod, hpod, node)
		}
	}
	desired, err := translate.TranslatePod(vpod, r.c.podEnv(ctx))
	if err != nil && !translate.IsNotReady(err) {
		return r.c.handleSyncError("pod", vpod, err)
	}
	if err == nil && translate.ApplyPodMutations(desired, hpod) {
		if err := r.c.Host.Update(ctx, hpod); err != nil {
			return r.c.handleSyncError("pod", vpod, err)
		}
	}
	// kubectl debug：虚拟 Pod 新增的 ephemeral 容器经子资源追加到宿主 Pod（只能增不能改删）
	if err == nil {
		if missing := translate.MissingEphemeralContainers(desired, hpod); len(missing) > 0 {
			hpod.Spec.EphemeralContainers = append(hpod.Spec.EphemeralContainers, missing...)
			if err := r.c.Host.SubResource("ephemeralcontainers").Update(ctx, hpod); err != nil {
				return r.c.handleSyncError("pod", vpod, err)
			}
		}
	}

	// 调度结果：节点名两侧相同，用 binding 子资源写 spec.nodeName
	if hpod.Spec.NodeName != "" && vpod.Spec.NodeName == "" {
		binding := &corev1.Binding{
			ObjectMeta: metav1.ObjectMeta{Name: vpod.Name, Namespace: vpod.Namespace, UID: vpod.UID},
			Target:     corev1.ObjectReference{Kind: "Node", Name: hpod.Spec.NodeName},
		}
		if err := r.c.VirtualClientset.CoreV1().Pods(vpod.Namespace).Bind(ctx, binding, metav1.CreateOptions{}); err != nil {
			return r.c.handleSyncError("pod", vpod, err)
		}
	}

	// status 整体回写（merge patch，避免与 binding 产生的 resourceVersion 变化冲突）。
	// qosClass 由各自 apiserver 按 spec 计算且不可变：宿主 LimitRange 给容器补了默认 request/limit 后
	// 宿主是 Burstable 而虚拟侧是 BestEffort，照抄会被拒（status.qosClass: field is immutable），保留虚拟侧的值。
	want := hpod.Status.DeepCopy()
	want.QOSClass = vpod.Status.QOSClass
	if !equality.Semantic.DeepEqual(vpod.Status, *want) {
		orig := vpod.DeepCopy()
		vpod.Status = *want
		if err := r.c.Virtual.Status().Patch(ctx, vpod, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}

	var requeue time.Duration
	if translate.NeedsSAToken(vpod) && !translate.IsTerminal(vpod) {
		requeue, err = r.ensureSAToken(ctx, vpod, hpod.Name, hpod)
		if err != nil {
			return r.c.handleSyncError("pod", vpod, err)
		}
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// setupPod 注册 Pod controller：虚拟 Pod + 宿主 Pod + 宿主 SA token Secret 三路事件。
func setupPod(mgr ctrl.Manager, c *Context) error {
	r := &podReconciler{c: c}
	tokenSecrets := predicate.NewTypedPredicateFuncs[*corev1.Secret](func(s *corev1.Secret) bool {
		return IsSATokenSecret(s)
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("pod").
		For(&corev1.Pod{}).
		WatchesRawSource(hostSource[*corev1.Pod](c.HostCache, &corev1.Pod{})).
		WatchesRawSource(source.Kind[*corev1.Secret](c.HostCache, &corev1.Secret{},
			handler.TypedEnqueueRequestsFromMapFunc[*corev1.Secret, reconcile.Request](func(_ context.Context, s *corev1.Secret) []reconcile.Request {
				key, ok := translate.VirtualKey(s)
				if !ok {
					return nil
				}
				return []reconcile.Request{{NamespacedName: key}}
			}), tokenSecrets)).
		Complete(r)
}

// podSecretOwner 供 SA token Secret 挂 ownerReference，宿主 GC 随 Pod 一起删。
func podSecretOwner(hpod *corev1.Pod) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: "v1",
		Kind:       "Pod",
		Name:       hpod.Name,
		UID:        hpod.UID,
	}
}

// saTokenSecretLabels SA token Secret 的标签：managed + 虚拟 Pod 键（供 Pod reconciler 反查）+ 宿主 Pod 名。
func saTokenSecretLabels(vpod *corev1.Pod, hostPodName string) map[string]string {
	return map[string]string{
		names.LabelManaged:    "true",
		names.LabelVName:      vpod.Name,
		names.LabelVNamespace: vpod.Namespace,
		LabelSATokenPod:       hostPodName,
	}
}
