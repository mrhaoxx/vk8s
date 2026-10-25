package controllers

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/names"
)

// newResourcesReconciler：fake client + 显式的控制平面默认 requests / storageSize（testConfig 只设 SystemNamespace）。
func newResourcesReconciler(t *testing.T) (*VirtualClusterReconciler, *v1alpha1.VirtualCluster) {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.Defaults.ControlPlane.Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("200m"),
		corev1.ResourceMemory: resource.MustParse("512Mi"),
	}
	cfg.Defaults.ControlPlane.StorageSize = resource.MustParse("5Gi")
	r := &VirtualClusterReconciler{Client: fake.NewClientBuilder().WithScheme(s).Build(), Scheme: s, Config: cfg}
	vc := &v1alpha1.VirtualCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "q", UID: types.UID("uid-q")},
		Spec: v1alpha1.VirtualClusterSpec{Quota: v1alpha1.Quota{
			CPU: resource.MustParse("16"), Memory: resource.MustParse("64Gi"), Pods: 50,
			Storage: resource.MustParse("200Gi"), Services: 20,
			Extended: map[string]resource.Quantity{"nvidia.com/gpu": resource.MustParse("4")},
		}},
	}
	return r, vc
}

func mustEqualQty(t *testing.T, what string, got corev1.ResourceList, name corev1.ResourceName, want string) {
	t.Helper()
	q, ok := got[name]
	if !ok {
		t.Fatalf("%s: %s missing in %v", what, name, got)
	}
	if q.Cmp(resource.MustParse(want)) != 0 {
		t.Fatalf("%s: %s = %s, want %s", what, name, q.String(), want)
	}
}

func TestEnsureResourceQuotaWritesOverheadAnnotation(t *testing.T) {
	r, vc := newResourcesReconciler(t)
	if err := r.ensureResourceQuota(context.Background(), vc); err != nil {
		t.Fatal(err)
	}
	rq := &corev1.ResourceQuota{}
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: names.HostNamespace("q"), Name: names.HostResourceQuotaName}, rq); err != nil {
		t.Fatal(err)
	}
	var overhead corev1.ResourceList
	if err := json.Unmarshal([]byte(rq.Annotations[names.AnnotationQuotaOverhead]), &overhead); err != nil {
		t.Fatalf("annotation %q: %v", rq.Annotations[names.AnnotationQuotaOverhead], err)
	}
	want := r.quotaOverhead(vc)
	if len(overhead) != len(want) {
		t.Fatalf("annotation %v != quotaOverhead %v", overhead, want)
	}
	for name, q := range want {
		mustEqualQty(t, "annotation", overhead, name, q.String())
	}
	mustEqualQty(t, "overhead", overhead, corev1.ResourceRequestsCPU, "300m")
	mustEqualQty(t, "overhead", overhead, corev1.ResourceRequestsMemory, "640Mi")
	mustEqualQty(t, "overhead", overhead, corev1.ResourcePods, "2")
	mustEqualQty(t, "overhead", overhead, corev1.ResourceServices, "1")
	mustEqualQty(t, "overhead", overhead, corev1.ResourceRequestsStorage, "5Gi")
	mustEqualQty(t, "hard", rq.Spec.Hard, corev1.ResourceRequestsCPU, "16300m")
	mustEqualQty(t, "hard", rq.Spec.Hard, corev1.ResourcePods, "52")
	mustEqualQty(t, "hard", rq.Spec.Hard, corev1.ResourceServices, "21")
	mustEqualQty(t, "hard", rq.Spec.Hard, corev1.ResourceRequestsStorage, "205Gi")
	mustEqualQty(t, "hard", rq.Spec.Hard, corev1.ResourceName("requests.nvidia.com/gpu"), "4")
}

func TestQuotaOverheadOmitsZeroDimensions(t *testing.T) {
	r, vc := newResourcesReconciler(t)
	r.Config.Defaults.ControlPlane.StorageSize = resource.Quantity{}
	overhead := r.quotaOverhead(vc)
	if _, ok := overhead[corev1.ResourceRequestsStorage]; ok {
		t.Fatalf("storage overhead must be omitted when storageSize is zero: %v", overhead)
	}
	if _, ok := overhead[corev1.ResourceName("requests.nvidia.com/gpu")]; ok {
		t.Fatalf("extended resources never carry overhead: %v", overhead)
	}
}

func TestSyncerRoleCanReadResourceQuotas(t *testing.T) {
	r, vc := newResourcesReconciler(t)
	if err := r.ensureRBAC(context.Background(), vc); err != nil {
		t.Fatal(err)
	}
	roles := &rbacv1.RoleList{}
	if err := r.List(context.Background(), roles, client.InNamespace(names.HostNamespace("q"))); err != nil {
		t.Fatal(err)
	}
	for _, role := range roles.Items {
		for _, rule := range role.Rules {
			for _, res := range rule.Resources {
				if res == "resourcequotas" && len(rule.APIGroups) == 1 && rule.APIGroups[0] == "" {
					if got := rule.Verbs; len(got) != 3 || got[0] != "get" || got[1] != "list" || got[2] != "watch" {
						t.Fatalf("resourcequotas verbs = %v, want get/list/watch", got)
					}
					return
				}
			}
		}
	}
	t.Fatalf("no Role in %s grants resourcequotas read", names.HostNamespace("q"))
}
