// Package controllers 实现 manager 内的 reconciler：VirtualCluster 生命周期与 AccessToken 回收。
package controllers

import (
	"context"
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/config"
	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/pki"
)

const (
	// ManagerServiceName manager 在 vk8s-system 内的 Service 名；TokenReview 回调地址与 manager 服务端证书 SAN 都用它。
	ManagerServiceName = "vk8s-manager"

	// AnnotationCertsHash 写在 StatefulSet pod template 上，证书 Secret 内容变化时触发滚动重启。
	AnnotationCertsHash = names.LabelPrefix + "certs-hash"

	// 控制平面 pod 的附加标签。
	LabelComponent        = "app.kubernetes.io/component"
	LabelManagedBy        = "app.kubernetes.io/managed-by"
	ComponentControlPlane = "control-plane"
	ManagedByValue        = "vk8s-manager"

	// Running 状态下的周期性重入间隔，用于证书到期检查。
	requeueRunning = time.Hour
	// 控制平面尚未就绪时的重入间隔（兜底，正常由 StatefulSet 事件触发）。
	requeueProvisioning = 30 * time.Second
	// 删除时等待宿主 namespace 消失的轮询间隔。
	requeueDeleting = 5 * time.Second

	// 校验失败的 reason。
	ReasonInvalidName        = "InvalidName"
	ReasonOwnerNotFound      = "OwnerNotFound"
	ReasonMemberNotFound     = "MemberNotFound"
	ReasonQuotaExceedsMax    = "QuotaExceedsMaximum"
	ReasonTooManyVClusters   = "TooManyVClusters"
	ReasonInvalidPodSecurity = "InvalidPodSecurity"
)

// VirtualClusterReconciler 按设计文档第 5 节的 12 步调和 VirtualCluster。
type VirtualClusterReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Config     *config.Config
	PlatformCA *pki.CA
	Recorder   record.EventRecorder
}

// SetupWithManager 注册 controller。
func (r *VirtualClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("virtualcluster").
		For(&v1alpha1.VirtualCluster{}).
		Owns(&corev1.Namespace{}).
		Owns(&corev1.Service{}).
		Owns(&appsv1.StatefulSet{}).
		// owner / member 对应的 User 出现或消失时重新校验。
		Watches(&v1alpha1.User{}, handler.EnqueueRequestsFromMapFunc(r.vclustersForUser)).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).
		Complete(r)
}

// vclustersForUser 找出以该用户为 owner 或 member 的全部虚拟集群。
func (r *VirtualClusterReconciler) vclustersForUser(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &v1alpha1.VirtualClusterList{}
	if err := r.List(ctx, list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		if list.Items[i].HasAccess(obj.GetName()) {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
		}
	}
	return reqs
}

// Reconcile 入口。状态在最后统一 patch，避免每步各写一次。
func (r *VirtualClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	vc := &v1alpha1.VirtualCluster{}
	if err := r.Get(ctx, req.NamespacedName, vc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	orig := vc.DeepCopy()
	var (
		res ctrl.Result
		err error
	)
	if !vc.DeletionTimestamp.IsZero() {
		res, err = r.reconcileDelete(ctx, vc)
	} else {
		res, err = r.reconcileNormal(ctx, vc)
	}

	if perr := r.patchStatus(ctx, vc, orig); perr != nil {
		if apierrors.IsNotFound(perr) {
			return ctrl.Result{}, nil
		}
		log.Error(perr, "patch status")
		if err == nil {
			err = perr
		}
	}
	return res, err
}

// patchStatus 仅在状态有变化时写 status 子资源。
func (r *VirtualClusterReconciler) patchStatus(ctx context.Context, vc, orig *v1alpha1.VirtualCluster) error {
	if equality.Semantic.DeepEqual(orig.Status, vc.Status) {
		return nil
	}
	return r.Status().Patch(ctx, vc, client.MergeFrom(orig))
}

// reconcileNormal 非删除路径的 1–11 步。
func (r *VirtualClusterReconciler) reconcileNormal(ctx context.Context, vc *v1alpha1.VirtualCluster) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// 1. 校验：不合规 → Failed，不重试，等 spec 变化。
	if reason, msg, err := r.validate(ctx, vc); err != nil {
		return ctrl.Result{}, err
	} else if reason != "" {
		if vc.Status.Phase != v1alpha1.PhaseFailed || vc.Status.Reason != msg {
			r.Recorder.Event(vc, corev1.EventTypeWarning, reason, msg)
		}
		vc.Status.Phase = v1alpha1.PhaseFailed
		vc.Status.Reason = msg
		vc.Status.ObservedGeneration = vc.Generation
		return ctrl.Result{}, nil
	}

	// 2. finalizer。
	if !controllerutil.ContainsFinalizer(vc, names.FinalizerCleanup) {
		controllerutil.AddFinalizer(vc, names.FinalizerCleanup)
		if err := r.Update(ctx, vc); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
	}

	if vc.Status.Phase == "" || vc.Status.Phase == v1alpha1.PhasePending || vc.Status.Phase == v1alpha1.PhaseFailed {
		vc.Status.Phase = v1alpha1.PhaseProvisioning
		vc.Status.Reason = ""
	}
	vc.Status.HostNamespace = names.HostNamespace(vc.Name)

	// 3. Namespace。
	if err := r.ensureNamespace(ctx, vc); err != nil {
		return r.stepFailed(vc, "NamespaceFailed", err)
	}
	// 4. ResourceQuota。
	if err := r.ensureResourceQuota(ctx, vc); err != nil {
		return r.stepFailed(vc, "ResourceQuotaFailed", err)
	}
	// 5. LimitRange。
	if err := r.ensureLimitRange(ctx, vc); err != nil {
		return r.stepFailed(vc, "LimitRangeFailed", err)
	}
	// 6. NetworkPolicy（仅非 privileged）。
	if err := r.ensureNetworkPolicy(ctx, vc); err != nil {
		return r.stepFailed(vc, "NetworkPolicyFailed", err)
	}
	// 7. Service，先拿 ClusterIP。
	clusterIP, err := r.ensureService(ctx, vc)
	if err != nil {
		return r.stepFailed(vc, "ServiceFailed", err)
	}
	// 8. PKI。
	certsHash, err := r.ensureCertificates(ctx, vc, clusterIP)
	if err != nil {
		setCondition(vc, v1alpha1.ConditionCertificatesReady, metav1.ConditionFalse, "Error", err.Error())
		return r.stepFailed(vc, "CertificatesFailed", err)
	}
	setCondition(vc, v1alpha1.ConditionCertificatesReady, metav1.ConditionTrue, "Ready", "")
	// 泛证书副本（可选，失败不阻塞）。
	if err := r.ensureAppsTLS(ctx, vc); err != nil {
		log.Error(err, "copy apps TLS secret")
		r.Recorder.Event(vc, corev1.EventTypeWarning, "AppsTLSCopyFailed", err.Error())
	}
	// 9. syncer RBAC。
	if err := r.ensureRBAC(ctx, vc); err != nil {
		return r.stepFailed(vc, "RBACFailed", err)
	}
	// 10. StatefulSet。
	sts, err := r.ensureStatefulSet(ctx, vc, certsHash)
	if err != nil {
		return r.stepFailed(vc, "StatefulSetFailed", err)
	}

	// 11. 就绪判定。
	ready, err := r.observeReadiness(ctx, vc, sts)
	if err != nil {
		return ctrl.Result{}, err
	}
	vc.Status.ObservedGeneration = vc.Generation
	vc.Status.APIEndpoint = r.Config.APIEndpoint(vc.Name)
	vc.Status.AppsDomain = names.AppsDomain(vc.Name, r.Config.AppsDomain)
	if ready {
		if vc.Status.Phase != v1alpha1.PhaseRunning {
			r.Recorder.Event(vc, corev1.EventTypeNormal, "Ready", "virtual cluster is running")
		}
		vc.Status.Phase = v1alpha1.PhaseRunning
		vc.Status.Reason = ""
		return ctrl.Result{RequeueAfter: requeueRunning}, nil
	}
	vc.Status.Phase = v1alpha1.PhaseProvisioning
	vc.Status.Reason = "ControlPlaneNotReady"
	return ctrl.Result{RequeueAfter: requeueProvisioning}, nil
}

// stepFailed 记录某一步的错误并返回 err 以便指数退避重试。
func (r *VirtualClusterReconciler) stepFailed(vc *v1alpha1.VirtualCluster, reason string, err error) (ctrl.Result, error) {
	vc.Status.Phase = v1alpha1.PhaseProvisioning
	vc.Status.Reason = fmt.Sprintf("%s: %v", reason, err)
	r.Recorder.Event(vc, corev1.EventTypeWarning, reason, err.Error())
	return ctrl.Result{}, err
}

// validate 第 1 步。返回非空 reason 表示校验失败（不重试）；err 表示临时性错误（重试）。
func (r *VirtualClusterReconciler) validate(ctx context.Context, vc *v1alpha1.VirtualCluster) (reason, msg string, err error) {
	if e := ValidateName(r.Config, vc.Name); e != nil {
		return ReasonInvalidName, e.Error(), nil
	}
	if e := ValidatePodSecurity(vc.Spec.PodSecurity); e != nil {
		return ReasonInvalidPodSecurity, e.Error(), nil
	}
	if vc.Spec.Owner == "" {
		return ReasonOwnerNotFound, "spec.owner is empty", nil
	}
	owner := &v1alpha1.User{}
	if e := r.Get(ctx, types.NamespacedName{Name: vc.Spec.Owner}, owner); e != nil {
		if apierrors.IsNotFound(e) {
			return ReasonOwnerNotFound, fmt.Sprintf("owner %q does not exist", vc.Spec.Owner), nil
		}
		return "", "", e
	}
	for _, m := range vc.Spec.Members {
		u := &v1alpha1.User{}
		if e := r.Get(ctx, types.NamespacedName{Name: m}, u); e != nil {
			if apierrors.IsNotFound(e) {
				return ReasonMemberNotFound, fmt.Sprintf("member %q does not exist", m), nil
			}
			return "", "", e
		}
	}
	if e := ValidateQuota(vc.Spec.Quota, r.Config.Defaults.MaxQuota); e != nil {
		return ReasonQuotaExceedsMax, e.Error(), nil
	}
	// owned 数量：按创建时间排序，只有排在上限之外的集群判为失败，避免整批集群一起 Failed。
	list := &v1alpha1.VirtualClusterList{}
	if e := r.List(ctx, list); e != nil {
		return "", "", e
	}
	owned := make([]v1alpha1.VirtualCluster, 0)
	for _, item := range list.Items {
		if item.Spec.Owner == vc.Spec.Owner && item.DeletionTimestamp.IsZero() {
			owned = append(owned, item)
		}
	}
	sort.Slice(owned, func(i, j int) bool {
		ti, tj := owned[i].CreationTimestamp, owned[j].CreationTimestamp
		if ti.Equal(&tj) {
			return owned[i].Name < owned[j].Name
		}
		return ti.Before(&tj)
	})
	max := r.Config.Limits.MaxVClustersPerUser
	if max > 0 {
		for idx, item := range owned {
			if item.Name == vc.Name && idx >= max {
				return ReasonTooManyVClusters, fmt.Sprintf("owner %q already has %d virtual clusters (limit %d)", vc.Spec.Owner, max, max), nil
			}
		}
	}
	return "", "", nil
}

// reconcileDelete 第 12 步：显式删宿主 namespace，等它真正消失再放开 finalizer。
// vk8s-system 内的 Secret、ClusterRole、AccessToken 等都带 ownerReference，由 GC 级联。
func (r *VirtualClusterReconciler) reconcileDelete(ctx context.Context, vc *v1alpha1.VirtualCluster) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(vc, names.FinalizerCleanup) {
		return ctrl.Result{}, nil
	}
	vc.Status.Phase = v1alpha1.PhaseDeleting
	vc.Status.Reason = ""

	ns := &corev1.Namespace{}
	err := r.Get(ctx, types.NamespacedName{Name: names.HostNamespace(vc.Name)}, ns)
	switch {
	case apierrors.IsNotFound(err):
		// namespace 已不存在，放行。
		controllerutil.RemoveFinalizer(vc, names.FinalizerCleanup)
		if err := r.Update(ctx, vc); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{}, nil
	case err != nil:
		return ctrl.Result{}, err
	}
	// 只删除确属本集群的 namespace，防止误删同名无关 ns。
	if ns.Labels[names.LabelVCluster] != vc.Name {
		controllerutil.RemoveFinalizer(vc, names.FinalizerCleanup)
		if err := r.Update(ctx, vc); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{}, nil
	}
	if ns.DeletionTimestamp.IsZero() {
		if err := r.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete namespace: %w", err)
		}
	}
	vc.Status.Reason = "WaitingForNamespaceDeletion"
	return ctrl.Result{RequeueAfter: requeueDeleting}, nil
}

// observeReadiness 读 StatefulSet 与 pod，写 ControlPlaneReady / SyncerReady 两个 condition。
func (r *VirtualClusterReconciler) observeReadiness(ctx context.Context, vc *v1alpha1.VirtualCluster, sts *appsv1.StatefulSet) (bool, error) {
	pod := &corev1.Pod{}
	err := r.Get(ctx, types.NamespacedName{Namespace: names.HostNamespace(vc.Name), Name: names.StatefulSetName(vc.Name) + "-0"}, pod)
	if err != nil && !apierrors.IsNotFound(err) {
		return false, err
	}
	cpReady, syncerReady := false, false
	cpMsg, syncerMsg := "pod not found", "pod not found"
	if err == nil {
		cpMsg, syncerMsg = "container not started", "container not started"
		for _, cs := range pod.Status.ContainerStatuses {
			msg := containerStateMessage(cs)
			switch cs.Name {
			case names.ContainerControlPlane:
				cpReady, cpMsg = cs.Ready, msg
			case names.ContainerSyncer:
				syncerReady, syncerMsg = cs.Ready, msg
			}
		}
	}
	setReadyCondition(vc, v1alpha1.ConditionControlPlaneReady, cpReady, cpMsg)
	setReadyCondition(vc, v1alpha1.ConditionSyncerReady, syncerReady, syncerMsg)

	stsReady := sts.Status.ReadyReplicas >= 1 && sts.Status.ObservedGeneration >= sts.Generation
	return stsReady && cpReady && syncerReady, nil
}

func containerStateMessage(cs corev1.ContainerStatus) string {
	switch {
	case cs.Ready:
		return ""
	case cs.State.Waiting != nil:
		return fmt.Sprintf("waiting: %s %s", cs.State.Waiting.Reason, cs.State.Waiting.Message)
	case cs.State.Terminated != nil:
		return fmt.Sprintf("terminated: %s %s", cs.State.Terminated.Reason, cs.State.Terminated.Message)
	case cs.State.Running != nil:
		return "running, not ready"
	default:
		return "unknown"
	}
}

func setReadyCondition(vc *v1alpha1.VirtualCluster, condType string, ready bool, msg string) {
	if ready {
		setCondition(vc, condType, metav1.ConditionTrue, "Ready", "")
		return
	}
	setCondition(vc, condType, metav1.ConditionFalse, "NotReady", msg)
}

func setCondition(vc *v1alpha1.VirtualCluster, condType string, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&vc.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: vc.Generation,
	})
}

// commonLabels 本 reconciler 创建的全部宿主对象都带这组标签。
func commonLabels(vc *v1alpha1.VirtualCluster) map[string]string {
	return map[string]string{
		names.LabelVCluster: vc.Name,
		LabelManagedBy:      ManagedByValue,
	}
}

// podSelectorLabels 控制平面 pod 的选择标签（Service selector / StatefulSet selector）。
func podSelectorLabels(vc *v1alpha1.VirtualCluster) map[string]string {
	return map[string]string{
		names.LabelVCluster: vc.Name,
		LabelComponent:      ComponentControlPlane,
	}
}

// setOwner 把 VirtualCluster 设为 controller owner（cluster-scoped owner 允许被任意对象引用）。
func (r *VirtualClusterReconciler) setOwner(vc *v1alpha1.VirtualCluster, obj client.Object) error {
	return controllerutil.SetControllerReference(vc, obj, r.Scheme)
}

// mergeLabels 把 want 合并进 obj 现有标签，不删除别人加的标签。
func mergeLabels(obj client.Object, want map[string]string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	for k, v := range want {
		labels[k] = v
	}
	obj.SetLabels(labels)
}
