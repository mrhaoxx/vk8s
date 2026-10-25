package manager

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/config"
	"vk8s.io/vk8s/internal/controllers"
	"vk8s.io/vk8s/internal/manager/auth"
	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/pki"
)

const (
	// sessionKeyLength 会话签名密钥长度。
	sessionKeyLength = 32
	// sessionKeyField 会话密钥 Secret 中的 key。
	sessionKeyField = "key"
	// bootstrapPasswordKey 引导管理员密码 Secret 中的明文 key。
	bootstrapPasswordKey = "password"
)

// ensurePlatformCA 读取或生成平台 CA（Secret vk8s-system/vk8s-ca）。
func ensurePlatformCA(ctx context.Context, c client.Client, sysNS string) (*pki.CA, error) {
	key := types.NamespacedName{Namespace: sysNS, Name: names.PlatformCASecret}
	secret := &corev1.Secret{}
	err := c.Get(ctx, key, secret)
	if err == nil {
		return pki.CAFromSecret(secret)
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read platform CA: %w", err)
	}
	ca, err := pki.NewCA("vk8s platform CA")
	if err != nil {
		return nil, err
	}
	secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: platformLabels()}}
	ca.WriteToSecret(secret)
	if err := c.Create(ctx, secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// 多副本同时首启：读对方写入的那份。
			if gerr := c.Get(ctx, key, secret); gerr != nil {
				return nil, gerr
			}
			return pki.CAFromSecret(secret)
		}
		return nil, fmt.Errorf("create platform CA: %w", err)
	}
	logf.FromContext(ctx).Info("generated platform CA", "secret", key.String())
	return ca, nil
}

// ensureSessionKey 读取或生成 32 字节会话签名密钥（Secret vk8s-session-key）。
func ensureSessionKey(ctx context.Context, c client.Client, sysNS string) ([]byte, error) {
	key := types.NamespacedName{Namespace: sysNS, Name: names.SessionKeySecret}
	secret := &corev1.Secret{}
	err := c.Get(ctx, key, secret)
	if err == nil {
		if k := secret.Data[sessionKeyField]; len(k) >= 16 {
			return k, nil
		}
		return nil, fmt.Errorf("secret %s has no usable %q", key.String(), sessionKeyField)
	}
	if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read session key: %w", err)
	}
	buf := make([]byte, sessionKeyLength)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate session key: %w", err)
	}
	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: platformLabels()},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{sessionKeyField: buf},
	}
	if err := c.Create(ctx, secret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			if gerr := c.Get(ctx, key, secret); gerr != nil {
				return nil, gerr
			}
			return secret.Data[sessionKeyField], nil
		}
		return nil, fmt.Errorf("create session key: %w", err)
	}
	return buf, nil
}

// ensureManagerCert 读取或签发 manager 8443 的服务端证书（平台 CA 签），剩余不足 30 天则换发。
func ensureManagerCert(ctx context.Context, c client.Client, sysNS string, ca *pki.CA) (*tls.Certificate, error) {
	key := types.NamespacedName{Namespace: sysNS, Name: names.ManagerCertsSecret}
	secret := &corev1.Secret{}
	err := c.Get(ctx, key, secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("read manager certs: %w", err)
	}
	if err == nil {
		certPEM, keyPEM := secret.Data[pki.SecretKeyCert], secret.Data[pki.SecretKeyKey]
		if len(certPEM) > 0 && len(keyPEM) > 0 && !pki.NeedsRenewal(certPEM) && ca.Issued(certPEM) {
			pair, perr := tls.X509KeyPair(certPEM, keyPEM)
			if perr == nil {
				return &pair, nil
			}
			logf.FromContext(ctx).Error(perr, "manager cert secret is corrupt, re-issuing")
		}
	}
	svc := controllers.ManagerServiceName
	cert, err := ca.Sign(pki.CertConfig{
		CommonName: svc,
		DNSNames: []string{
			svc,
			svc + "." + sysNS,
			svc + "." + sysNS + ".svc",
			svc + "." + sysNS + ".svc.cluster.local",
			"localhost",
		},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		Usage:       pki.UsageServer,
	})
	if err != nil {
		return nil, fmt.Errorf("sign manager cert: %w", err)
	}
	secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, c, secret, func() error {
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		for k, v := range platformLabels() {
			secret.Labels[k] = v
		}
		if secret.CreationTimestamp.IsZero() {
			secret.Type = corev1.SecretTypeTLS
		}
		secret.Data = cert.CertificateSecretData(ca.CertPEM)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("write manager certs: %w", err)
	}
	pair, err := tls.X509KeyPair(cert.CertPEM, cert.KeyPEM)
	if err != nil {
		return nil, err
	}
	logf.FromContext(ctx).Info("issued manager serving certificate", "secret", key.String(), "notAfter", cert.Cert.NotAfter)
	return &pair, nil
}

// ensureBootstrapAdmin 没有任何 admin User 时，按配置创建静态管理员：
// 明文密码来自 bootstrapAdmin.passwordSecretRef 的 "password" key，bcrypt 后写入 user-<name> Secret。
func ensureBootstrapAdmin(ctx context.Context, c client.Client, cfg *config.Config) error {
	log := logf.FromContext(ctx)
	users := &v1alpha1.UserList{}
	if err := c.List(ctx, users); err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	for _, u := range users.Items {
		if u.IsAdmin() {
			return nil
		}
	}
	ba := cfg.Auth.BootstrapAdmin
	if ba == nil || ba.Username == "" {
		log.Info("no admin user exists and auth.bootstrapAdmin is not configured; nobody can log in until a User with role admin is created")
		return nil
	}
	ns, name := ba.PasswordSecretRef.Split()
	if ns == "" {
		ns = cfg.SystemNamespace
	}
	pwSecret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, pwSecret); err != nil {
		return fmt.Errorf("read bootstrap admin password secret %s/%s: %w", ns, name, err)
	}
	password := string(pwSecret.Data[bootstrapPasswordKey])
	if password == "" {
		return fmt.Errorf("bootstrap admin password secret %s/%s has no %q key", ns, name, bootstrapPasswordKey)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return fmt.Errorf("bootstrap admin password: %w", err)
	}

	user := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: ba.Username},
		Spec: v1alpha1.UserSpec{
			DisplayName: ba.Username,
			Role:        v1alpha1.UserRoleAdmin,
			Auth: v1alpha1.UserAuth{
				Static: &v1alpha1.StaticAuth{PasswordSecretRef: corev1.LocalObjectReference{Name: names.UserPasswordSecret(ba.Username)}},
			},
		},
	}
	if err := c.Create(ctx, user); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// 同名用户已存在但不是 admin：提升为 admin 并补上静态密码。
			if gerr := c.Get(ctx, types.NamespacedName{Name: ba.Username}, user); gerr != nil {
				return gerr
			}
			user.Spec.Role = v1alpha1.UserRoleAdmin
			user.Spec.Disabled = false
			if user.Spec.Auth.Static == nil {
				user.Spec.Auth.Static = &v1alpha1.StaticAuth{PasswordSecretRef: corev1.LocalObjectReference{Name: names.UserPasswordSecret(ba.Username)}}
			}
			if uerr := c.Update(ctx, user); uerr != nil {
				return fmt.Errorf("promote bootstrap admin: %w", uerr)
			}
		} else {
			return fmt.Errorf("create bootstrap admin: %w", err)
		}
	}
	if err := auth.WritePasswordSecret(ctx, c, cfg.SystemNamespace, user, hash); err != nil {
		return err
	}
	log.Info("created bootstrap admin", "user", ba.Username)
	return nil
}

var serviceCIDRPattern = regexp.MustCompile(`valid IPs is ([0-9a-fA-F.:]+/[0-9]+)`)

// detectServiceCIDR 用 dry-run 创建一个 ClusterIP 越界的 Service，从 apiserver 的报错里读出宿主 Service CIDR。
func detectServiceCIDR(ctx context.Context, c client.Client, sysNS string) (string, error) {
	for _, probe := range []string{"1.1.1.1", "198.51.100.1", "203.0.113.1"} {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "vk8s-cidr-probe", Namespace: sysNS},
			Spec: corev1.ServiceSpec{
				Type:      corev1.ServiceTypeClusterIP,
				ClusterIP: probe,
				Ports:     []corev1.ServicePort{{Name: "probe", Port: 443, Protocol: corev1.ProtocolTCP}},
			},
		}
		err := c.Create(ctx, svc, client.DryRunAll)
		if err == nil {
			// 探测 IP 恰好在范围内，换下一个。
			continue
		}
		if m := serviceCIDRPattern.FindStringSubmatch(err.Error()); m != nil {
			return m[1], nil
		}
		if !apierrors.IsInvalid(err) {
			return "", fmt.Errorf("probe service CIDR: %w", err)
		}
	}
	return "", errors.New("could not detect host Service CIDR; set hostServiceCIDR in the platform config")
}

// detectDNSServiceIP 取 kube-system/kube-dns 的 ClusterIP。
func detectDNSServiceIP(ctx context.Context, c client.Client) (string, error) {
	svc := &corev1.Service{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: "kube-system", Name: "kube-dns"}, svc); err != nil {
		return "", fmt.Errorf("read kube-system/kube-dns (set hostDNSServiceIP explicitly if the DNS service has another name): %w", err)
	}
	if svc.Spec.ClusterIP == "" || svc.Spec.ClusterIP == corev1.ClusterIPNone {
		return "", errors.New("kube-system/kube-dns has no ClusterIP; set hostDNSServiceIP explicitly")
	}
	return svc.Spec.ClusterIP, nil
}

func platformLabels() map[string]string {
	return map[string]string{controllers.LabelManagedBy: controllers.ManagedByValue}
}
