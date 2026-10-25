package addons

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"vk8s.io/vk8s/api/v1alpha1"
)

func svc(lb string) *corev1.Service {
	s := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "devpod-system", Name: "devpod-gateway"}, Spec: corev1.ServiceSpec{ClusterIP: "172.27.1.2"}}
	if lb != "" {
		s.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: lb}}
	}
	return s
}

func TestResolveOutputs(t *testing.T) {
	tpl := devpodsTemplate()
	tpl.Spec.Outputs = append(tpl.Spec.Outputs,
		v1alpha1.AddonOutput{Name: "web", Kind: "url", URL: "http://{{ .Cluster.Name }}.{{ .Cluster.AppsDomain }}"},
		v1alpha1.AddonOutput{Name: "pw", Kind: "secret", Secret: &v1alpha1.AddonSecretRef{Namespace: "devpod-system", Name: "admin", Key: "password"}},
		v1alpha1.AddonOutput{Name: "gone", Kind: "service", Service: &v1alpha1.AddonServiceRef{Namespace: "x", Name: "missing", Port: 80}},
	)
	ci := ClusterInfo{Name: "alice-dev", AppsDomain: "apps.example"}
	ctx := context.Background()
	got, err := ResolveOutputs(ctx, fake.NewClientBuilder().WithObjects(svc("172.28.0.8")).Build(), tpl, ci)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, o := range got {
		m[o.Name] = o.Value
	}
	if m["ssh"] != "172.28.0.8:22" || m["web"] != "http://alice-dev.apps.example" || len(m) != 2 {
		t.Fatalf("outputs = %v", m)
	}
	got, _ = ResolveOutputs(ctx, fake.NewClientBuilder().WithObjects(svc("")).Build(), tpl, ci)
	if got[0].Value != "172.27.1.2:22" {
		t.Fatalf("clusterIP fallback = %v", got)
	}
}

func TestReadSecretOutput(t *testing.T) {
	sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "g", Name: "admin"}, Data: map[string][]byte{"password": []byte("pw")}}
	c := fake.NewClientBuilder().WithObjects(sec).Build()
	o := v1alpha1.AddonOutput{Name: "pw", Kind: "secret", Secret: &v1alpha1.AddonSecretRef{Namespace: "g", Name: "admin", Key: "password"}}
	if v, err := ReadSecretOutput(context.Background(), c, o); err != nil || v != "pw" {
		t.Fatalf("v=%q err=%v", v, err)
	}
	o.Secret.Key = "nope"
	if _, err := ReadSecretOutput(context.Background(), c, o); err == nil {
		t.Fatal("missing key must fail")
	}
}
