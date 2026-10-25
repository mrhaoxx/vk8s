package controllers

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/internal/names"
)

// AdminKubeconfigClients 用 vk8s-system 里的 vc-<name>-admin-kubeconfig 构造虚拟集群 client，
// 按 Secret 的 resourceVersion 缓存（证书换发后自动重建）。
type AdminKubeconfigClients struct {
	Reader          client.Reader
	Scheme          *runtime.Scheme
	SystemNamespace string

	mu    sync.Mutex
	cache map[string]cachedClient
}

type cachedClient struct {
	rv string
	c  client.Client
}

// For 实现 VirtualClients。
func (a *AdminKubeconfigClients) For(ctx context.Context, vc string) (client.Client, error) {
	sec := &corev1.Secret{}
	if err := a.Reader.Get(ctx, client.ObjectKey{Namespace: a.SystemNamespace, Name: names.ManagerAdminKubeconfigSecret(vc)}, sec); err != nil {
		return nil, fmt.Errorf("admin kubeconfig for %s: %w", vc, err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cache == nil {
		a.cache = map[string]cachedClient{}
	}
	if c, ok := a.cache[vc]; ok && c.rv == sec.ResourceVersion {
		return c.c, nil
	}
	cfg, err := clientcmd.RESTConfigFromKubeConfig(sec.Data[KeyAdminKubeconfig])
	if err != nil {
		return nil, fmt.Errorf("parse admin kubeconfig for %s: %w", vc, err)
	}
	c, err := client.New(cfg, client.Options{Scheme: a.Scheme})
	if err != nil {
		return nil, err
	}
	a.cache[vc] = cachedClient{rv: sec.ResourceVersion, c: c}
	return c, nil
}
