package translate

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"vk8s.io/vk8s/internal/names"
)

func TestRuntimeClassMirror(t *testing.T) {
	host := &nodev1.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "nvidia", Labels: map[string]string{"vendor": "nvidia"},
			Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{}", "note": "x"}},
		Handler:    "nvidia",
		Overhead:   &nodev1.Overhead{PodFixed: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m")}},
		Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{"gpu": "true"}},
	}
	v := VirtualRuntimeClass(host)
	if v.Name != "nvidia" || v.Handler != "nvidia" || v.Labels[names.LabelManaged] != "true" || v.Labels["vendor"] != "nvidia" {
		t.Fatalf("mirror: %+v", v)
	}
	if _, ok := v.Annotations["kubectl.kubernetes.io/last-applied-configuration"]; ok || v.Annotations["note"] != "x" {
		t.Fatalf("annotations: %v", v.Annotations)
	}
	if v.Scheduling == nil || v.Scheduling.NodeSelector["gpu"] != "true" || v.Overhead == nil {
		t.Fatalf("scheduling/overhead not copied: %+v", v)
	}

	actual := VirtualRuntimeClass(host)
	if changed, recreate := ApplyRuntimeClassMutations(v, actual); changed || recreate {
		t.Fatalf("identical objects must be a no-op: %v %v", changed, recreate)
	}
	host.Scheduling.NodeSelector["gpu"] = "false"
	changed, recreate := ApplyRuntimeClassMutations(VirtualRuntimeClass(host), actual)
	if !changed || recreate || actual.Scheduling.NodeSelector["gpu"] != "false" {
		t.Fatalf("scheduling change must update in place: %v %v %+v", changed, recreate, actual.Scheduling)
	}
	host.Handler = "runc"
	if _, recreate := ApplyRuntimeClassMutations(VirtualRuntimeClass(host), actual); !recreate {
		t.Fatalf("handler change must recreate")
	}
}

func TestLimitRangeMirror(t *testing.T) {
	host := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: names.LimitRangeName, Namespace: "vc-e2e", Labels: map[string]string{"vk8s.io/vcluster": "e2e"}},
		Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
			Type:           corev1.LimitTypeContainer,
			DefaultRequest: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
			Default:        corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
		}}},
	}
	v := VirtualLimitRange(host, "demo")
	if v.Namespace != "demo" || v.Name != names.LimitRangeName || v.Labels[names.LabelManaged] != "true" {
		t.Fatalf("mirror: %+v", v.ObjectMeta)
	}
	if _, ok := v.Labels["vk8s.io/vcluster"]; ok {
		t.Fatalf("host-side labels must not leak into the virtual cluster")
	}
	if !v.Spec.Limits[0].DefaultRequest.Cpu().Equal(resource.MustParse("100m")) {
		t.Fatalf("spec: %+v", v.Spec)
	}
	actual := VirtualLimitRange(host, "demo")
	if ApplyLimitRangeMutations(v, actual) {
		t.Fatalf("identical must be no-op")
	}
	host.Spec.Limits[0].Default[corev1.ResourceCPU] = resource.MustParse("2")
	if !ApplyLimitRangeMutations(VirtualLimitRange(host, "demo"), actual) || !actual.Spec.Limits[0].Default.Cpu().Equal(resource.MustParse("2")) {
		t.Fatalf("spec change not applied: %+v", actual.Spec)
	}
	// 用户改了 label 也会被补回
	delete(actual.Labels, names.LabelManaged)
	if !ApplyLimitRangeMutations(VirtualLimitRange(host, "demo"), actual) || actual.Labels[names.LabelManaged] != "true" {
		t.Fatalf("managed label must be restored")
	}
}
