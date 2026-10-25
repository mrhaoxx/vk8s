package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/pki"
)

const (
	// KeyAdminKubeconfig vk8s-system 内 manager admin kubeconfig Secret 的 key。
	KeyAdminKubeconfig = "kubeconfig"

	cnAPIServer   = "kube-apiserver"
	cnFakeKubelet = "vk8s-fake-kubelet"

	// egressSelectorConfig apiserver 的 egress selector 配置：cluster 流量经 HTTPConnect over UDS 到 syncer。
	egressSelectorConfig = `apiVersion: apiserver.k8s.io/v1beta1
kind: EgressSelectorConfiguration
egressSelections:
- name: cluster
  connection:
    proxyProtocol: HTTPConnect
    transport:
      uds:
        udsName: ` + names.KonnectivitySocket + `
`
)

// leafCertKeys 需要检查有效期的叶子证书 key（cert → key）。
var leafCertKeys = map[string]string{
	names.KeyAPIServerCert:     names.KeyAPIServerKey,
	names.KeyKubeletClientCert: names.KeyKubeletClientKey,
	names.KeyFakeKubeletCert:   names.KeyFakeKubeletKey,
	names.KeyFrontProxyCert:    names.KeyFrontProxyKey,
	names.KeySyncerServingCert: names.KeySyncerServingKey,
	names.KeyWebhookClientCert: names.KeyWebhookClientKey,
}

// requiredCertKeys 证书 Secret 必须具备的全部 key。
var requiredCertKeys = []string{
	names.KeyPlatformCA, names.KeyInternalCA,
	names.KeyAPIServerCert, names.KeyAPIServerKey,
	names.KeyKubeletClientCert, names.KeyKubeletClientKey,
	names.KeyFakeKubeletCert, names.KeyFakeKubeletKey,
	names.KeyFrontProxyCert, names.KeyFrontProxyKey,
	names.KeySyncerServingCert, names.KeySyncerServingKey,
	names.KeyServiceAccountPub, names.KeyServiceAccountKey,
	names.KeyKCMKubeconfig, names.KeySyncerKubeconfig, names.KeyWebhookKubeconfig,
	names.KeyEgressSelectorConf,
	names.KeyWebhookClientCert, names.KeyWebhookClientKey,
}

// ensureCertificates 第 8 步：内部 CA、全部叶子证书、SA 密钥、kubeconfig → 租户 ns 的证书 Secret；
// 另外在 vk8s-system 写 manager 修复用的 admin kubeconfig。返回证书 Secret 内容的哈希，
// 写进 StatefulSet pod template 以在换发时触发滚动重启。
func (r *VirtualClusterReconciler) ensureCertificates(ctx context.Context, vc *v1alpha1.VirtualCluster, clusterIP string) (string, error) {
	log := logf.FromContext(ctx)

	internalCA, err := r.ensureInternalCA(ctx, vc)
	if err != nil {
		return "", err
	}

	hostNS := names.HostNamespace(vc.Name)
	secret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Namespace: hostNS, Name: names.CertsSecretName(vc.Name)}, secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("read certs secret: %w", err)
	}
	exists := err == nil
	data := map[string][]byte{}
	if exists {
		data = secret.Data
	}

	dnsSANs, ipSANs := r.apiserverSANs(vc, clusterIP)
	renew, why := r.needsRenewal(data, internalCA, dnsSANs, ipSANs)
	if renew {
		if exists {
			log.Info("renewing certificates", "vcluster", vc.Name, "reason", why)
			r.Recorder.Eventf(vc, corev1.EventTypeNormal, "CertificatesRenewed", "renewing certificates: %s", why)
		}
		data, err = r.generateCertData(vc, internalCA, dnsSANs, ipSANs, data)
		if err != nil {
			return "", err
		}
		secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: names.CertsSecretName(vc.Name), Namespace: hostNS}}
		if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
			mergeLabels(secret, commonLabels(vc))
			secret.Type = corev1.SecretTypeOpaque
			secret.Data = data
			return r.setOwner(vc, secret)
		}); err != nil {
			return "", fmt.Errorf("write certs secret: %w", err)
		}
	}

	if err := r.ensureAdminKubeconfig(ctx, vc, internalCA, renew); err != nil {
		return "", err
	}
	return hashData(data), nil
}

// ensureInternalCA 每集群内部 CA，私钥只存 vk8s-system。
func (r *VirtualClusterReconciler) ensureInternalCA(ctx context.Context, vc *v1alpha1.VirtualCluster) (*pki.CA, error) {
	secret := &corev1.Secret{}
	key := types.NamespacedName{Namespace: r.Config.SystemNamespace, Name: names.InternalCASecretName(vc.Name)}
	err := r.Get(ctx, key, secret)
	if err == nil {
		ca, perr := pki.CAFromSecret(secret)
		if perr != nil {
			return nil, fmt.Errorf("internal CA secret is corrupt: %w", perr)
		}
		return ca, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read internal CA: %w", err)
	}
	ca, err := pki.NewCA("vk8s internal CA " + vc.Name)
	if err != nil {
		return nil, err
	}
	secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: commonLabels(vc)}}
	ca.WriteToSecret(secret)
	if err := r.setOwner(vc, secret); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// 并发创建：重新读取对方写入的那份。
			if gerr := r.Get(ctx, key, secret); gerr != nil {
				return nil, gerr
			}
			return pki.CAFromSecret(secret)
		}
		return nil, fmt.Errorf("create internal CA: %w", err)
	}
	return ca, nil
}

// apiserverSANs 虚拟 apiserver 服务端证书 SAN（设计 4.4）。
func (r *VirtualClusterReconciler) apiserverSANs(vc *v1alpha1.VirtualCluster, clusterIP string) ([]string, []net.IP) {
	svc := names.ServiceName(vc.Name)
	hostNS := names.HostNamespace(vc.Name)
	dns := []string{
		names.APIHost(vc.Name, r.Config.APIDomain),
		svc,
		svc + "." + hostNS,
		svc + "." + hostNS + ".svc",
		svc + "." + hostNS + ".svc.cluster.local",
		"kubernetes",
		"kubernetes.default",
		"kubernetes.default.svc",
		"kubernetes.default.svc.cluster.local",
		"localhost",
	}
	ips := []net.IP{net.ParseIP("127.0.0.1")}
	if ip := net.ParseIP(clusterIP); ip != nil {
		ips = append(ips, ip)
	}
	return dns, ips
}

// needsRenewal 判断证书 Secret 是否需要（重新）生成，并给出原因。
func (r *VirtualClusterReconciler) needsRenewal(data map[string][]byte, internalCA *pki.CA, dnsSANs []string, ipSANs []net.IP) (bool, string) {
	// 审计开关变化：启用但没有审计 kubeconfig、或关闭但还留着 → 重新生成（证书 Secret 哈希随之变化，控制平面滚动重启一次）
	if _, has := data[names.KeyAuditWebhookKubeconfig]; has != r.Config.Audit.Enabled {
		return true, "audit webhook kubeconfig presence does not match audit.enabled"
	}
	for _, k := range requiredCertKeys {
		if len(data[k]) == 0 {
			return true, "missing " + k
		}
	}
	if string(data[names.KeyPlatformCA]) != string(r.PlatformCA.CertPEM) {
		return true, "platform CA changed"
	}
	if string(data[names.KeyInternalCA]) != string(internalCA.CertPEM) {
		return true, "internal CA changed"
	}
	if string(data[names.KeyEgressSelectorConf]) != egressSelectorConfig {
		return true, "egress selector config changed"
	}
	for certKey := range leafCertKeys {
		if pki.NeedsRenewal(data[certKey]) {
			return true, certKey + " expires within 30 days"
		}
	}
	// 平台 CA 签的两张必须由当前平台 CA 签发；其余必须由当前内部 CA 签发。
	for certKey := range leafCertKeys {
		issuer := internalCA
		if certKey == names.KeyAPIServerCert || certKey == names.KeyWebhookClientCert {
			issuer = r.PlatformCA
		}
		if !issuer.Issued(data[certKey]) {
			return true, certKey + " not issued by current CA"
		}
	}
	apiCert, err := pki.ParseCertificate(data[names.KeyAPIServerCert])
	if err != nil {
		return true, "apiserver cert unparsable"
	}
	if !pki.HasSANs(apiCert, dnsSANs, ipSANs) {
		return true, "apiserver cert SAN out of date"
	}
	return false, ""
}

// generateCertData 生成整套叶子证书与 kubeconfig。SA 密钥对若已存在则保留（token 签名不可变）。
func (r *VirtualClusterReconciler) generateCertData(vc *v1alpha1.VirtualCluster, internalCA *pki.CA, dnsSANs []string, ipSANs []net.IP, old map[string][]byte) (map[string][]byte, error) {
	platformCA := r.PlatformCA
	svcDNS := names.ControlPlaneServiceDNS(vc.Name)
	localhostIP := []net.IP{net.ParseIP("127.0.0.1")}

	apiserver, err := platformCA.Sign(pki.CertConfig{
		CommonName: cnAPIServer, DNSNames: dnsSANs, IPAddresses: ipSANs, Usage: pki.UsageServer,
	})
	if err != nil {
		return nil, err
	}
	kubeletClient, err := internalCA.Sign(pki.CertConfig{CommonName: names.CNKubeletClient, Usage: pki.UsageClient})
	if err != nil {
		return nil, err
	}
	fakeKubelet, err := internalCA.Sign(pki.CertConfig{
		CommonName: cnFakeKubelet, DNSNames: []string{"localhost"}, IPAddresses: localhostIP, Usage: pki.UsageServer,
	})
	if err != nil {
		return nil, err
	}
	frontProxy, err := internalCA.Sign(pki.CertConfig{CommonName: names.CNFrontProxyClient, Usage: pki.UsageClient})
	if err != nil {
		return nil, err
	}
	syncerServing, err := internalCA.Sign(pki.CertConfig{
		CommonName: svcDNS, DNSNames: []string{"localhost", svcDNS}, IPAddresses: localhostIP, Usage: pki.UsageServer,
	})
	if err != nil {
		return nil, err
	}
	kcmClient, err := internalCA.Sign(pki.CertConfig{CommonName: names.CNKCM, Usage: pki.UsageClient})
	if err != nil {
		return nil, err
	}
	syncerClient, err := internalCA.Sign(pki.CertConfig{
		CommonName: names.CNSyncer, Organization: []string{names.OrgSystemMasters}, Usage: pki.UsageClient,
	})
	if err != nil {
		return nil, err
	}
	// webhook 客户端证书 CN 必须是 "vc-<name>"，manager 以此校验路径一致。
	webhookClient, err := platformCA.Sign(pki.CertConfig{CommonName: names.HostNamespace(vc.Name), Usage: pki.UsageClient})
	if err != nil {
		return nil, err
	}

	saPub, saKey := old[names.KeyServiceAccountPub], old[names.KeyServiceAccountKey]
	if len(saPub) == 0 || len(saKey) == 0 {
		saPub, saKey, err = pki.NewServiceAccountKeyPair()
		if err != nil {
			return nil, err
		}
	}

	localServer := fmt.Sprintf("https://127.0.0.1:%d", names.APIServerPort)
	kcmKubeconfig, err := pki.Kubeconfig(pki.KubeconfigOptions{
		ClusterName: vc.Name, UserName: names.CNKCM, Server: localServer, CAPEM: platformCA.CertPEM,
		ClientCertPEM: kcmClient.CertPEM, ClientKeyPEM: kcmClient.KeyPEM,
	})
	if err != nil {
		return nil, err
	}
	syncerKubeconfig, err := pki.Kubeconfig(pki.KubeconfigOptions{
		ClusterName: vc.Name, UserName: names.CNSyncer, Server: localServer, CAPEM: platformCA.CertPEM,
		ClientCertPEM: syncerClient.CertPEM, ClientKeyPEM: syncerClient.KeyPEM,
	})
	if err != nil {
		return nil, err
	}
	webhookKubeconfig, err := pki.Kubeconfig(pki.KubeconfigOptions{
		ClusterName: "vk8s-manager", UserName: names.HostNamespace(vc.Name),
		Server: r.tokenReviewURL(vc), CAPEM: platformCA.CertPEM,
		ClientCertPEM: webhookClient.CertPEM, ClientKeyPEM: webhookClient.KeyPEM,
	})
	if err != nil {
		return nil, err
	}
	// 审计 webhook 复用同一张客户端证书，只换 server；未启用审计时不写该 key，避免无谓的滚动重启。
	var auditKubeconfig []byte
	if r.Config.Audit.Enabled {
		if auditKubeconfig, err = auditWebhookKubeconfig(r.auditWebhookURL(vc), platformCA, webhookClient); err != nil {
			return nil, err
		}
	}

	data := map[string][]byte{
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
		names.KeyEgressSelectorConf: []byte(egressSelectorConfig),
		names.KeyWebhookClientCert:  webhookClient.CertPEM,
		names.KeyWebhookClientKey:   webhookClient.KeyPEM,
	}
	if auditKubeconfig != nil {
		data[names.KeyAuditWebhookKubeconfig] = auditKubeconfig
	}
	return data, nil
}

// auditWebhookURL 虚拟 apiserver 回调 manager 的审计 webhook 地址。
func (r *VirtualClusterReconciler) auditWebhookURL(vc *v1alpha1.VirtualCluster) string {
	return fmt.Sprintf("https://%s.%s.svc:%d%s%s",
		ManagerServiceName, r.Config.SystemNamespace, names.ManagerTLSPort, names.AuditWebhookPathPrefix, vc.Name)
}

// auditWebhookKubeconfig 审计 webhook 的 kubeconfig：平台 CA 校验 manager，客户端证书 CN=vc-<name>。
func auditWebhookKubeconfig(server string, platformCA *pki.CA, client *pki.Certificate) ([]byte, error) {
	return pki.Kubeconfig(pki.KubeconfigOptions{
		ClusterName: "vk8s-audit", UserName: client.Cert.Subject.CommonName,
		Server: server, CAPEM: platformCA.CertPEM,
		ClientCertPEM: client.CertPEM, ClientKeyPEM: client.KeyPEM,
	})
}

// tokenReviewURL 虚拟 apiserver 回调 manager 的 TokenReview 地址。
func (r *VirtualClusterReconciler) tokenReviewURL(vc *v1alpha1.VirtualCluster) string {
	return fmt.Sprintf("https://%s.%s.svc:%d%s%s",
		ManagerServiceName, r.Config.SystemNamespace, names.ManagerTLSPort, names.TokenReviewPathPrefix, vc.Name)
}

// ensureAdminKubeconfig vk8s-system 内 manager 访问该虚拟集群用的 kubeconfig（内部 CA 签，system:masters）。
// 证书 Secret 换发时一并重签；否则只在缺失时生成。
func (r *VirtualClusterReconciler) ensureAdminKubeconfig(ctx context.Context, vc *v1alpha1.VirtualCluster, internalCA *pki.CA, force bool) error {
	key := types.NamespacedName{Namespace: r.Config.SystemNamespace, Name: names.ManagerAdminKubeconfigSecret(vc.Name)}
	secret := &corev1.Secret{}
	err := r.Get(ctx, key, secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("read admin kubeconfig: %w", err)
	}
	if err == nil && !force && len(secret.Data[KeyAdminKubeconfig]) > 0 {
		return nil
	}
	adminClient, err := internalCA.Sign(pki.CertConfig{
		CommonName: names.CNManagerAdmin, Organization: []string{names.OrgSystemMasters}, Usage: pki.UsageClient,
	})
	if err != nil {
		return err
	}
	kubeconfig, err := pki.Kubeconfig(pki.KubeconfigOptions{
		ClusterName: vc.Name, UserName: names.CNManagerAdmin,
		Server:        fmt.Sprintf("https://%s:%d", names.ControlPlaneServiceDNS(vc.Name), names.ServicePort),
		CAPEM:         r.PlatformCA.CertPEM,
		ClientCertPEM: adminClient.CertPEM, ClientKeyPEM: adminClient.KeyPEM,
	})
	if err != nil {
		return err
	}
	secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		mergeLabels(secret, commonLabels(vc))
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{KeyAdminKubeconfig: kubeconfig}
		return r.setOwner(vc, secret)
	})
	if err != nil {
		return fmt.Errorf("write admin kubeconfig: %w", err)
	}
	return nil
}

// hashData 对 Secret data 的稳定哈希（按 key 排序），取前 16 个十六进制字符。
func hashData(data map[string][]byte) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write(data[k])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
