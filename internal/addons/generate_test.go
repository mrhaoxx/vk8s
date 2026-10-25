package addons

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"vk8s.io/vk8s/api/v1alpha1"
)

func TestEnsureGeneratedIsIdempotent(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	tpl := devpodsTemplate()
	tpl.Spec.Generate = append(tpl.Spec.Generate, v1alpha1.AddonGenerate{Secret: v1alpha1.NamespacedName{Namespace: "devpod-system", Name: "admin"}, Kind: "password"})
	ctx := context.Background()
	if err := EnsureGenerated(ctx, c, tpl); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, client.ObjectKey{Name: "devpod-system"}, &corev1.Namespace{}); err != nil {
		t.Fatalf("namespace: %v", err)
	}
	host := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: "devpod-system", Name: "devpod-gateway-host-key"}, host); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(host.Data["ssh_host_ed25519_key"]), "-----BEGIN OPENSSH PRIVATE KEY-----") || !strings.HasPrefix(string(host.Data["ssh_host_ed25519_key.pub"]), "ssh-ed25519 ") {
		t.Fatalf("ssh secret = %v", host.Data)
	}
	pw := &corev1.Secret{}
	_ = c.Get(ctx, client.ObjectKey{Namespace: "devpod-system", Name: "admin"}, pw)
	if len(pw.Data["password"]) < 32 {
		t.Fatalf("password = %q", pw.Data["password"])
	}
	before := string(host.Data["ssh_host_ed25519_key"])
	if err := EnsureGenerated(ctx, c, tpl); err != nil {
		t.Fatal(err)
	}
	_ = c.Get(ctx, client.ObjectKey{Namespace: "devpod-system", Name: "devpod-gateway-host-key"}, host)
	if string(host.Data["ssh_host_ed25519_key"]) != before {
		t.Fatal("existing secret must not be regenerated")
	}
}

// chart 自己也渲染了这个 namespace 时，helm 只接管带归属标记的对象：平台建的 namespace 必须预先标成属于该 release。
func TestEnsureGeneratedNamespaceIsAdoptableByHelm(t *testing.T) {
	c := fake.NewClientBuilder().Build()
	ctx := context.Background()
	if err := EnsureGenerated(ctx, c, devpodsTemplate()); err != nil {
		t.Fatal(err)
	}
	ns := &corev1.Namespace{}
	_ = c.Get(ctx, client.ObjectKey{Name: "devpod-system"}, ns)
	if ns.Labels["app.kubernetes.io/managed-by"] != "Helm" || ns.Annotations["meta.helm.sh/release-name"] != "devpod" ||
		ns.Annotations["meta.helm.sh/release-namespace"] != "devpod-system" {
		t.Fatalf("namespace not adoptable: labels=%v annotations=%v", ns.Labels, ns.Annotations)
	}
	// 用户自己建的 namespace 不碰
	c = fake.NewClientBuilder().WithObjects(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "devpod-system"}}).Build()
	_ = EnsureGenerated(ctx, c, devpodsTemplate())
	ns = &corev1.Namespace{}
	_ = c.Get(ctx, client.ObjectKey{Name: "devpod-system"}, ns)
	if ns.Annotations["meta.helm.sh/release-name"] != "" {
		t.Fatalf("must not claim a pre-existing namespace: %v", ns.Annotations)
	}
}

// 平台以前建、但没打 helm 标记的 namespace（带平台标记）要补上，否则 chart 永远 "cannot be imported"。
func TestEnsureGeneratedRepairsPlatformNamespaceWithoutHelmMetadata(t *testing.T) {
	old := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "devpod-system", Labels: map[string]string{AddonGeneratedLabel: "true"}}}
	c := fake.NewClientBuilder().WithObjects(old).Build()
	if err := EnsureGenerated(context.Background(), c, devpodsTemplate()); err != nil {
		t.Fatal(err)
	}
	ns := &corev1.Namespace{}
	_ = c.Get(context.Background(), client.ObjectKey{Name: "devpod-system"}, ns)
	if ns.Annotations["meta.helm.sh/release-name"] != "devpod" || ns.Labels["app.kubernetes.io/managed-by"] != "Helm" {
		t.Fatalf("not repaired: %v %v", ns.Labels, ns.Annotations)
	}
}
