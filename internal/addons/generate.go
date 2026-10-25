package addons

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"fmt"

	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/api/v1alpha1"
)

// AddonGeneratedLabel 平台为应用建的 namespace 上的标记：只有带它的 namespace 才会被补上 helm 归属信息。
const AddonGeneratedLabel = "vk8s.io/addon-generated"

// EnsureGenerated 在虚拟集群里建模板声明的 namespace 与 Secret；已存在的 Secret 原样保留。
func EnsureGenerated(ctx context.Context, c client.Client, t *v1alpha1.AddonTemplate) error {
	for _, g := range t.Spec.Generate {
		releaseNS := g.Secret.Namespace == t.Spec.Namespace
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: g.Secret.Namespace, Labels: map[string]string{AddonGeneratedLabel: "true"}}}
		if releaseNS {
			// chart 也可能渲染这个 namespace：预先标成属于该 release，helm 才会接管而不是报
			// "exists and cannot be imported into the current release"。只对平台建的 namespace 打标。
			setHelmOwnership(ns, t)
		}
		if err := c.Create(ctx, ns); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return fmt.Errorf("create namespace %s: %w", g.Secret.Namespace, err)
			}
			if releaseNS {
				if err := repairHelmOwnership(ctx, c, t, g.Secret.Namespace); err != nil {
					return err
				}
			}
		}
		existing := &corev1.Secret{}
		err := c.Get(ctx, client.ObjectKey{Namespace: g.Secret.Namespace, Name: g.Secret.Name}, existing)
		if err == nil {
			continue
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
		data, err := generateData(g)
		if err != nil {
			return err
		}
		sec := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: g.Secret.Namespace, Name: g.Secret.Name,
			Labels: map[string]string{"app.kubernetes.io/managed-by": "vk8s-addons"}}, Data: data}
		if err := c.Create(ctx, sec); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("create secret %s/%s: %w", g.Secret.Namespace, g.Secret.Name, err)
		}
	}
	return nil
}

func generateData(g v1alpha1.AddonGenerate) (map[string][]byte, error) {
	switch g.Kind {
	case "sshKeyPair":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		block, err := ssh.MarshalPrivateKey(priv, "vk8s-addon")
		if err != nil {
			return nil, err
		}
		sshPub, err := ssh.NewPublicKey(pub)
		if err != nil {
			return nil, err
		}
		return map[string][]byte{
			"ssh_host_ed25519_key":     pem.EncodeToMemory(block),
			"ssh_host_ed25519_key.pub": ssh.MarshalAuthorizedKey(sshPub),
		}, nil
	case "password":
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		key := g.Key
		if key == "" {
			key = "password"
		}
		return map[string][]byte{key: []byte(base64.RawURLEncoding.EncodeToString(buf))}, nil
	}
	return nil, fmt.Errorf("unknown generate kind %q", g.Kind)
}

func setHelmOwnership(ns *corev1.Namespace, t *v1alpha1.AddonTemplate) {
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	if ns.Annotations == nil {
		ns.Annotations = map[string]string{}
	}
	ns.Labels["app.kubernetes.io/managed-by"] = "Helm"
	ns.Annotations["meta.helm.sh/release-name"] = t.Spec.Release
	ns.Annotations["meta.helm.sh/release-namespace"] = t.Spec.Namespace
}

// repairHelmOwnership 平台以前建、但还没有 helm 归属信息的 namespace 补上；用户自己建的（无平台标记）不碰。
func repairHelmOwnership(ctx context.Context, c client.Client, t *v1alpha1.AddonTemplate, name string) error {
	ns := &corev1.Namespace{}
	if err := c.Get(ctx, client.ObjectKey{Name: name}, ns); err != nil {
		return err
	}
	if ns.Labels[AddonGeneratedLabel] != "true" || ns.Annotations["meta.helm.sh/release-name"] != "" {
		return nil
	}
	setHelmOwnership(ns, t)
	return c.Update(ctx, ns)
}
