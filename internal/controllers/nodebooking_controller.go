package controllers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/manager/metrics"
	"vk8s.io/vk8s/internal/names"
)

const (
	// IndexBookingNode / IndexBookingVCluster NodeBooking 的字段索引名。
	IndexBookingNode     = "spec.node"
	IndexBookingVCluster = "spec.vcluster"

	// DefaultBookingRetention 终态预约的默认保留时长。
	DefaultBookingRetention = 168 * time.Hour

	// 事件 reason。
	EventNodeLeased      = "NodeLeased"
	EventNodeReleased    = "NodeReleased"
	EventLeaseRevoked    = "LeaseRevoked"
	EventBookingRejected = "BookingRejected"

	// requeueJitter 边界时刻之后再多等一点，避免时钟边缘重复判定。
	requeueJitter = time.Second
)

func bookingNodeIndexer(o client.Object) []string {
	return []string{o.(*v1alpha1.NodeBooking).Spec.Node}
}

func bookingVClusterIndexer(o client.Object) []string {
	return []string{o.(*v1alpha1.NodeBooking).Spec.VCluster}
}

// RegisterBookingIndexes 注册控制器与 API 共用的 NodeBooking 索引。
func RegisterBookingIndexes(ctx context.Context, idx client.FieldIndexer) error {
	if err := idx.IndexField(ctx, &v1alpha1.NodeBooking{}, IndexBookingNode, bookingNodeIndexer); err != nil {
		return err
	}
	return idx.IndexField(ctx, &v1alpha1.NodeBooking{}, IndexBookingVCluster, bookingVClusterIndexer)
}

// NodeBookingReconciler 推进 NodeBooking 的 phase，并把生效中的租约写进 VirtualCluster.status.leasedNodes。
// 只写 NodeBooking 的 status / finalizer 与 VirtualCluster 的 status.leasedNodes，不碰任何 spec。
type NodeBookingReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// Retention 终态保留时长；≤0 取 DefaultBookingRetention。
	Retention time.Duration
	// Now 可注入以便测试；为空取 time.Now。
	Now func() time.Time
}

// SetupWithManager 注册 controller：NodeBooking 本身，以及宿主 Node 的 bookable 标签变化 / 删除。
func (r *NodeBookingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("nodebooking").
		For(&v1alpha1.NodeBooking{}).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(r.bookingsForNode), builder.WithPredicates(bookableLabelChanged())).
		Complete(r)
}

// bookableLabelChanged 只放行与 vk8s.io/bookable 标签有关的 Node 事件。
func bookableLabelChanged() predicate.Funcs {
	label := func(o client.Object) string { return o.GetLabels()[names.LabelBookable] }
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return label(e.Object) == names.BookableTrue },
		UpdateFunc:  func(e event.UpdateEvent) bool { return label(e.ObjectOld) != label(e.ObjectNew) },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// bookingsForNode 该节点上的全部预约。
func (r *NodeBookingReconciler) bookingsForNode(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &v1alpha1.NodeBookingList{}
	if err := r.List(ctx, list, client.MatchingFields{IndexBookingNode: obj.GetName()}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
	}
	return reqs
}

func (r *NodeBookingReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *NodeBookingReconciler) retention() time.Duration {
	if r.Retention > 0 {
		return r.Retention
	}
	return DefaultBookingRetention
}

// Reconcile 入口。
func (r *NodeBookingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	b := &v1alpha1.NodeBooking{}
	if err := r.Get(ctx, req.NamespacedName, b); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 删除：先把节点从目标集群的租约里移出，再放行。
	if !b.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(b, names.FinalizerLease) {
			return ctrl.Result{}, nil
		}
		if err := r.syncLeases(ctx, b.Spec.VCluster, b.Name, nil); err != nil {
			return ctrl.Result{}, err
		}
		orig := b.DeepCopy()
		controllerutil.RemoveFinalizer(b, names.FinalizerLease)
		if err := r.Patch(ctx, b, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		log.Info("booking deleted, lease released", "booking", b.Name, "node", b.Spec.Node, "vcluster", b.Spec.VCluster)
		return ctrl.Result{}, nil
	}

	now := r.now()
	facts, vc, err := r.collectFacts(ctx, b, now)
	if err != nil {
		return ctrl.Result{}, err
	}
	res := decidePhase(b, facts)
	terminal := isTerminalPhase(res.Phase)

	orig := b.DeepCopy()
	metaChanged := false
	if !terminal && controllerutil.AddFinalizer(b, names.FinalizerLease) {
		metaChanged = true
	}
	if terminal && controllerutil.RemoveFinalizer(b, names.FinalizerLease) {
		metaChanged = true
	}
	statusChanged := res.Phase != b.Status.Phase || res.Reason != b.Status.Reason || res.Message != b.Status.Message
	if statusChanged {
		b.Status.Phase, b.Status.Reason, b.Status.Message = res.Phase, res.Reason, res.Message
		ts := metav1.NewTime(now)
		if res.Phase == v1alpha1.NodeBookingActive && b.Status.ActivatedAt == nil {
			b.Status.ActivatedAt = &ts
		}
		if terminal && b.Status.ReleasedAt == nil {
			b.Status.ReleasedAt = &ts
			metrics.BookingsTotal.WithLabelValues(strings.ToLower(string(res.Phase))).Inc()
		}
	}
	// 元数据与 status 分两次提交，各用独立副本：Patch 会把服务端返回的对象回写进参数，
	// 若共用 b，第一次提交就会把内存里尚未提交的另一半改动冲掉。
	// 顺序：非终态先加 finalizer 再写 status（Active 的对象一定带 finalizer，删除时才会先撤租约）；
	// 终态先写 status、重算租约，最后才摘 finalizer（两步之间对象被删也不会留下脏租约）。
	patchMeta := func() error {
		m := orig.DeepCopy()
		m.Finalizers = b.Finalizers
		if err := r.Patch(ctx, m, client.MergeFrom(orig)); err != nil {
			return err
		}
		b.ResourceVersion = m.ResourceVersion
		return nil
	}
	patchStatus := func() error {
		st := orig.DeepCopy()
		st.ResourceVersion = b.ResourceVersion
		st.Status = b.Status
		if err := r.Status().Patch(ctx, st, client.MergeFrom(orig)); err != nil {
			return err
		}
		b.ResourceVersion = st.ResourceVersion
		log.Info("booking phase changed", "booking", b.Name, "node", b.Spec.Node, "vcluster", b.Spec.VCluster, "phase", res.Phase, "reason", res.Reason)
		switch res.Phase {
		case v1alpha1.NodeBookingRevoked:
			if vc != nil {
				r.Recorder.Eventf(vc, corev1.EventTypeWarning, EventLeaseRevoked, "lease of node %s (booking %s) revoked: %s", b.Spec.Node, b.Name, res.Message)
			}
		case v1alpha1.NodeBookingRejected:
			r.Recorder.Eventf(b, corev1.EventTypeWarning, EventBookingRejected, "%s: %s", res.Reason, res.Message)
		}
		return nil
	}
	leases := func() error {
		if vc == nil {
			return nil
		}
		return r.syncLeases(ctx, vc.Name, "", b)
	}
	var steps []func() error
	if terminal {
		if statusChanged {
			steps = append(steps, patchStatus)
		}
		steps = append(steps, leases)
		if metaChanged {
			steps = append(steps, patchMeta)
		}
	} else {
		if metaChanged {
			steps = append(steps, patchMeta)
		}
		if statusChanged {
			steps = append(steps, patchStatus)
		}
		steps = append(steps, leases)
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return ctrl.Result{}, err
		}
	}

	if terminal {
		if b.Status.ReleasedAt != nil && !now.Before(b.Status.ReleasedAt.Time.Add(r.retention())) {
			if err := r.Delete(ctx, b); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			log.Info("deleted booking past retention", "booking", b.Name, "phase", b.Status.Phase)
			return ctrl.Result{}, nil
		}
	}
	after := nextBoundary(b, res.Phase, now, r.retention()).Sub(now) + requeueJitter
	if after < requeueJitter {
		after = requeueJitter
	}
	return ctrl.Result{RequeueAfter: after}, nil
}

func isTerminalPhase(p v1alpha1.NodeBookingPhase) bool {
	return p == v1alpha1.NodeBookingCompleted || p == v1alpha1.NodeBookingRejected || p == v1alpha1.NodeBookingRevoked
}

// collectFacts 收集判定所需的外部事实；目标集群不存在时返回的 vc 为 nil。
func (r *NodeBookingReconciler) collectFacts(ctx context.Context, b *v1alpha1.NodeBooking, now time.Time) (bookingFacts, *v1alpha1.VirtualCluster, error) {
	f := bookingFacts{Now: now}
	vc := &v1alpha1.VirtualCluster{}
	switch err := r.Get(ctx, types.NamespacedName{Name: b.Spec.VCluster}, vc); {
	case err == nil:
		f.VCExists = vc.DeletionTimestamp.IsZero()
	case apierrors.IsNotFound(err):
		vc = nil
	default:
		return f, nil, fmt.Errorf("get VirtualCluster %s: %w", b.Spec.VCluster, err)
	}
	if !f.VCExists {
		vc = nil
	}
	node := &corev1.Node{}
	switch err := r.Get(ctx, types.NamespacedName{Name: b.Spec.Node}, node); {
	case err == nil:
		f.NodeExists = node.DeletionTimestamp.IsZero()
		f.Bookable = node.Labels[names.LabelBookable] == names.BookableTrue
	case apierrors.IsNotFound(err):
	default:
		return f, nil, fmt.Errorf("get Node %s: %w", b.Spec.Node, err)
	}
	list := &v1alpha1.NodeBookingList{}
	if err := r.List(ctx, list, client.MatchingFields{IndexBookingNode: b.Spec.Node}); err != nil {
		return f, nil, fmt.Errorf("list bookings on node %s: %w", b.Spec.Node, err)
	}
	for i := range list.Items {
		o := &list.Items[i]
		if o.Name == b.Name || o.IsTerminal() || !o.DeletionTimestamp.IsZero() {
			continue
		}
		f.Others = append(f.Others, *o)
	}
	return f, vc, nil
}

// holdsLease 预约此刻是否持有节点：按 spec 的时间判定而不是 status.phase ——
// 相邻两段预约在交界点、以及刚 patch 的 status 还没进 cache 时，phase 都可能滞后；按时间判定没有空窗。
// 非终态、未在删除中、start ≤ now < end。注定被拒绝的预约会在它自己的 Reconcile 里立刻撤掉。
func holdsLease(b *v1alpha1.NodeBooking, now time.Time) bool {
	if b.IsTerminal() || !b.DeletionTimestamp.IsZero() {
		return false
	}
	return !now.Before(b.Spec.Start.Time) && now.Before(b.Spec.End.Time)
}

// syncLeases 重算目标集群的 leasedNodes（全部此刻持有节点、且非 exclude 的预约），有变化才写 status 并发事件。
// fresh 为当前正在处理、内存里最新的预约对象，用它覆盖 cache 里可能滞后的同名条目；可为 nil。
func (r *NodeBookingReconciler) syncLeases(ctx context.Context, vcName, exclude string, fresh *v1alpha1.NodeBooking) error {
	vc := &v1alpha1.VirtualCluster{}
	if err := r.Get(ctx, types.NamespacedName{Name: vcName}, vc); err != nil {
		return client.IgnoreNotFound(err)
	}
	list := &v1alpha1.NodeBookingList{}
	if err := r.List(ctx, list, client.MatchingFields{IndexBookingVCluster: vcName}); err != nil {
		return fmt.Errorf("list bookings of %s: %w", vcName, err)
	}
	now := r.now()
	seen := false
	var desired []v1alpha1.LeasedNode
	for i := range list.Items {
		b := &list.Items[i]
		if fresh != nil && b.Name == fresh.Name {
			b = fresh
			seen = true
		}
		if b.Name == exclude || !holdsLease(b, now) {
			continue
		}
		desired = append(desired, v1alpha1.LeasedNode{Node: b.Spec.Node, Booking: b.Name, Until: b.Spec.End})
	}
	if fresh != nil && !seen && fresh.Spec.VCluster == vcName && fresh.Name != exclude && holdsLease(fresh, now) {
		desired = append(desired, v1alpha1.LeasedNode{Node: fresh.Spec.Node, Booking: fresh.Name, Until: fresh.Spec.End})
	}
	sort.Slice(desired, func(i, j int) bool {
		if desired[i].Node != desired[j].Node {
			return desired[i].Node < desired[j].Node
		}
		return desired[i].Booking < desired[j].Booking
	})
	r.updateActiveGauge(ctx)
	if equality.Semantic.DeepEqual(vc.Status.LeasedNodes, desired) {
		return nil
	}
	before := leaseSet(vc.Status.LeasedNodes)
	after := leaseSet(desired)
	orig := vc.DeepCopy()
	vc.Status.LeasedNodes = desired
	if err := r.Status().Patch(ctx, vc, client.MergeFrom(orig)); err != nil {
		return fmt.Errorf("patch leasedNodes of %s: %w", vcName, err)
	}
	for _, l := range desired {
		if _, ok := before[l.Node]; !ok {
			r.Recorder.Eventf(vc, corev1.EventTypeNormal, EventNodeLeased, "node %s leased by booking %s until %s", l.Node, l.Booking, l.Until.UTC().Format(time.RFC3339))
		}
	}
	for node, booking := range before {
		if _, ok := after[node]; !ok {
			r.Recorder.Eventf(vc, corev1.EventTypeNormal, EventNodeReleased, "node %s released (booking %s)", node, booking)
		}
	}
	return nil
}

func leaseSet(ls []v1alpha1.LeasedNode) map[string]string {
	m := make(map[string]string, len(ls))
	for _, l := range ls {
		m[l.Node] = l.Booking
	}
	return m
}

// updateActiveGauge 全局 Active 预约数。
func (r *NodeBookingReconciler) updateActiveGauge(ctx context.Context) {
	list := &v1alpha1.NodeBookingList{}
	if err := r.List(ctx, list); err != nil {
		return
	}
	n := 0
	for i := range list.Items {
		if list.Items[i].Status.Phase == v1alpha1.NodeBookingActive && list.Items[i].DeletionTimestamp.IsZero() {
			n++
		}
	}
	metrics.BookingsActive.Set(float64(n))
}
