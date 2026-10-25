package controllers

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/names"
)

const (
	testBookNode = "gpu-07"
	testBookVC   = "alice-dev"
)

func bat(h, m int) time.Time { return time.Date(2026, 10, 24, h, m, 0, 0, time.UTC) }

func newBookingTestEnv(t *testing.T, now time.Time, objs ...client.Object) (*NodeBookingReconciler, client.Client, *record.FakeRecorder) {
	t.Helper()
	return newBookingTestEnvWith(t, now, interceptor.Funcs{}, objs...)
}

func newBookingTestEnvWith(t *testing.T, now time.Time, funcs interceptor.Funcs, objs ...client.Object) (*NodeBookingReconciler, client.Client, *record.FakeRecorder) {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&v1alpha1.NodeBooking{}, &v1alpha1.VirtualCluster{}).
		WithIndex(&v1alpha1.NodeBooking{}, IndexBookingNode, bookingNodeIndexer).
		WithIndex(&v1alpha1.NodeBooking{}, IndexBookingVCluster, bookingVClusterIndexer).
		WithObjects(objs...).
		WithInterceptorFuncs(funcs).
		Build()
	rec := record.NewFakeRecorder(32)
	r := &NodeBookingReconciler{Client: c, Scheme: s, Recorder: rec, Retention: time.Hour, Now: func() time.Time { return now }}
	return r, c, rec
}

func hostNode(name string, bookable bool) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{}}}
	if bookable {
		n.Labels[names.LabelBookable] = names.BookableTrue
	}
	return n
}

func vclusterWithLeases(name string, leased ...v1alpha1.LeasedNode) *v1alpha1.VirtualCluster {
	vc := &v1alpha1.VirtualCluster{ObjectMeta: metav1.ObjectMeta{Name: name}}
	vc.Spec.Owner = "alice"
	vc.Status.LeasedNodes = leased
	return vc
}

// booking 构造预约；phase 非空时写进 status，非终态的 Active/Pending 视为已被控制器处理过（带 finalizer）。
func booking(name, node, vc string, start, end, created time.Time, phase v1alpha1.NodeBookingPhase) *v1alpha1.NodeBooking {
	b := &v1alpha1.NodeBooking{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(created)}}
	b.Spec = v1alpha1.NodeBookingSpec{Node: node, VCluster: vc, Start: metav1.NewTime(start), End: metav1.NewTime(end), User: "alice"}
	b.Status.Phase = phase
	if phase == v1alpha1.NodeBookingPending || phase == v1alpha1.NodeBookingActive {
		b.Finalizers = []string{names.FinalizerLease}
	}
	return b
}

func reconcileBooking(t *testing.T, r *NodeBookingReconciler, name string) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}})
	if err != nil {
		t.Fatalf("reconcile %s: %v", name, err)
	}
	return res
}

func getBooking(t *testing.T, c client.Client, name string) *v1alpha1.NodeBooking {
	t.Helper()
	b := &v1alpha1.NodeBooking{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, b); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		t.Fatal(err)
	}
	return b
}

func getVC(t *testing.T, c client.Client, name string) *v1alpha1.VirtualCluster {
	t.Helper()
	vc := &v1alpha1.VirtualCluster{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: name}, vc); err != nil {
		t.Fatal(err)
	}
	return vc
}

func drainEvents(rec *record.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func hasEvent(events []string, reason string) bool {
	for _, e := range events {
		if strings.Contains(e, reason) {
			return true
		}
	}
	return false
}

func hasFinalizer(b *v1alpha1.NodeBooking) bool {
	for _, f := range b.Finalizers {
		if f == names.FinalizerLease {
			return true
		}
	}
	return false
}

func TestReconcileActivatesAndWritesLease(t *testing.T) {
	now := bat(10, 30)
	r, c, rec := newBookingTestEnv(t, now,
		hostNode(testBookNode, true), vclusterWithLeases(testBookVC),
		booking("nb-a", testBookNode, testBookVC, bat(10, 0), bat(12, 0), bat(1, 0), ""))
	res := reconcileBooking(t, r, "nb-a")

	b := getBooking(t, c, "nb-a")
	if b.Status.Phase != v1alpha1.NodeBookingActive || b.Status.ActivatedAt == nil || !hasFinalizer(b) {
		t.Fatalf("booking = %+v / finalizers %v", b.Status, b.Finalizers)
	}
	vc := getVC(t, c, testBookVC)
	want := []v1alpha1.LeasedNode{{Node: testBookNode, Booking: "nb-a", Until: metav1.NewTime(bat(12, 0))}}
	if len(vc.Status.LeasedNodes) != 1 || vc.Status.LeasedNodes[0].Node != want[0].Node || vc.Status.LeasedNodes[0].Booking != want[0].Booking || !vc.Status.LeasedNodes[0].Until.Equal(&want[0].Until) {
		t.Fatalf("leasedNodes = %+v, want %+v", vc.Status.LeasedNodes, want)
	}
	if res.RequeueAfter < 90*time.Minute || res.RequeueAfter > 90*time.Minute+3*time.Second {
		t.Fatalf("RequeueAfter = %v, want ≈ 1h30m", res.RequeueAfter)
	}
	if ev := drainEvents(rec); !hasEvent(ev, EventNodeLeased) {
		t.Fatalf("want NodeLeased event, got %v", ev)
	}
}

func TestReconcileCompletesAfterEndWithoutManager(t *testing.T) {
	now := bat(13, 0)
	r, c, rec := newBookingTestEnv(t, now,
		hostNode(testBookNode, true),
		vclusterWithLeases(testBookVC, v1alpha1.LeasedNode{Node: testBookNode, Booking: "nb-a", Until: metav1.NewTime(bat(12, 0))}),
		booking("nb-a", testBookNode, testBookVC, bat(10, 0), bat(12, 0), bat(1, 0), v1alpha1.NodeBookingPending))
	reconcileBooking(t, r, "nb-a")

	b := getBooking(t, c, "nb-a")
	if b.Status.Phase != v1alpha1.NodeBookingCompleted || b.Status.ReleasedAt == nil || !b.Status.ReleasedAt.Time.Equal(now) || hasFinalizer(b) {
		t.Fatalf("booking = %+v / finalizers %v", b.Status, b.Finalizers)
	}
	if b.Status.ActivatedAt != nil {
		t.Fatalf("a booking that was never active must not get activatedAt: %v", b.Status.ActivatedAt)
	}
	if vc := getVC(t, c, testBookVC); len(vc.Status.LeasedNodes) != 0 {
		t.Fatalf("lease must be removed, got %+v", vc.Status.LeasedNodes)
	}
	if ev := drainEvents(rec); !hasEvent(ev, EventNodeReleased) {
		t.Fatalf("want NodeReleased event, got %v", ev)
	}
}

func TestReconcileRejectsLaterOverlapReconciledFirst(t *testing.T) {
	now := bat(9, 0)
	r, c, _ := newBookingTestEnv(t, now,
		hostNode(testBookNode, true), vclusterWithLeases(testBookVC),
		booking("nb-a", testBookNode, testBookVC, bat(11, 0), bat(13, 0), bat(1, 0), ""),
		booking("nb-b", testBookNode, testBookVC, bat(10, 0), bat(12, 0), bat(2, 0), ""))
	reconcileBooking(t, r, "nb-b")
	b := getBooking(t, c, "nb-b")
	if b.Status.Phase != v1alpha1.NodeBookingRejected || b.Status.Reason != v1alpha1.BookingReasonOverlap || !strings.Contains(b.Status.Message, "nb-a") {
		t.Fatalf("nb-b = %+v", b.Status)
	}
	if hasFinalizer(b) || b.Status.ReleasedAt == nil {
		t.Fatalf("rejected booking must have no finalizer and a releasedAt: %+v / %v", b.Status, b.Finalizers)
	}
	reconcileBooking(t, r, "nb-a")
	if a := getBooking(t, c, "nb-a"); a.Status.Phase != v1alpha1.NodeBookingPending || !hasFinalizer(a) {
		t.Fatalf("nb-a = %+v / %v", a.Status, a.Finalizers)
	}
}

func TestReconcileRevokesWhenLabelRemoved(t *testing.T) {
	now := bat(11, 0)
	r, c, rec := newBookingTestEnv(t, now,
		hostNode(testBookNode, false),
		vclusterWithLeases(testBookVC, v1alpha1.LeasedNode{Node: testBookNode, Booking: "nb-a", Until: metav1.NewTime(bat(12, 0))}),
		booking("nb-a", testBookNode, testBookVC, bat(10, 0), bat(12, 0), bat(1, 0), v1alpha1.NodeBookingActive))
	reconcileBooking(t, r, "nb-a")
	b := getBooking(t, c, "nb-a")
	if b.Status.Phase != v1alpha1.NodeBookingRevoked || b.Status.Reason != v1alpha1.BookingReasonNodeNotBookable {
		t.Fatalf("booking = %+v", b.Status)
	}
	if vc := getVC(t, c, testBookVC); len(vc.Status.LeasedNodes) != 0 {
		t.Fatalf("lease must be removed, got %+v", vc.Status.LeasedNodes)
	}
	ev := drainEvents(rec)
	if !hasEvent(ev, EventLeaseRevoked) || !hasEvent(ev, EventNodeReleased) {
		t.Fatalf("want LeaseRevoked and NodeReleased events, got %v", ev)
	}
}

func TestDeleteActiveBookingReleasesLease(t *testing.T) {
	now := bat(11, 0)
	r, c, _ := newBookingTestEnv(t, now,
		hostNode(testBookNode, true),
		vclusterWithLeases(testBookVC, v1alpha1.LeasedNode{Node: testBookNode, Booking: "nb-a", Until: metav1.NewTime(bat(12, 0))}),
		booking("nb-a", testBookNode, testBookVC, bat(10, 0), bat(12, 0), bat(1, 0), v1alpha1.NodeBookingActive))
	if err := c.Delete(context.Background(), getBooking(t, c, "nb-a")); err != nil {
		t.Fatal(err)
	}
	if b := getBooking(t, c, "nb-a"); b == nil || b.DeletionTimestamp.IsZero() {
		t.Fatal("fake client must keep the object with a deletionTimestamp while the finalizer is present")
	}
	reconcileBooking(t, r, "nb-a")
	if vc := getVC(t, c, testBookVC); len(vc.Status.LeasedNodes) != 0 {
		t.Fatalf("lease must be removed before the finalizer, got %+v", vc.Status.LeasedNodes)
	}
	if b := getBooking(t, c, "nb-a"); b != nil {
		t.Fatalf("booking must be gone after the finalizer is removed, got %+v", b.Finalizers)
	}
}

func TestGCDeletesTerminalAfterRetention(t *testing.T) {
	now := bat(12, 0)
	old := booking("nb-old", testBookNode, testBookVC, bat(6, 0), bat(8, 0), bat(1, 0), v1alpha1.NodeBookingCompleted)
	rel := metav1.NewTime(now.Add(-2 * time.Hour))
	old.Status.ReleasedAt = &rel
	fresh := booking("nb-fresh", testBookNode, testBookVC, bat(9, 0), bat(11, 30), bat(1, 0), v1alpha1.NodeBookingCompleted)
	rel2 := metav1.NewTime(now.Add(-30 * time.Minute))
	fresh.Status.ReleasedAt = &rel2
	r, c, _ := newBookingTestEnv(t, now, hostNode(testBookNode, true), vclusterWithLeases(testBookVC), old, fresh)

	reconcileBooking(t, r, "nb-old")
	if getBooking(t, c, "nb-old") != nil {
		t.Fatal("terminal booking past retention must be deleted")
	}
	res := reconcileBooking(t, r, "nb-fresh")
	if getBooking(t, c, "nb-fresh") == nil {
		t.Fatal("terminal booking within retention must be kept")
	}
	if res.RequeueAfter < 30*time.Minute || res.RequeueAfter > 30*time.Minute+3*time.Second {
		t.Fatalf("RequeueAfter = %v, want ≈ 30m", res.RequeueAfter)
	}
}

func TestBookingsForNodeMapsOnlyThatNode(t *testing.T) {
	r, _, _ := newBookingTestEnv(t, bat(9, 0),
		booking("nb-a", "gpu-07", testBookVC, bat(10, 0), bat(12, 0), bat(1, 0), ""),
		booking("nb-b", "gpu-08", testBookVC, bat(10, 0), bat(12, 0), bat(1, 0), ""))
	reqs := r.bookingsForNode(context.Background(), hostNode("gpu-07", true))
	if len(reqs) != 1 || reqs[0].Name != "nb-a" {
		t.Fatalf("requests = %+v", reqs)
	}
}

// 相邻两段预约 A(10–12)、B(12–14) 同属一个集群：12:00 先处理 A 时，B 的 status 还是 Pending（甚至还在 cache 滞后里），
// 但按时间它已经持有节点；租约重算必须按时间判定，否则 syncer 轮询落在空窗会把节点移出并清掉 Pod。
func TestSyncLeasesKeepsAdjacentSuccessorWhileStillPending(t *testing.T) {
	now := bat(12, 0)
	r, c, _ := newBookingTestEnv(t, now,
		hostNode(testBookNode, true),
		vclusterWithLeases(testBookVC, v1alpha1.LeasedNode{Node: testBookNode, Booking: "nb-a", Until: metav1.NewTime(bat(12, 0))}),
		booking("nb-a", testBookNode, testBookVC, bat(10, 0), bat(12, 0), bat(1, 0), v1alpha1.NodeBookingActive),
		booking("nb-b", testBookNode, testBookVC, bat(12, 0), bat(14, 0), bat(2, 0), v1alpha1.NodeBookingPending))
	reconcileBooking(t, r, "nb-a")
	if a := getBooking(t, c, "nb-a"); a.Status.Phase != v1alpha1.NodeBookingCompleted {
		t.Fatalf("nb-a = %+v", a.Status)
	}
	vc := getVC(t, c, testBookVC)
	if len(vc.Status.LeasedNodes) != 1 || vc.Status.LeasedNodes[0].Booking != "nb-b" || vc.Status.LeasedNodes[0].Node != testBookNode {
		t.Fatalf("successor must hold the lease without a gap, got %+v", vc.Status.LeasedNodes)
	}
}

// 终态转换必须先写 status、再摘 finalizer：若顺序相反，两步之间对象被删会跳过租约撤销，留下永久的脏租约。
func TestTerminalTransitionWritesStatusBeforeRemovingFinalizer(t *testing.T) {
	now := bat(13, 0)
	var calls []string
	funcs := interceptor.Funcs{
		Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if _, ok := obj.(*v1alpha1.NodeBooking); ok {
				calls = append(calls, "meta")
			}
			return cl.Patch(ctx, obj, patch, opts...)
		},
		SubResourcePatch: func(ctx context.Context, cl client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
			if _, ok := obj.(*v1alpha1.NodeBooking); ok {
				calls = append(calls, "status")
			}
			return cl.SubResource(sub).Patch(ctx, obj, patch, opts...)
		},
	}
	r, c, _ := newBookingTestEnvWith(t, now, funcs,
		hostNode(testBookNode, true), vclusterWithLeases(testBookVC),
		booking("nb-a", testBookNode, testBookVC, bat(10, 0), bat(12, 0), bat(1, 0), v1alpha1.NodeBookingPending))
	reconcileBooking(t, r, "nb-a")
	if b := getBooking(t, c, "nb-a"); b.Status.Phase != v1alpha1.NodeBookingCompleted || hasFinalizer(b) {
		t.Fatalf("booking = %+v / %v", b.Status, b.Finalizers)
	}
	if len(calls) < 2 || calls[0] != "status" || calls[len(calls)-1] != "meta" {
		t.Fatalf("status must be written before the finalizer is removed, got %v", calls)
	}
}
