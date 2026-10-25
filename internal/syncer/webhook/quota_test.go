package webhook

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/testutil"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/translate"
)

const testHostNS = "vc-q"

// rl 由 "name","qty" 对构造 ResourceList。
func rl(kv ...string) corev1.ResourceList {
	out := corev1.ResourceList{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[corev1.ResourceName(kv[i])] = resource.MustParse(kv[i+1])
	}
	return out
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// newQuotaFixture 宿主 fake client 预置 RQ vk8s-quota（hard/used，overhead 非 nil 时写注解）与 hostObjs；虚拟 fake client 预置 virtualObjs。
func newQuotaFixture(t *testing.T, hard, used corev1.ResourceList, overhead *corev1.ResourceList, hostObjs, virtualObjs []client.Object) *QuotaValidator {
	t.Helper()
	s := testScheme(t)
	rq := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Namespace: testHostNS, Name: names.HostResourceQuotaName},
		Spec:       corev1.ResourceQuotaSpec{Hard: hard},
		Status:     corev1.ResourceQuotaStatus{Hard: hard, Used: used},
	}
	if overhead != nil {
		b, _ := json.Marshal(*overhead)
		rq.Annotations = map[string]string{names.AnnotationQuotaOverhead: string(b)}
	}
	host := fake.NewClientBuilder().WithScheme(s).WithObjects(append([]client.Object{rq}, hostObjs...)...).Build()
	virtual := fake.NewClientBuilder().WithScheme(s).WithObjects(virtualObjs...).Build()
	return &QuotaValidator{Log: logr.Discard(), Host: host, Virtual: virtual, HostNamespace: testHostNS, Clock: clocktesting.NewFakeClock(time.Now())}
}

// podWith 单容器 Pod，requests.cpu=cpu（可为空），extra 为额外 requests。
func podWith(ns, name, cpu string, phase corev1.PodPhase, extra ...string) *corev1.Pod {
	req := corev1.ResourceList{}
	if cpu != "" {
		req[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		req[corev1.ResourceName(extra[i])] = resource.MustParse(extra[i+1])
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "x", Resources: corev1.ResourceRequirements{Requests: req}}}},
		Status:     corev1.PodStatus{Phase: phase},
	}
}

func pvcWith(ns, name, storage string) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: rl("storage", storage)},
		},
	}
}

func mustDeny(t *testing.T, v *QuotaValidator, obj, old client.Object, wantMsg string) *Decision {
	t.Helper()
	d, err := v.Evaluate(context.Background(), obj, old)
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if d == nil {
		t.Fatalf("expected denial %q, got allow", wantMsg)
	}
	if d.Message != wantMsg {
		t.Fatalf("message\n got: %s\nwant: %s", d.Message, wantMsg)
	}
	return d
}

func mustAllow(t *testing.T, v *QuotaValidator, obj, old client.Object) {
	t.Helper()
	d, err := v.Evaluate(context.Background(), obj, old)
	if err != nil {
		t.Fatalf("Evaluate error: %v", err)
	}
	if d != nil {
		t.Fatalf("expected allow, got denial: %s", d.Message)
	}
}

func TestEvaluateDeniesCPUWithUserFacingNumbers(t *testing.T) {
	ov := rl("requests.cpu", "300m")
	v := newQuotaFixture(t, rl("requests.cpu", "16300m"), rl("requests.cpu", "15300m"), &ov, nil, nil)
	d := mustDeny(t, v, podWith("default", "p", "2", corev1.PodPending), nil,
		"exceeded quota: vk8s-quota, requested: requests.cpu=2, used: requests.cpu=15, limited: requests.cpu=16")
	if len(d.Exceeded) != 1 || d.Exceeded[0] != corev1.ResourceRequestsCPU {
		t.Fatalf("exceeded = %v", d.Exceeded)
	}
}

func TestEvaluateDeniesPodCountAndFormatsMultipleResources(t *testing.T) {
	ov := rl("pods", "2", "requests.cpu", "300m")
	v := newQuotaFixture(t, rl("pods", "52", "requests.cpu", "16300m"), rl("pods", "52", "requests.cpu", "15300m"), &ov, nil, nil)
	d := mustDeny(t, v, podWith("default", "p", "2", corev1.PodPending), nil,
		"exceeded quota: vk8s-quota, requested: pods=1,requests.cpu=2, used: pods=50,requests.cpu=15, limited: pods=50,requests.cpu=16")
	if len(d.Exceeded) != 2 {
		t.Fatalf("exceeded = %v", d.Exceeded)
	}
}

func TestEvaluateAllowsWhenWithinQuota(t *testing.T) {
	ov := rl("requests.cpu", "300m")
	v := newQuotaFixture(t, rl("requests.cpu", "16300m"), rl("requests.cpu", "14300m"), &ov, nil, nil)
	mustAllow(t, v, podWith("default", "p", "2", corev1.PodPending), nil)
}

func TestEvaluateIgnoresDimensionsAbsentFromHard(t *testing.T) {
	v := newQuotaFixture(t, rl("pods", "52"), rl("pods", "10"), nil, nil, nil)
	mustAllow(t, v, podWith("default", "p", "", corev1.PodPending, "nvidia.com/gpu", "8"), nil)

	v = newQuotaFixture(t, rl("requests.nvidia.com/gpu", "4"), rl("requests.nvidia.com/gpu", "4"), nil, nil, nil)
	mustDeny(t, v, podWith("default", "p", "", corev1.PodPending, "nvidia.com/gpu", "1"), nil,
		"exceeded quota: vk8s-quota, requested: requests.nvidia.com/gpu=1, used: requests.nvidia.com/gpu=4, limited: requests.nvidia.com/gpu=4")
}

func TestEvaluateCountsInflightVirtualObjects(t *testing.T) {
	ov := rl("requests.cpu", "300m")
	inflight := []client.Object{
		podWith("a", "x1", "1", corev1.PodPending),
		podWith("a", "x2", "1", corev1.PodPending),
		podWith("b", "x3", "1", corev1.PodRunning),
	}
	v := newQuotaFixture(t, rl("requests.cpu", "16300m"), rl("requests.cpu", "12300m"), &ov, nil, inflight)
	mustDeny(t, v, podWith("default", "p", "2", corev1.PodPending), nil,
		"exceeded quota: vk8s-quota, requested: requests.cpu=2, used: requests.cpu=15, limited: requests.cpu=16")

	// 其中一个在宿主上已存在：不再算在途
	onHost := podWith(testHostNS, translate.HostName("x3", "b"), "1", corev1.PodRunning)
	v = newQuotaFixture(t, rl("requests.cpu", "16300m"), rl("requests.cpu", "12300m"), &ov, []client.Object{onHost}, inflight)
	mustAllow(t, v, podWith("default", "p", "2", corev1.PodPending), nil)
}

func TestEvaluateIgnoresTerminalInflightPods(t *testing.T) {
	ov := rl("requests.cpu", "300m")
	inflight := []client.Object{
		podWith("a", "x1", "1", corev1.PodPending),
		podWith("a", "x2", "1", corev1.PodSucceeded),
		podWith("b", "x3", "1", corev1.PodFailed),
	}
	v := newQuotaFixture(t, rl("requests.cpu", "16300m"), rl("requests.cpu", "12300m"), &ov, nil, inflight)
	mustAllow(t, v, podWith("default", "p", "2", corev1.PodPending), nil)
}

func TestEvaluatePVCCreateAndExpansion(t *testing.T) {
	ov := rl("requests.storage", "5Gi")
	v := newQuotaFixture(t, rl("requests.storage", "205Gi"), rl("requests.storage", "200Gi"), &ov, nil, nil)
	mustDeny(t, v, pvcWith("default", "d", "10Gi"), nil,
		"exceeded quota: vk8s-quota, requested: requests.storage=10Gi, used: requests.storage=195Gi, limited: requests.storage=200Gi")

	// 扩容只计增量：190+10 ≤ 205 放行；196+10 > 205 拒绝
	v = newQuotaFixture(t, rl("requests.storage", "205Gi"), rl("requests.storage", "190Gi"), &ov, nil, nil)
	mustAllow(t, v, pvcWith("default", "d", "20Gi"), pvcWith("default", "d", "10Gi"))
	v = newQuotaFixture(t, rl("requests.storage", "205Gi"), rl("requests.storage", "196Gi"), &ov, nil, nil)
	mustDeny(t, v, pvcWith("default", "d", "20Gi"), pvcWith("default", "d", "10Gi"),
		"exceeded quota: vk8s-quota, requested: requests.storage=10Gi, used: requests.storage=191Gi, limited: requests.storage=200Gi")

	// 缩容、只改 label：放行
	v = newQuotaFixture(t, rl("requests.storage", "205Gi"), rl("requests.storage", "205Gi"), &ov, nil, nil)
	mustAllow(t, v, pvcWith("default", "d", "10Gi"), pvcWith("default", "d", "20Gi"))
	same := pvcWith("default", "d", "10Gi")
	same.Labels = map[string]string{"a": "b"}
	mustAllow(t, v, same, pvcWith("default", "d", "10Gi"))

	// 计数维度
	v = newQuotaFixture(t, rl("persistentvolumeclaims", "3"), rl("persistentvolumeclaims", "3"), nil, nil, nil)
	mustDeny(t, v, pvcWith("default", "d", "1Gi"), nil,
		"exceeded quota: vk8s-quota, requested: persistentvolumeclaims=1, used: persistentvolumeclaims=3, limited: persistentvolumeclaims=3")
}

func TestEvaluateAllowsWhenQuotaMissing(t *testing.T) {
	s := testScheme(t)
	v := &QuotaValidator{Log: logr.Discard(), Host: fake.NewClientBuilder().WithScheme(s).Build(), Virtual: fake.NewClientBuilder().WithScheme(s).Build(),
		HostNamespace: testHostNS, Clock: clocktesting.NewFakeClock(time.Now())}
	before := testutil.ToFloat64(QuotaSkipped.WithLabelValues("no_quota"))
	d, err := v.Evaluate(context.Background(), podWith("default", "p", "2", corev1.PodPending), nil)
	if err == nil || d != nil {
		t.Fatalf("expected (nil, err), got (%v, %v)", d, err)
	}
	if got := testutil.ToFloat64(QuotaSkipped.WithLabelValues("no_quota")) - before; got != 1 {
		t.Fatalf("quota_skipped{no_quota} += %v, want 1", got)
	}
}

func TestEvaluateWithoutOverheadAnnotationUsesHostNumbers(t *testing.T) {
	v := newQuotaFixture(t, rl("requests.cpu", "16300m"), rl("requests.cpu", "15300m"), nil, nil, nil)
	mustDeny(t, v, podWith("default", "p", "2", corev1.PodPending), nil,
		"exceeded quota: vk8s-quota, requested: requests.cpu=2, used: requests.cpu=15300m, limited: requests.cpu=16300m")

	bad := newQuotaFixture(t, rl("requests.cpu", "16300m"), rl("requests.cpu", "15300m"), nil, nil, nil)
	rq := &corev1.ResourceQuota{}
	if err := bad.Host.Get(context.Background(), client.ObjectKey{Namespace: testHostNS, Name: names.HostResourceQuotaName}, rq); err != nil {
		t.Fatal(err)
	}
	rq.Annotations = map[string]string{names.AnnotationQuotaOverhead: "not json"}
	if err := bad.Host.(client.Client).Update(context.Background(), rq); err != nil {
		t.Fatal(err)
	}
	mustDeny(t, bad, podWith("default", "p", "2", corev1.PodPending), nil,
		"exceeded quota: vk8s-quota, requested: requests.cpu=2, used: requests.cpu=15300m, limited: requests.cpu=16300m")
}

func TestQuotaMessageSortsAndJoinsLikeApiserver(t *testing.T) {
	got := quotaMessage("vk8s-quota", rl("requests.memory", "1Gi", "pods", "1"), rl("pods", "50", "requests.memory", "63Gi"), rl("pods", "50", "requests.memory", "64Gi"))
	want := "exceeded quota: vk8s-quota, requested: pods=1,requests.memory=1Gi, used: pods=50,requests.memory=63Gi, limited: pods=50,requests.memory=64Gi"
	if got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
	if strings.Contains(got, ", ,") {
		t.Fatal("empty parts")
	}
}

// ---- Handle ----

func admissionReq(t *testing.T, op admissionv1.Operation, kind string, obj, old client.Object, dryRun bool) admission.Request {
	t.Helper()
	req := admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{Operation: op, Kind: metav1.GroupVersionKind{Version: "v1", Kind: kind}, DryRun: &dryRun}}
	if obj != nil {
		raw, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		req.Object.Raw = raw
		req.Namespace = obj.GetNamespace()
		req.Name = obj.GetName()
	}
	if old != nil {
		raw, err := json.Marshal(old)
		if err != nil {
			t.Fatal(err)
		}
		req.OldObject.Raw = raw
	}
	return req
}

func TestHandleDeniesWithForbiddenAndMessage(t *testing.T) {
	ov := rl("requests.cpu", "300m")
	v := newQuotaFixture(t, rl("requests.cpu", "16300m"), rl("requests.cpu", "15300m"), &ov, nil, nil)
	resp := v.Handle(context.Background(), admissionReq(t, admissionv1.Create, "Pod", podWith("default", "p", "2", corev1.PodPending), nil, false))
	if resp.Allowed || resp.Result == nil || resp.Result.Code != 403 || resp.Result.Reason != metav1.StatusReasonForbidden {
		t.Fatalf("response = %+v", resp)
	}
	if want := "exceeded quota: vk8s-quota, requested: requests.cpu=2, used: requests.cpu=15, limited: requests.cpu=16"; resp.Result.Message != want {
		t.Fatalf("message\n got: %s\nwant: %s", resp.Result.Message, want)
	}
}

func TestHandleAllowsOnDecodeError(t *testing.T) {
	v := newQuotaFixture(t, rl("requests.cpu", "16300m"), rl("requests.cpu", "15300m"), nil, nil, nil)
	req := admissionReq(t, admissionv1.Create, "Pod", podWith("default", "p", "2", corev1.PodPending), nil, false)
	req.Object.Raw = []byte("{not json")
	before := testutil.ToFloat64(QuotaSkipped.WithLabelValues("error"))
	if resp := v.Handle(context.Background(), req); !resp.Allowed {
		t.Fatalf("decode error must allow, got %+v", resp.Result)
	}
	if got := testutil.ToFloat64(QuotaSkipped.WithLabelValues("error")) - before; got != 1 {
		t.Fatalf("quota_skipped{error} += %v, want 1", got)
	}
}

func TestHandleAllowsWhenQuotaMissing(t *testing.T) {
	s := testScheme(t)
	v := &QuotaValidator{Log: logr.Discard(), Host: fake.NewClientBuilder().WithScheme(s).Build(), Virtual: fake.NewClientBuilder().WithScheme(s).Build(),
		HostNamespace: testHostNS, Clock: clocktesting.NewFakeClock(time.Now())}
	before := testutil.ToFloat64(QuotaSkipped.WithLabelValues("no_quota"))
	if resp := v.Handle(context.Background(), admissionReq(t, admissionv1.Create, "Pod", podWith("default", "p", "2", corev1.PodPending), nil, false)); !resp.Allowed {
		t.Fatalf("missing quota must allow, got %+v", resp.Result)
	}
	if got := testutil.ToFloat64(QuotaSkipped.WithLabelValues("no_quota")) - before; got != 1 {
		t.Fatalf("quota_skipped{no_quota} += %v, want 1", got)
	}
}

func TestHandleDryRunIsStillDenied(t *testing.T) {
	ov := rl("requests.cpu", "300m")
	v := newQuotaFixture(t, rl("requests.cpu", "16300m"), rl("requests.cpu", "15300m"), &ov, nil, nil)
	resp := v.Handle(context.Background(), admissionReq(t, admissionv1.Create, "Pod", podWith("default", "p", "2", corev1.PodPending), nil, true))
	if resp.Allowed || resp.Result.Code != 403 {
		t.Fatalf("dry-run must get the same verdict, got %+v", resp)
	}
}

func TestHandleUpdateDecodesOldObject(t *testing.T) {
	ov := rl("requests.storage", "5Gi")
	v := newQuotaFixture(t, rl("requests.storage", "205Gi"), rl("requests.storage", "196Gi"), &ov, nil, nil)
	resp := v.Handle(context.Background(), admissionReq(t, admissionv1.Update, "PersistentVolumeClaim", pvcWith("default", "d", "20Gi"), pvcWith("default", "d", "10Gi"), false))
	if resp.Allowed || resp.Result.Code != 403 {
		t.Fatalf("expansion beyond quota must be denied, got %+v", resp)
	}
	same := pvcWith("default", "d", "10Gi")
	same.Labels = map[string]string{"a": "b"}
	if resp := v.Handle(context.Background(), admissionReq(t, admissionv1.Update, "PersistentVolumeClaim", same, pvcWith("default", "d", "10Gi"), false)); !resp.Allowed {
		t.Fatalf("label-only update must be allowed, got %+v", resp.Result)
	}
}

// ---- 审查修复 ----

// 原生插件只把本次请求涉及的维度与 hard 比较：别的维度已经超限（管理员调低配额）不影响无关对象的创建。
func TestEvaluateIgnoresUnrelatedOverHardDimensions(t *testing.T) {
	v := newQuotaFixture(t, rl("pods", "52", "requests.storage", "205Gi"), rl("pods", "60", "requests.storage", "10Gi"), nil, nil, nil)
	mustAllow(t, v, pvcWith("default", "d", "1Gi"), nil)

	v = newQuotaFixture(t, rl("requests.memory", "64Gi", "pods", "52"), rl("requests.memory", "70Gi", "pods", "10"), nil, nil, nil)
	mustAllow(t, v, podWith("default", "p", "1", corev1.PodPending), nil)
}

// 同一批并发创建（ReplicaSet 的 slowStartBatch）在落库前互相看不见：webhook 记住自己刚放行的 CREATE，
// 直到它出现在虚拟缓存里或超过 TTL。
func TestEvaluateRemembersRecentlyAdmittedCreates(t *testing.T) {
	ov := rl("requests.cpu", "300m")
	v := newQuotaFixture(t, rl("requests.cpu", "16300m"), rl("requests.cpu", "14300m"), &ov, nil, nil)
	fc := clocktesting.NewFakeClock(time.Now())
	v.Clock = fc
	p1, p2, p3 := podWith("a", "p1", "1", corev1.PodPending), podWith("a", "p2", "1", corev1.PodPending), podWith("a", "p3", "1", corev1.PodPending)
	mustAllow(t, v, p1, nil)
	v.RecordAdmitted(p1)
	mustAllow(t, v, p2, nil) // 14.3 + 1(p1) + 1 = 16.3
	v.RecordAdmitted(p2)
	mustDeny(t, v, p3, nil, "exceeded quota: vk8s-quota, requested: requests.cpu=1, used: requests.cpu=16, limited: requests.cpu=16")

	// p1 落库（虚拟缓存里有了、宿主还没有）：只能算一次，不能既是在途又是账本
	if err := v.Virtual.(client.Client).Create(context.Background(), p1.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	mustDeny(t, v, p3, nil, "exceeded quota: vk8s-quota, requested: requests.cpu=1, used: requests.cpu=16, limited: requests.cpu=16")

	// TTL 过后账本条目失效（p2 从未落库，例如被别的 webhook 拒了）
	fc.Step(admittedTTL + time.Second)
	mustAllow(t, v, p3, nil) // 14.3 + 1(p1 在途) + 1 = 16.3
}

// hangingReader 模拟 informer 永远同步不了：Get / List 阻塞到 ctx 结束。
type hangingReader struct{ client.Reader }

func (hangingReader) Get(ctx context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	<-ctx.Done()
	return ctx.Err()
}
func (hangingReader) List(ctx context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	<-ctx.Done()
	return ctx.Err()
}

// 缓存读不出来（RBAC 缺失、informer 没同步）时不能让每个创建都等到 apiserver 的 5 秒超时：1 秒内放行并计 skipped{error}。
func TestHandleAllowsQuicklyWhenCacheReadHangs(t *testing.T) {
	s := testScheme(t)
	v := &QuotaValidator{Log: logr.Discard(), Host: hangingReader{}, Virtual: fake.NewClientBuilder().WithScheme(s).Build(),
		HostNamespace: testHostNS, Clock: clocktesting.NewFakeClock(time.Now())}
	before := testutil.ToFloat64(QuotaSkipped.WithLabelValues("error"))
	start := time.Now()
	resp := v.Handle(context.Background(), admissionReq(t, admissionv1.Create, "Pod", podWith("default", "p", "2", corev1.PodPending), nil, false))
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("Handle took %v, must give up within the read timeout", el)
	}
	if !resp.Allowed {
		t.Fatalf("hanging cache must allow, got %+v", resp.Result)
	}
	if got := testutil.ToFloat64(QuotaSkipped.WithLabelValues("error")) - before; got != 1 {
		t.Fatalf("quota_skipped{error} += %v, want 1", got)
	}
}
