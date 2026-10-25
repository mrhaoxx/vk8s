package resources

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"vk8s.io/vk8s/internal/syncer/translate"
)

func TestSyncPodIdentityData(t *testing.T) {
	vpod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "demo", UID: "uid-1"}}
	ca := []byte("CA")

	data := map[string][]byte{translate.SATokenKeyToken: []byte("tok")}
	if !syncPodIdentityData(data, vpod, ca) {
		t.Fatalf("missing keys must report a change")
	}
	want := map[string]string{
		translate.SATokenKeyToken: "tok", translate.SATokenKeyCA: "CA",
		translate.SATokenKeyNamespace: "demo", translate.SATokenKeyPodName: "web", translate.SATokenKeyPodUID: "uid-1",
	}
	for k, v := range want {
		if string(data[k]) != v {
			t.Fatalf("%s = %q, want %q", k, data[k], v)
		}
	}
	if syncPodIdentityData(data, vpod, ca) {
		t.Fatalf("second sync must be a no-op")
	}
	if !syncPodIdentityData(data, vpod, []byte("CA2")) || string(data[translate.SATokenKeyCA]) != "CA2" {
		t.Fatalf("CA rotation must be applied")
	}
}
