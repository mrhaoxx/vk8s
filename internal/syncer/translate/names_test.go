package translate

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"vk8s.io/vk8s/internal/names"
)

func TestHostName(t *testing.T) {
	long := strings.Repeat("a", 40)
	longNS := strings.Repeat("b", 40)
	cases := []struct {
		name, vname, vns string
		want             string
		truncated        bool
	}{
		{"short", "web", "demo", "web-x-demo", false},
		{"exactly 63", strings.Repeat("a", 56), "demo", strings.Repeat("a", 56) + "-x-demo", false},
		{"truncated", long, longNS, "", true},
		{"truncated trims dash", strings.Repeat("a", 19) + "-" + strings.Repeat("c", 30), longNS, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := HostName(c.vname, c.vns)
			if len(got) > MaxHostNameLength {
				t.Fatalf("HostName(%q,%q)=%q exceeds %d", c.vname, c.vns, got, MaxHostNameLength)
			}
			if !c.truncated {
				if got != c.want {
					t.Fatalf("got %q want %q", got, c.want)
				}
				return
			}
			parts := strings.SplitN(got, hostNameSeparator, 2)
			if len(parts) != 2 || len(parts[0]) > truncatedPartLength || strings.HasSuffix(parts[0], "-") {
				t.Fatalf("unexpected truncated form %q", got)
			}
			if got != HostName(c.vname, c.vns) {
				t.Fatalf("HostName not deterministic")
			}
			if got == HostName(c.vname+"x", c.vns) {
				t.Fatalf("different inputs collide: %q", got)
			}
		})
	}
}

func TestFilterUserKeysDropsPlatformPrefix(t *testing.T) {
	got := FilterUserKeys(map[string]string{"app": "web", names.LabelManaged: "true", "vk8s.io/x": "y"})
	if len(got) != 1 || got["app"] != "web" {
		t.Fatalf("got %v", got)
	}
	if FilterUserKeys(map[string]string{names.LabelVName: "a"}) != nil {
		t.Fatalf("all-platform map should become nil")
	}
}

func TestHostObjectMetaAndMergeMeta(t *testing.T) {
	v := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Name: "cm", Namespace: "demo", UID: "uid-1",
		Labels:      map[string]string{"app": "web", "vk8s.io/evil": "x"},
		Annotations: map[string]string{"note": "1"},
	}}
	meta := HostObjectMeta(v, "vc-e2e")
	if meta.Name != "cm-x-demo" || meta.Namespace != "vc-e2e" {
		t.Fatalf("name/ns: %+v", meta)
	}
	if meta.Labels["app"] != "web" || meta.Labels[names.LabelManaged] != "true" ||
		meta.Labels[names.LabelVName] != "cm" || meta.Labels[names.LabelVNamespace] != "demo" {
		t.Fatalf("labels: %v", meta.Labels)
	}
	if _, ok := meta.Labels["vk8s.io/evil"]; ok {
		t.Fatalf("platform-prefixed user label must be dropped")
	}
	if meta.Annotations[names.AnnotationVUID] != "uid-1" || meta.Annotations[AnnotationSyncedLabels] != "app" {
		t.Fatalf("annotations: %v", meta.Annotations)
	}

	// 宿主对象上已有 CNI 之类的第三方 annotation，用户删掉 label app 后 MergeMeta 应删 app、保留第三方项
	actual := metav1.ObjectMeta{
		Labels:      map[string]string{"app": "web", names.LabelManaged: "true", "cni.example/x": "1"},
		Annotations: map[string]string{AnnotationSyncedLabels: "app", "third": "party"},
	}
	v.Labels = nil
	changed := MergeMeta(&actual, HostObjectMeta(v, "vc-e2e"))
	if !changed {
		t.Fatalf("expected change")
	}
	if _, ok := actual.Labels["app"]; ok {
		t.Fatalf("stale synced label not removed: %v", actual.Labels)
	}
	if actual.Labels["cni.example/x"] != "1" || actual.Annotations["third"] != "party" {
		t.Fatalf("third-party keys must be kept: %v %v", actual.Labels, actual.Annotations)
	}
	if MergeMeta(&actual, HostObjectMeta(v, "vc-e2e")) {
		t.Fatalf("second merge must be a no-op")
	}
}

func TestIsManagedExcludesHostControllers(t *testing.T) {
	managed := map[string]string{names.LabelManaged: "true"}
	if !IsManaged(&corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Labels: managed}}) {
		t.Fatalf("plain managed object")
	}
	kcm := map[string]string{names.LabelManaged: "true", LabelEndpointsManagedBy: "endpoint-controller"}
	if IsManaged(&corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Labels: kcm}}) {
		t.Fatalf("kcm endpoints must not be treated as ours")
	}
	slice := map[string]string{names.LabelManaged: "true", LabelEndpointSliceManagedBy: "endpointslice-controller.k8s.io"}
	if IsManaged(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: slice}}) {
		t.Fatalf("kcm endpointslice must not be treated as ours")
	}
	ours := map[string]string{names.LabelManaged: "true", LabelEndpointSliceManagedBy: EndpointSliceManager}
	if !IsManaged(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: ours}}) {
		t.Fatalf("our endpointslice")
	}
	key, ok := VirtualKey(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
		names.LabelManaged: "true", names.LabelVName: "web", names.LabelVNamespace: "demo"}}})
	if !ok || key.Name != "web" || key.Namespace != "demo" {
		t.Fatalf("VirtualKey: %v %v", key, ok)
	}
	if _, ok := VirtualKey(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: kcm}}); ok {
		t.Fatalf("VirtualKey must fail for kcm objects")
	}
}
