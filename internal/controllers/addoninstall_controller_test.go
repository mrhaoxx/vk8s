package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/addons/runner"
	"vk8s.io/vk8s/internal/config"
	"vk8s.io/vk8s/internal/names"
)

type staticClients struct{ c client.Client }

func (s staticClients) For(context.Context, string) (client.Client, error) { return s.c, nil }

type addonFixture struct {
	t       *testing.T
	r       *AddonInstallReconciler
	host    client.Client
	virtual client.Client
	n       int
}

func addonTemplate() *v1alpha1.AddonTemplate {
	return &v1alpha1.AddonTemplate{ObjectMeta: metav1.ObjectMeta{Name: "devpods"}, Spec: v1alpha1.AddonTemplateSpec{
		DisplayName: "DevPods",
		Chart:       v1alpha1.AddonChart{Repository: "oci://reg/charts", Name: "devpod", Version: "0.1.0"},
		Namespace:   "devpod-system", Release: "devpod",
		Inputs:   []v1alpha1.AddonInput{{Name: "adminPassword", Type: "password", Path: "admin.password"}},
		Requires: v1alpha1.AddonRequires{PodSecurity: v1alpha1.PodSecurityPrivileged},
		Generate: []v1alpha1.AddonGenerate{{Secret: v1alpha1.NamespacedName{Namespace: "devpod-system", Name: "devpod-gateway-host-key"}, Kind: "sshKeyPair"}},
		Outputs:  []v1alpha1.AddonOutput{{Name: "ssh", Label: "SSH", Kind: "service", Service: &v1alpha1.AddonServiceRef{Namespace: "devpod-system", Name: "devpod-gateway", Port: 22}}},
	}}
}

func newAddonFixture(t *testing.T, phase v1alpha1.VirtualClusterPhase, ps v1alpha1.PodSecurityLevel, withTemplate bool, inputs map[string]string) *addonFixture {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	vc := &v1alpha1.VirtualCluster{ObjectMeta: metav1.ObjectMeta{Name: "alice-dev", UID: "vc-uid"},
		Spec: v1alpha1.VirtualClusterSpec{Owner: "alice", PodSecurity: ps}, Status: v1alpha1.VirtualClusterStatus{Phase: phase, AppsDomain: "apps.example"}}
	in := &v1alpha1.AddonInstall{ObjectMeta: metav1.ObjectMeta{Name: "alice-dev-devpods", UID: "in-uid", Generation: 1},
		Spec: v1alpha1.AddonInstallSpec{VCluster: "alice-dev", Template: "devpods", Version: "0.1.0", Inputs: inputs}}
	objs := []client.Object{vc, in}
	if withTemplate {
		objs = append(objs, addonTemplate())
	}
	host := fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.AddonInstall{}, &v1alpha1.VirtualCluster{}, &batchv1.Job{}).Build()
	virtual := fake.NewClientBuilder().WithScheme(s).Build()
	cfg := &config.Config{SystemNamespace: "vk8s-system", Image: "img:1"}
	cfg.Addons.JobTimeout.Duration = 10 * time.Minute
	f := &addonFixture{t: t, host: host, virtual: virtual}
	now := time.Now()
	f.r = &AddonInstallReconciler{Client: host, Scheme: s, Config: cfg, Clients: staticClients{virtual},
		Now: func() time.Time { return now }, Suffix: func() string { f.n++; return fmt.Sprintf("s%04d", f.n) }}
	return f
}

func (f *addonFixture) reconcile() ctrl.Result {
	f.t.Helper()
	res, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "alice-dev-devpods"}})
	if err != nil {
		f.t.Fatalf("reconcile: %v", err)
	}
	return res
}

func (f *addonFixture) get() *v1alpha1.AddonInstall {
	f.t.Helper()
	in := &v1alpha1.AddonInstall{}
	if err := f.host.Get(context.Background(), client.ObjectKey{Name: "alice-dev-devpods"}, in); err != nil {
		f.t.Fatalf("get install: %v", err)
	}
	return in
}

func (f *addonFixture) jobs() []batchv1.Job {
	l := &batchv1.JobList{}
	_ = f.host.List(context.Background(), l, client.InNamespace("vk8s-system"))
	return l.Items
}

func (f *addonFixture) spec() runner.RunSpec {
	cm := &corev1.ConfigMap{}
	if err := f.host.Get(context.Background(), client.ObjectKey{Namespace: "vk8s-system", Name: "addon-alice-dev-devpods-spec"}, cm); err != nil {
		f.t.Fatalf("spec configmap: %v", err)
	}
	var s runner.RunSpec
	_ = json.Unmarshal([]byte(cm.Data["spec.json"]), &s)
	return s
}

// finishJob 把当前 Job 置为完成 / 失败并建一个带 termination message 的 Pod。
func (f *addonFixture) finishJob(ok bool, reason string, msg runner.Result) {
	f.t.Helper()
	ctx := context.Background()
	name := f.get().Status.Job
	j := &batchv1.Job{}
	if err := f.host.Get(ctx, client.ObjectKey{Namespace: "vk8s-system", Name: name}, j); err != nil {
		f.t.Fatalf("job %s: %v", name, err)
	}
	cond := batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, Reason: reason, LastTransitionTime: metav1.Now()}
	if !ok {
		cond.Type = batchv1.JobFailed
	}
	j.Status.Conditions = append(j.Status.Conditions, cond)
	j.Status.CompletionTime = &metav1.Time{Time: f.r.Now()}
	if err := f.host.Status().Update(ctx, j); err != nil {
		f.t.Fatal(err)
	}
	b, _ := json.Marshal(msg)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "vk8s-system", Name: name + "-pod", Labels: map[string]string{"job-name": name}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "addon", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Message: string(b)}}}}}}
	if err := f.host.Create(ctx, pod); err != nil {
		f.t.Fatal(err)
	}
}

func setVCPhase(f *addonFixture, phase v1alpha1.VirtualClusterPhase) {
	vc := &v1alpha1.VirtualCluster{}
	_ = f.host.Get(context.Background(), client.ObjectKey{Name: "alice-dev"}, vc)
	vc.Status.Phase = phase
	_ = f.host.Status().Update(context.Background(), vc)
}

func TestAddonPendingUntilClusterRunning(t *testing.T) {
	f := newAddonFixture(t, v1alpha1.PhaseProvisioning, v1alpha1.PodSecurityPrivileged, true, nil)
	f.reconcile()
	in := f.get()
	if in.Status.Phase != v1alpha1.AddonPending || !strings.Contains(in.Status.Message, "集群尚未就绪") || len(f.jobs()) != 0 {
		t.Fatalf("status=%+v jobs=%d", in.Status, len(f.jobs()))
	}
	setVCPhase(f, v1alpha1.PhaseRunning)
	f.reconcile()
	if in := f.get(); in.Status.Phase != v1alpha1.AddonInstalling || len(f.jobs()) != 1 {
		t.Fatalf("status=%+v jobs=%d", in.Status, len(f.jobs()))
	}
}

func TestAddonTemplateMissingSelfHeals(t *testing.T) {
	f := newAddonFixture(t, v1alpha1.PhaseRunning, v1alpha1.PodSecurityPrivileged, false, nil)
	f.reconcile()
	if in := f.get(); in.Status.Phase != v1alpha1.AddonFailed || in.Status.Reason != "TemplateMissing" {
		t.Fatalf("status=%+v", in.Status)
	}
	if err := f.host.Create(context.Background(), addonTemplate()); err != nil {
		t.Fatal(err)
	}
	f.reconcile()
	if in := f.get(); in.Status.Phase != v1alpha1.AddonInstalling {
		t.Fatalf("status=%+v", in.Status)
	}
}

func TestAddonRequiresUnmet(t *testing.T) {
	f := newAddonFixture(t, v1alpha1.PhaseRunning, v1alpha1.PodSecurityBaseline, true, nil)
	f.reconcile()
	if in := f.get(); in.Status.Phase != v1alpha1.AddonFailed || in.Status.Reason != "RequiresUnmet" || in.Status.Message != "需要 privileged 档位" || len(f.jobs()) != 0 {
		t.Fatalf("status=%+v", in.Status)
	}
}

func TestAddonInstallHappyPath(t *testing.T) {
	f := newAddonFixture(t, v1alpha1.PhaseRunning, v1alpha1.PodSecurityPrivileged, true, nil)
	f.reconcile()
	ctx := context.Background()
	if err := f.virtual.Get(ctx, client.ObjectKey{Namespace: "devpod-system", Name: "devpod-gateway-host-key"}, &corev1.Secret{}); err != nil {
		t.Fatalf("generated secret: %v", err)
	}
	in := f.get()
	if in.Status.Phase != v1alpha1.AddonInstalling || in.Status.Job == "" || in.Status.Release != "devpod" || in.Status.Namespace != "devpod-system" {
		t.Fatalf("status=%+v", in.Status)
	}
	if len(in.Finalizers) != 1 || in.Finalizers[0] != names.AddonFinalizer {
		t.Fatalf("finalizers=%v", in.Finalizers)
	}
	if s := f.spec(); s.Action != "install" || s.Chart.Version != "0.1.0" {
		t.Fatalf("spec=%+v", s)
	}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "devpod-system", Name: "devpod-gateway"}, Spec: corev1.ServiceSpec{ClusterIP: "172.27.0.9"},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "172.28.0.8"}}}}}
	_ = f.virtual.Create(ctx, svc)
	f.finishJob(true, "", runner.Result{OK: true, Message: "ok"})
	f.reconcile()
	in = f.get()
	if in.Status.Phase != v1alpha1.AddonReady || in.Status.InstalledVersion != "0.1.0" || len(in.Status.Outputs) != 1 || in.Status.Outputs[0].Value != "172.28.0.8:22" {
		t.Fatalf("status=%+v", in.Status)
	}
}

func TestAddonJobFailureAndRetry(t *testing.T) {
	f := newAddonFixture(t, v1alpha1.PhaseRunning, v1alpha1.PodSecurityPrivileged, true, nil)
	f.reconcile()
	first := f.get().Status.Job
	f.finishJob(false, "BackoffLimitExceeded", runner.Result{OK: false, Message: "helm failed", LogTail: "Error: boom"})
	f.reconcile()
	in := f.get()
	if in.Status.Phase != v1alpha1.AddonFailed || in.Status.Reason != "JobFailed" || !strings.Contains(in.Status.Message, "Error: boom") {
		t.Fatalf("status=%+v", in.Status)
	}
	f.reconcile() // 没有重试注解：不再起 Job
	if len(f.jobs()) != 1 {
		t.Fatalf("jobs=%d", len(f.jobs()))
	}
	in.Annotations = map[string]string{names.AnnotationAddonRetry: "2026-10-25T00:00:00Z"}
	if err := f.host.Update(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	f.reconcile()
	in = f.get()
	if in.Status.Phase != v1alpha1.AddonInstalling || in.Status.Job == first || len(f.jobs()) != 2 {
		t.Fatalf("status=%+v jobs=%d", in.Status, len(f.jobs()))
	}
	if _, ok := in.Annotations[names.AnnotationAddonRetry]; ok {
		t.Fatal("retry annotation must be consumed")
	}
}

func TestAddonJobTimeout(t *testing.T) {
	f := newAddonFixture(t, v1alpha1.PhaseRunning, v1alpha1.PodSecurityPrivileged, true, nil)
	f.reconcile()
	f.finishJob(false, "DeadlineExceeded", runner.Result{})
	f.reconcile()
	if in := f.get(); in.Status.Phase != v1alpha1.AddonFailed || in.Status.Reason != "Timeout" {
		t.Fatalf("status=%+v", in.Status)
	}
}

func readyFixture(t *testing.T) *addonFixture {
	f := newAddonFixture(t, v1alpha1.PhaseRunning, v1alpha1.PodSecurityPrivileged, true, nil)
	f.reconcile()
	f.finishJob(true, "", runner.Result{OK: true})
	f.reconcile()
	if st := f.get().Status; st.Phase != v1alpha1.AddonReady {
		t.Fatalf("not ready: %+v", st)
	}
	return f
}

func TestAddonUpgradeOnVersionChange(t *testing.T) {
	f := readyFixture(t)
	in := f.get()
	in.Spec.Version = "0.2.0"
	in.Generation = 2
	_ = f.host.Update(context.Background(), in)
	f.reconcile()
	if in := f.get(); in.Status.Phase != v1alpha1.AddonInstalling || f.spec().Action != "upgrade" || f.spec().Chart.Version != "0.2.0" {
		t.Fatalf("status=%+v spec=%+v", in.Status, f.spec())
	}
}

func TestAddonAvailableVersion(t *testing.T) {
	f := readyFixture(t)
	tpl := &v1alpha1.AddonTemplate{}
	_ = f.host.Get(context.Background(), client.ObjectKey{Name: "devpods"}, tpl)
	tpl.Spec.Chart.Version = "0.2.0"
	_ = f.host.Update(context.Background(), tpl)
	res := f.reconcile()
	in := f.get()
	if in.Status.Phase != v1alpha1.AddonReady || in.Status.AvailableVersion != "0.2.0" || len(f.jobs()) != 1 || res.RequeueAfter != 5*time.Minute {
		t.Fatalf("status=%+v jobs=%d res=%+v", in.Status, len(f.jobs()), res)
	}
}

func deleteInstall(f *addonFixture) {
	in := f.get()
	if err := f.host.Delete(context.Background(), in); err != nil {
		f.t.Fatal(err)
	}
}

func gone(f *addonFixture) bool {
	err := f.host.Get(context.Background(), client.ObjectKey{Name: "alice-dev-devpods"}, &v1alpha1.AddonInstall{})
	return apierrors.IsNotFound(err)
}

func TestAddonUninstallFlow(t *testing.T) {
	f := readyFixture(t)
	deleteInstall(f)
	f.reconcile()
	if in := f.get(); in.Status.Phase != v1alpha1.AddonUninstalling || f.spec().Action != "uninstall" || len(f.jobs()) != 2 {
		t.Fatalf("status=%+v jobs=%d", in.Status, len(f.jobs()))
	}
	f.finishJob(true, "", runner.Result{OK: true})
	f.reconcile()
	if !gone(f) {
		t.Fatal("install must be gone after uninstall")
	}
}

func TestAddonUninstallWithoutTemplate(t *testing.T) {
	f := readyFixture(t)
	_ = f.host.Delete(context.Background(), addonTemplate())
	deleteInstall(f)
	f.reconcile()
	if s := f.spec(); s.Action != "uninstall" || s.Release != "devpod" || s.Namespace != "devpod-system" {
		t.Fatalf("spec=%+v", s)
	}
	f.finishJob(true, "", runner.Result{OK: true})
	f.reconcile()
	if !gone(f) {
		t.Fatal("install must be gone")
	}
}

func TestAddonFinalizerDroppedWhenClusterGone(t *testing.T) {
	f := readyFixture(t)
	vc := &v1alpha1.VirtualCluster{}
	_ = f.host.Get(context.Background(), client.ObjectKey{Name: "alice-dev"}, vc)
	_ = f.host.Delete(context.Background(), vc)
	deleteInstall(f)
	f.reconcile()
	if !gone(f) || len(f.jobs()) != 1 {
		t.Fatalf("gone=%v jobs=%d", gone(f), len(f.jobs()))
	}
}

func TestAddonOneActiveJob(t *testing.T) {
	f := newAddonFixture(t, v1alpha1.PhaseRunning, v1alpha1.PodSecurityPrivileged, true, nil)
	f.reconcile()
	f.reconcile()
	f.reconcile()
	if len(f.jobs()) != 1 {
		t.Fatalf("jobs=%d", len(f.jobs()))
	}
}

func TestAddonStatusNeverStoresPassword(t *testing.T) {
	f := newAddonFixture(t, v1alpha1.PhaseRunning, v1alpha1.PodSecurityPrivileged, true, map[string]string{"adminPassword": "s3cret"})
	f.reconcile()
	f.finishJob(false, "", runner.Result{OK: false, Message: "helm failed"})
	f.reconcile()
	b, _ := json.Marshal(f.get().Status)
	if strings.Contains(string(b), "s3cret") {
		t.Fatalf("status leaks password: %s", b)
	}
	if !strings.Contains(f.spec().Values, "s3cret") {
		t.Fatal("the job spec needs the password")
	}
}

// ---- 审查修复 ----

type failingClients struct{}

func (failingClients) For(context.Context, string) (client.Client, error) {
	return nil, fmt.Errorf("admin kubeconfig not ready")
}

func TestAddonRerunsWhenJobVanishesDuringUpgrade(t *testing.T) {
	f := readyFixture(t)
	in := f.get()
	in.Spec.Version = "0.2.0"
	in.Generation = 2
	_ = f.host.Update(context.Background(), in)
	f.reconcile()
	job := f.get().Status.Job
	_ = f.host.Delete(context.Background(), &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "vk8s-system", Name: job}})
	f.reconcile()
	in = f.get()
	if in.Status.Phase != v1alpha1.AddonInstalling || in.Status.Job == job || in.Status.Job == "" {
		t.Fatalf("vanished upgrade job must be replaced: %+v", in.Status)
	}
}

func TestAddonInvalidTemplateFails(t *testing.T) {
	f := newAddonFixture(t, v1alpha1.PhaseRunning, v1alpha1.PodSecurityPrivileged, true, nil)
	tpl := &v1alpha1.AddonTemplate{}
	_ = f.host.Get(context.Background(), client.ObjectKey{Name: "devpods"}, tpl)
	tpl.Spec.Generate[0].Kind = "rsa"
	_ = f.host.Update(context.Background(), tpl)
	f.reconcile()
	if in := f.get(); in.Status.Phase != v1alpha1.AddonFailed || in.Status.Reason != "TemplateInvalid" || !strings.Contains(in.Status.Message, "rsa") || len(f.jobs()) != 0 {
		t.Fatalf("status=%+v", in.Status)
	}
}

func TestAddonStartErrorSurfacesInStatus(t *testing.T) {
	f := newAddonFixture(t, v1alpha1.PhaseRunning, v1alpha1.PodSecurityPrivileged, true, nil)
	f.r.Clients = failingClients{}
	_, err := f.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Name: "alice-dev-devpods"}})
	if err == nil {
		t.Fatal("transient start error must be returned for retry")
	}
	if in := f.get(); !strings.Contains(in.Status.Message, "admin kubeconfig not ready") {
		t.Fatalf("status must explain the failure: %+v", in.Status)
	}
}

func TestAddonReadyStaysReadyWhenPodSecurityTightened(t *testing.T) {
	f := readyFixture(t)
	vc := &v1alpha1.VirtualCluster{}
	_ = f.host.Get(context.Background(), client.ObjectKey{Name: "alice-dev"}, vc)
	vc.Spec.PodSecurity = v1alpha1.PodSecurityBaseline
	_ = f.host.Update(context.Background(), vc)
	f.reconcile()
	if in := f.get(); in.Status.Phase != v1alpha1.AddonReady {
		t.Fatalf("installed addon must not flip to Failed: %+v", in.Status)
	}
}

func TestAddonUninstallWaitsForRunningCluster(t *testing.T) {
	f := readyFixture(t)
	setVCPhase(f, v1alpha1.PhaseFailed)
	deleteInstall(f)
	f.reconcile()
	if in := f.get(); len(f.jobs()) != 1 || !strings.Contains(in.Status.Message, "集群") {
		t.Fatalf("must not start an uninstall job against a non-running cluster: jobs=%d status=%+v", len(f.jobs()), in.Status)
	}
	setVCPhase(f, v1alpha1.PhaseRunning)
	f.reconcile()
	if len(f.jobs()) != 2 {
		t.Fatalf("uninstall job expected once running, jobs=%d", len(f.jobs()))
	}
}

// 缓存滞后：status.job 还没刷到缓存时，下一轮 reconcile 不能再建一个 Job（真实环境里出现过两个 helm 同时装同一个 release）。
func TestAddonNoSecondJobWhenStatusIsStale(t *testing.T) {
	f := newAddonFixture(t, v1alpha1.PhaseRunning, v1alpha1.PodSecurityPrivileged, true, nil)
	f.reconcile()
	if len(f.jobs()) != 1 {
		t.Fatalf("jobs=%d", len(f.jobs()))
	}
	// 模拟缓存里的旧状态：清掉 status.job 与 phase
	in := f.get()
	in.Status.Job, in.Status.Phase = "", ""
	_ = f.host.Status().Update(context.Background(), in)
	f.reconcile()
	if n := len(f.jobs()); n != 1 {
		t.Fatalf("a running job already exists; got %d jobs", n)
	}
	if in := f.get(); in.Status.Job == "" || in.Status.Phase != v1alpha1.AddonInstalling {
		t.Fatalf("status must re-adopt the running job: %+v", in.Status)
	}
}

// adoptJob 也要恢复 ObservedGeneration：否则成功的那次运行会因 generation 不匹配被当成「Job 不见了」再跑一遍。
func TestAddonAdoptedJobCompletesWithoutRerun(t *testing.T) {
	f := newAddonFixture(t, v1alpha1.PhaseRunning, v1alpha1.PodSecurityPrivileged, true, nil)
	f.reconcile()
	in := f.get()
	in.Status.Job, in.Status.Phase, in.Status.ObservedGeneration = "", "", 0
	_ = f.host.Status().Update(context.Background(), in)
	f.reconcile()
	f.finishJob(true, "", runner.Result{OK: true})
	f.reconcile()
	if in := f.get(); in.Status.Phase != v1alpha1.AddonReady || len(f.jobs()) != 1 {
		t.Fatalf("status=%+v jobs=%d", in.Status, len(f.jobs()))
	}
}

// 同名重建的 AddonInstall 不能认领上一代留下的 Job。
func TestAddonIgnoresJobsOfPreviousIncarnation(t *testing.T) {
	f := newAddonFixture(t, v1alpha1.PhaseRunning, v1alpha1.PodSecurityPrivileged, true, nil)
	old := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "vk8s-system", Name: "addon-alice-dev-devpods-old",
		Labels:          map[string]string{names.LabelAddonInstall: "alice-dev-devpods", "vk8s.io/addon-action": "uninstall"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "vk8s.io/v1alpha1", Kind: "AddonInstall", Name: "alice-dev-devpods", UID: "previous-uid", Controller: ptrTrue()}}}}
	_ = f.host.Create(context.Background(), old)
	f.reconcile()
	if in := f.get(); in.Status.Phase != v1alpha1.AddonInstalling || in.Status.Job == "addon-alice-dev-devpods-old" {
		t.Fatalf("status=%+v", in.Status)
	}
}

func ptrTrue() *bool { b := true; return &b }

// 目录项只改 values（版本不变）：已装实例标「有更新」，用户点升级（重试注解）后重装。
func TestAddonTemplateValuesChangeIsOfferedAsUpdate(t *testing.T) {
	f := readyFixture(t)
	tpl := &v1alpha1.AddonTemplate{}
	_ = f.host.Get(context.Background(), client.ObjectKey{Name: "devpods"}, tpl)
	tpl.Spec.Values = "gateway: {replicas: 3}\n"
	_ = f.host.Update(context.Background(), tpl)
	f.reconcile()
	in := f.get()
	if !in.Status.UpdateAvailable || in.Status.Phase != v1alpha1.AddonReady || len(f.jobs()) != 1 {
		t.Fatalf("values change must be offered, not auto-applied: %+v jobs=%d", in.Status, len(f.jobs()))
	}
	in.Annotations = map[string]string{names.AnnotationAddonRetry: "now"}
	_ = f.host.Update(context.Background(), in)
	f.reconcile()
	if in := f.get(); in.Status.Phase != v1alpha1.AddonInstalling || f.spec().Action != "upgrade" || !strings.Contains(f.spec().Values, "replicas: 3") {
		t.Fatalf("retry on Ready must upgrade with new values: %+v", in.Status)
	}
	f.finishJob(true, "", runner.Result{OK: true})
	f.reconcile()
	if in := f.get(); in.Status.UpdateAvailable {
		t.Fatalf("update flag must clear after applying: %+v", in.Status)
	}
}
