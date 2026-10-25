package resources

import (
	"context"
	"strings"
	"testing"

	"errors"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/placement"
	"vk8s.io/vk8s/internal/syncer/translate"
)

const testHostNS = "vc-e2e"

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// virtualPod 一个绑定在 node 上、terminationGracePeriodSeconds 远大于驱逐上限的虚拟 Pod。
func virtualPod(name, ns, node string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID("uid-" + name)},
		Spec: corev1.PodSpec{
			NodeName:                      node,
			Containers:                    []corev1.Container{{Name: "c", Image: "nginx"}},
			TerminationGracePeriodSeconds: ptr.To[int64](3600),
		},
		Status: corev1.PodStatus{Phase: phase},
	}
}

// hostPodFor 虚拟 Pod 在宿主上的对应 Pod（带 managed 标签），绑定在同一节点。
func hostPodFor(vpod *corev1.Pod, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: translate.HostObjectMeta(vpod, testHostNS),
		Spec:       corev1.PodSpec{NodeName: vpod.Spec.NodeName, Containers: vpod.Spec.Containers},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

// deleteRecorder 记录虚拟 client 上每次 Delete 使用的 gracePeriodSeconds。
type deleteRecorder struct{ grace map[string]*int64 }

func (d *deleteRecorder) funcs() interceptor.Funcs {
	return interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			o := &client.DeleteOptions{}
			o.ApplyOptions(opts)
			d.grace[obj.GetNamespace()+"/"+obj.GetName()] = o.GracePeriodSeconds
			return c.Delete(ctx, obj, opts...)
		},
	}
}

type testOpts struct {
	deletes      *deleteRecorder
	virtualFuncs *interceptor.Funcs
	hostFuncs    *interceptor.Funcs
}

func newTestContext(t *testing.T, virtualObjs, hostObjs []client.Object, o testOpts) *Context {
	t.Helper()
	scheme := testScheme(t)
	vb := fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&corev1.Pod{}, IndexPodNodeName, podNodeNameIndexer).
		WithObjects(virtualObjs...)
	if o.deletes != nil {
		vb = vb.WithInterceptorFuncs(o.deletes.funcs())
	}
	if o.virtualFuncs != nil {
		vb = vb.WithInterceptorFuncs(*o.virtualFuncs)
	}
	hb := fake.NewClientBuilder().WithScheme(scheme).WithObjects(hostObjs...)
	if o.hostFuncs != nil {
		hb = hb.WithInterceptorFuncs(*o.hostFuncs)
	}
	return &Context{
		Log:           logr.Discard(),
		VCName:        "e2e",
		HostNamespace: testHostNS,
		Virtual:       vb.Build(),
		Host:          hb.Build(),
		Recorder:      record.NewFakeRecorder(16),
		Placement:     placement.NewHolder(),
	}
}

func expectNoEvent(t *testing.T, c *Context) {
	t.Helper()
	select {
	case ev := <-c.Recorder.(*record.FakeRecorder).Events:
		t.Fatalf("unexpected event %q", ev)
	default:
	}
}

func podExists(t *testing.T, c client.Client, ns, name string) bool {
	t.Helper()
	err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, &corev1.Pod{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get pod %s/%s: %v", ns, name, err)
	}
	return err == nil
}

func expectEviction(t *testing.T, c *Context, node string) {
	t.Helper()
	select {
	case ev := <-c.Recorder.(*record.FakeRecorder).Events:
		if !strings.Contains(ev, EventEvictedByNodePlacement) || !strings.Contains(ev, node) {
			t.Fatalf("unexpected event %q", ev)
		}
	default:
		t.Fatalf("no %s event recorded", EventEvictedByNodePlacement)
	}
}

func TestEvictPodsOnNodeDeletesOnlyEvictablePodsOnThatNode(t *testing.T) {
	now := metav1.Now()
	deleting := virtualPod("deleting", "demo", "node-x", corev1.PodRunning)
	deleting.DeletionTimestamp = &now
	deleting.Finalizers = []string{"test/keep"}
	web := virtualPod("web", "demo", "node-x", corev1.PodRunning)
	other := virtualPod("other", "demo", "node-y", corev1.PodRunning)
	// nodeName 指向该节点但被 SyncRejected、从未在宿主存在的 Pod：不是「在节点上」，不能删
	rejected := virtualPod("rejected", "demo", "node-x", corev1.PodPending)
	dr := &deleteRecorder{grace: map[string]*int64{}}
	c := newTestContext(t,
		[]client.Object{web, other, virtualPod("done", "demo", "node-x", corev1.PodSucceeded), deleting, rejected},
		[]client.Object{hostPodFor(web, corev1.PodRunning), hostPodFor(other, corev1.PodRunning)},
		testOpts{deletes: dr})

	if err := c.evictPodsOnNode(context.Background(), "node-x"); err != nil {
		t.Fatalf("evictPodsOnNode: %v", err)
	}

	if podExists(t, c.Virtual, "demo", "web") {
		t.Fatal("running pod on the disallowed node should be deleted")
	}
	for _, keep := range []string{"other", "done", "deleting", "rejected"} {
		if !podExists(t, c.Virtual, "demo", keep) {
			t.Fatalf("pod %s should not be touched", keep)
		}
	}
	if len(dr.grace) != 1 {
		t.Fatalf("expected exactly one delete, got %v", dr.grace)
	}
	if g := dr.grace["demo/web"]; g == nil || *g != names.PlacementEvictionMaxGracePeriodSeconds {
		t.Fatalf("grace period = %v, want %d", g, names.PlacementEvictionMaxGracePeriodSeconds)
	}
	expectEviction(t, c, "node-x")
}

func TestEvictPodsOnNodeSkipsWhenHostPodAlreadyTerminal(t *testing.T) {
	// Job 的 Pod 刚在宿主结束、status 还没回写：删虚拟 Pod 会让 Job 重跑，必须跳过。
	vpod := virtualPod("job", "demo", "node-x", corev1.PodRunning)
	hpod := &corev1.Pod{ObjectMeta: translate.HostObjectMeta(vpod, testHostNS), Status: corev1.PodStatus{Phase: corev1.PodSucceeded}}
	c := newTestContext(t, []client.Object{vpod}, []client.Object{hpod}, testOpts{})

	if err := c.evictPodsOnNode(context.Background(), "node-x"); err != nil {
		t.Fatalf("evictPodsOnNode: %v", err)
	}
	if !podExists(t, c.Virtual, "demo", "job") {
		t.Fatal("pod whose host pod already finished must not be deleted")
	}
	expectNoEvent(t, c)
}

func TestEvictPodRecordsNothingWhenDeleteFindsNothing(t *testing.T) {
	// Node 与 Pod 两个控制器可能竞争驱逐同一个 Pod：第二次 Delete 返回 NotFound，不应再记 Event 与指标。
	web := virtualPod("web", "demo", "node-x", corev1.PodRunning)
	notFound := interceptor.Funcs{
		Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.DeleteOption) error {
			return apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, obj.GetName())
		},
	}
	c := newTestContext(t, []client.Object{web}, []client.Object{hostPodFor(web, corev1.PodRunning)}, testOpts{virtualFuncs: &notFound})
	before := testutil.ToFloat64(PlacementEvictions)

	if err := c.evictPodsOnNode(context.Background(), "node-x"); err != nil {
		t.Fatalf("evictPodsOnNode: %v", err)
	}
	expectNoEvent(t, c)
	if after := testutil.ToFloat64(PlacementEvictions); after != before {
		t.Fatalf("PlacementEvictions changed from %v to %v although nothing was deleted", before, after)
	}
}

func TestNodeReconcilerEvictsPodsWhenNodeNoLongerAllowed(t *testing.T) {
	hostNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-x", Labels: map[string]string{"pool": "b"}}}
	web := virtualPod("web", "demo", "node-x", corev1.PodRunning)
	other := virtualPod("other", "demo", "node-y", corev1.PodRunning)
	c := newTestContext(t,
		[]client.Object{translate.VirtualNode(hostNode), web, other},
		[]client.Object{hostNode, hostPodFor(web, corev1.PodRunning), hostPodFor(other, corev1.PodRunning)}, testOpts{})
	c.Placement.Set(placement.Effective{Static: v1alpha1.NodePlacement{NodeSelector: map[string]string{"pool": "a"}}})

	r := &nodeReconciler{c: c}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "node-x"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if err := c.Virtual.Get(context.Background(), client.ObjectKey{Name: "node-x"}, &corev1.Node{}); !apierrors.IsNotFound(err) {
		t.Fatalf("virtual node mirror should be deleted, got %v", err)
	}
	if podExists(t, c.Virtual, "demo", "web") {
		t.Fatal("pod on the node that is no longer allowed should be deleted")
	}
	if !podExists(t, c.Virtual, "demo", "other") {
		t.Fatal("pod on another node should stay")
	}
	expectEviction(t, c, "node-x")
}

func TestPodReconcilerEvictsPodBoundToDisallowedNode(t *testing.T) {
	// placement 变化前创建、变化后才被宿主调度到被禁节点：虚拟侧尚未 binding，宿主 Pod 已有 nodeName。
	hostNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-x", Labels: map[string]string{"pool": "b"}}}
	vpod := virtualPod("web", "demo", "", corev1.PodPending)
	hpod := &corev1.Pod{
		ObjectMeta: translate.HostObjectMeta(vpod, testHostNS),
		Spec:       corev1.PodSpec{NodeName: "node-x", Containers: vpod.Spec.Containers},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	c := newTestContext(t, []client.Object{vpod}, []client.Object{hostNode, hpod}, testOpts{})
	c.Placement.Set(placement.Effective{Static: v1alpha1.NodePlacement{NodeSelector: map[string]string{"pool": "a"}}})

	r := &podReconciler{c: c}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "demo", Name: "web"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if podExists(t, c.Virtual, "demo", "web") {
		t.Fatal("virtual pod whose host pod landed on a disallowed node should be deleted")
	}
	expectEviction(t, c, "node-x")
}

func TestPodReconcilerDoesNotEvictWhenNodeLookupFails(t *testing.T) {
	// 读宿主 Node 出错（而不是 NotFound）时不能把「不确定」当成「不允许」去做不可逆的删除：返回错误等重试。
	hostNode := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-x", Labels: map[string]string{"pool": "a"}}}
	vpod := virtualPod("web", "demo", "", corev1.PodPending)
	hpod := &corev1.Pod{
		ObjectMeta: translate.HostObjectMeta(vpod, testHostNS),
		Spec:       corev1.PodSpec{NodeName: "node-x", Containers: vpod.Spec.Containers},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	lookupErr := errors.New("host cache unavailable")
	failNodeGet := interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, isNode := obj.(*corev1.Node); isNode {
				return lookupErr
			}
			return c.Get(ctx, key, obj, opts...)
		},
	}
	c := newTestContext(t, []client.Object{vpod}, []client.Object{hostNode, hpod}, testOpts{hostFuncs: &failNodeGet})
	c.Placement.Set(placement.Effective{Static: v1alpha1.NodePlacement{NodeSelector: map[string]string{"pool": "a"}}})

	r := &podReconciler{c: c}
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "demo", Name: "web"}})
	if !errors.Is(err, lookupErr) {
		t.Fatalf("Reconcile should surface the lookup error for retry, got %v", err)
	}
	if !podExists(t, c.Virtual, "demo", "web") {
		t.Fatal("pod must not be deleted when the node lookup failed")
	}
	expectNoEvent(t, c)
}
