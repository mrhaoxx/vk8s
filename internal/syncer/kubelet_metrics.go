package syncer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/internal/names"
)

// nodeForIP 节点 IP → 虚拟 Node 名（虚拟 Node 镜像的是宿主真实地址）。
func nodeForIP(virtual client.Reader) func(ctx context.Context, ip string) (string, bool) {
	return func(ctx context.Context, ip string) (string, bool) {
		if ip == "" {
			return "", false
		}
		list := &corev1.NodeList{}
		if err := virtual.List(ctx, list); err != nil {
			return "", false
		}
		for i := range list.Items {
			for _, a := range list.Items[i].Status.Addresses {
				if a.Address == ip {
					return list.Items[i].Name, true
				}
			}
		}
		return "", false
	}
}

// hostPodLookup 宿主 Pod 名 → 虚拟 (namespace, name)，取自 syncer 写在宿主 Pod 上的 vk8s.io/vname / vnamespace。
func hostPodLookup(host client.Reader, hostNamespace string) func(ctx context.Context, hostPod string) (string, string, bool) {
	return func(ctx context.Context, hostPod string) (string, string, bool) {
		if hostPod == "" {
			return "", "", false
		}
		p := &corev1.Pod{}
		if err := host.Get(ctx, client.ObjectKey{Namespace: hostNamespace, Name: hostPod}, p); err != nil {
			return "", "", false
		}
		ns, name := p.Labels[names.LabelVNamespace], p.Labels[names.LabelVName]
		if ns == "" || name == "" {
			return "", "", false
		}
		return ns, name, true
	}
}

// managerMetrics 用证书 Secret 里的 webhook-client 证书（平台 CA 签，CN = vc-<name>）经 mTLS 问 manager 要已过滤的指标。
func managerMetrics(managerURL, vc, certsDir string, platformCA []byte) (func(ctx context.Context, node, path string) (io.ReadCloser, int, error), error) {
	base, err := url.Parse(strings.TrimSuffix(managerURL, "/"))
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("invalid manager url %q", managerURL)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(platformCA) {
		return nil, fmt.Errorf("platform CA has no certificates")
	}
	certFile, keyFile := filepath.Join(certsDir, names.KeyWebhookClientCert), filepath.Join(certsDir, names.KeyWebhookClientKey)
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
		// 每次握手重读：证书换发后无需重启 syncer
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			c, err := tls.LoadX509KeyPair(certFile, keyFile)
			return &c, err
		},
	}
	// 9s：比 Prometheus 默认 scrape_timeout（10s）短，比 manager 取数（8s）长
	httpClient := &http.Client{Timeout: 9 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsCfg, MaxIdleConnsPerHost: 4}}
	return func(ctx context.Context, node, path string) (io.ReadCloser, int, error) {
		u := *base
		u.Path = fmt.Sprintf("/kubelet/%s/nodes/%s/metrics/%s", url.PathEscape(vc), url.PathEscape(node), url.PathEscape(path))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, 0, err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, 0, err
		}
		return resp.Body, resp.StatusCode, nil
	}, nil
}
