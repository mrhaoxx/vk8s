package translate

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidateIngressHost(t *testing.T) {
	const vc, apps = "alice-dev", "apps.vk8s.corp.com"
	cases := []struct {
		host string
		ok   bool
	}{
		{"alice-dev.apps.vk8s.corp.com", true},
		{"web--alice-dev.apps.vk8s.corp.com", true},
		{"Web--Alice-Dev.apps.vk8s.corp.com.", true},
		{"", false},
		{"evil.example.com", false},
		{"bob--alice-dev.apps.vk8s.corp.com.evil.com", false},
		{"web.alice-dev.apps.vk8s.corp.com", false},   // 多级
		{"alice-dev2.apps.vk8s.corp.com", false},      // 别人的集群
		{"web--bob.apps.vk8s.corp.com", false},        // 别人的集群
		{"a--b--alice-dev.apps.vk8s.corp.com", false}, // label 含 --
		{"--alice-dev.apps.vk8s.corp.com", false},     // 空 label
		{"-web--alice-dev.apps.vk8s.corp.com", false}, // 非法 label
	}
	for _, c := range cases {
		err := ValidateIngressHost(c.host, vc, apps)
		if (err == nil) != c.ok {
			t.Errorf("host %q: ok=%v err=%v", c.host, c.ok, err)
		}
		if err != nil && !IsReject(err) {
			t.Errorf("host %q: error must be RejectError, got %T", c.host, err)
		}
	}
}

func TestTranslateIngress(t *testing.T) {
	env := IngressEnv{HostNamespace: "vc-alice-dev", VCName: "alice-dev", AppsDomain: "apps.vk8s.corp.com", AppsTLSSecret: "vk8s-apps-tls"}
	pt := networkingv1.PathTypePrefix
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "demo"},
		Spec: networkingv1.IngressSpec{
			IngressClassName: ptrString("nginx"),
			Rules: []networkingv1.IngressRule{{Host: "web--alice-dev.apps.vk8s.corp.com", IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Path: "/", PathType: &pt,
					Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "web", Port: networkingv1.ServiceBackendPort{Number: 80}}}}}}}}},
		},
	}
	h, err := TranslateIngress(ing, env)
	if err != nil {
		t.Fatalf("TranslateIngress: %v", err)
	}
	if h.Name != "web-x-demo" || *h.Spec.IngressClassName != "nginx" {
		t.Fatalf("meta/class: %+v", h.Spec)
	}
	if h.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name != "web-x-demo" {
		t.Fatalf("backend not renamed: %+v", h.Spec.Rules[0].HTTP.Paths[0].Backend)
	}
	if len(h.Spec.TLS) != 1 || h.Spec.TLS[0].SecretName != "vk8s-apps-tls" || h.Spec.TLS[0].Hosts[0] != "web--alice-dev.apps.vk8s.corp.com" {
		t.Fatalf("auto tls: %+v", h.Spec.TLS)
	}

	// 用户自带 tls：secret 改名，不自动补
	ing.Spec.TLS = []networkingv1.IngressTLS{{Hosts: []string{"web--alice-dev.apps.vk8s.corp.com"}, SecretName: "my-cert"}}
	h, err = TranslateIngress(ing, env)
	if err != nil || len(h.Spec.TLS) != 1 || h.Spec.TLS[0].SecretName != "my-cert-x-demo" {
		t.Fatalf("user tls: %v %+v", err, h.Spec.TLS)
	}

	// 任一 rule host 不合规 → 整个拒绝
	ing.Spec.Rules = append(ing.Spec.Rules, networkingv1.IngressRule{Host: "evil.example.com"})
	if _, err := TranslateIngress(ing, env); !IsReject(err) {
		t.Fatalf("want reject, got %v", err)
	}

	// 未配置泛证书时不补 tls
	ing.Spec.Rules = ing.Spec.Rules[:1]
	ing.Spec.TLS = nil
	env.AppsTLSSecret = ""
	h, err = TranslateIngress(ing, env)
	if err != nil || len(h.Spec.TLS) != 0 {
		t.Fatalf("no apps tls configured: %v %+v", err, h.Spec.TLS)
	}

	// backend.resource 不支持
	ing.Spec.Rules[0].HTTP.Paths[0].Backend = networkingv1.IngressBackend{Resource: &corev1.TypedLocalObjectReference{Kind: "StorageBucket", Name: "b"}}
	if _, err := TranslateIngress(ing, env); !IsReject(err) {
		t.Fatalf("backend.resource must be rejected, got %v", err)
	}
}

func ptrString(s string) *string { return &s }
