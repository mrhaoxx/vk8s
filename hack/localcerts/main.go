// Command localcerts 为本地冒烟测试生成一套与 operator ensureCertificates 等价的证书目录
// （docs 第 13 节验证 1：kine + apiserver + kcm 同进程启动）。
//
// 用法：go run ./hack/localcerts -out /tmp/vk8s-local/certs -vcluster dev
// 额外输出 admin.kubeconfig（内部 CA 签发、system:masters、指向 https://127.0.0.1:6443）供 kubectl 使用。
package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/pki"
)

func main() {
	out := flag.String("out", "", "输出目录（必填）")
	vc := flag.String("vcluster", "dev", "虚拟集群名")
	apiDomain := flag.String("api-domain", "vk8s.local", "API 域名后缀")
	tokenReviewURL := flag.String("tokenreview-url", "https://127.0.0.1:18443/tokenreview/", "webhook.kubeconfig 的 server（本地测试无 manager，仅占位）")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "-out is required")
		os.Exit(2)
	}
	if err := run(*out, *vc, *apiDomain, *tokenReviewURL); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(out, vc, apiDomain, tokenReviewURL string) error {
	platformCA, err := pki.NewCA("vk8s platform CA (local)")
	if err != nil {
		return err
	}
	internalCA, err := pki.NewCA("vk8s internal CA " + vc)
	if err != nil {
		return err
	}

	svc := names.ServiceName(vc)
	hostNS := names.HostNamespace(vc)
	dnsSANs := []string{
		names.APIHost(vc, apiDomain),
		svc, svc + "." + hostNS, svc + "." + hostNS + ".svc", svc + "." + hostNS + ".svc.cluster.local",
		"kubernetes", "kubernetes.default", "kubernetes.default.svc", "kubernetes.default.svc.cluster.local",
		"localhost",
	}
	localhost := []net.IP{net.ParseIP("127.0.0.1")}
	svcDNS := names.ControlPlaneServiceDNS(vc)

	sign := func(ca *pki.CA, cfg pki.CertConfig) *pki.Certificate {
		if err != nil {
			return nil
		}
		var c *pki.Certificate
		c, err = ca.Sign(cfg)
		return c
	}
	apiserver := sign(platformCA, pki.CertConfig{CommonName: "kube-apiserver", DNSNames: dnsSANs, IPAddresses: localhost, Usage: pki.UsageServer})
	kubeletClient := sign(internalCA, pki.CertConfig{CommonName: names.CNKubeletClient, Usage: pki.UsageClient})
	fakeKubelet := sign(internalCA, pki.CertConfig{CommonName: "vk8s-fake-kubelet", DNSNames: []string{"localhost"}, IPAddresses: localhost, Usage: pki.UsageServer})
	frontProxy := sign(internalCA, pki.CertConfig{CommonName: names.CNFrontProxyClient, Usage: pki.UsageClient})
	syncerServing := sign(internalCA, pki.CertConfig{CommonName: svcDNS, DNSNames: []string{"localhost", svcDNS}, IPAddresses: localhost, Usage: pki.UsageServer})
	kcmClient := sign(internalCA, pki.CertConfig{CommonName: names.CNKCM, Usage: pki.UsageClient})
	syncerClient := sign(internalCA, pki.CertConfig{CommonName: names.CNSyncer, Organization: []string{names.OrgSystemMasters}, Usage: pki.UsageClient})
	webhookClient := sign(platformCA, pki.CertConfig{CommonName: hostNS, Usage: pki.UsageClient})
	admin := sign(internalCA, pki.CertConfig{CommonName: names.CNManagerAdmin, Organization: []string{names.OrgSystemMasters}, Usage: pki.UsageClient})
	if err != nil {
		return err
	}

	saPub, saKey, err := pki.NewServiceAccountKeyPair()
	if err != nil {
		return err
	}

	localServer := fmt.Sprintf("https://127.0.0.1:%d", names.APIServerPort)
	kubeconfig := func(user, server string, c *pki.Certificate) []byte {
		if err != nil {
			return nil
		}
		var b []byte
		b, err = pki.Kubeconfig(pki.KubeconfigOptions{
			ClusterName: vc, UserName: user, Server: server, CAPEM: platformCA.CertPEM,
			ClientCertPEM: c.CertPEM, ClientKeyPEM: c.KeyPEM,
		})
		return b
	}
	kcmKubeconfig := kubeconfig(names.CNKCM, localServer, kcmClient)
	syncerKubeconfig := kubeconfig(names.CNSyncer, localServer, syncerClient)
	webhookKubeconfig := kubeconfig(hostNS, tokenReviewURL+vc, webhookClient)
	adminKubeconfig := kubeconfig(names.CNManagerAdmin, localServer, admin)
	if err != nil {
		return err
	}

	egress := "apiVersion: apiserver.k8s.io/v1beta1\nkind: EgressSelectorConfiguration\negressSelections:\n- name: cluster\n  connection:\n    proxyProtocol: HTTPConnect\n    transport:\n      uds:\n        udsName: " + names.KonnectivitySocket + "\n"

	files := map[string][]byte{
		names.KeyPlatformCA:         platformCA.CertPEM,
		names.KeyInternalCA:         internalCA.CertPEM,
		names.KeyAPIServerCert:      apiserver.CertPEM,
		names.KeyAPIServerKey:       apiserver.KeyPEM,
		names.KeyKubeletClientCert:  kubeletClient.CertPEM,
		names.KeyKubeletClientKey:   kubeletClient.KeyPEM,
		names.KeyFakeKubeletCert:    fakeKubelet.CertPEM,
		names.KeyFakeKubeletKey:     fakeKubelet.KeyPEM,
		names.KeyFrontProxyCert:     frontProxy.CertPEM,
		names.KeyFrontProxyKey:      frontProxy.KeyPEM,
		names.KeySyncerServingCert:  syncerServing.CertPEM,
		names.KeySyncerServingKey:   syncerServing.KeyPEM,
		names.KeyServiceAccountPub:  saPub,
		names.KeyServiceAccountKey:  saKey,
		names.KeyKCMKubeconfig:      kcmKubeconfig,
		names.KeySyncerKubeconfig:   syncerKubeconfig,
		names.KeyWebhookKubeconfig:  webhookKubeconfig,
		names.KeyEgressSelectorConf: []byte(egress),
		names.KeyWebhookClientCert:  webhookClient.CertPEM,
		names.KeyWebhookClientKey:   webhookClient.KeyPEM,
		"admin.kubeconfig":          adminKubeconfig,
		"internal-ca.key":           internalCA.KeyPEM,
		"platform-ca.key":           platformCA.KeyPEM,
	}
	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(out, name), data, 0o600); err != nil {
			return err
		}
	}
	fmt.Printf("wrote %d files to %s\n", len(files), out)
	return nil
}
