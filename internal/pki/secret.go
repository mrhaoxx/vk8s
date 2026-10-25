package pki

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// CA 在 Secret 中的 key，与 kubernetes.io/tls 类型保持一致。
const (
	SecretKeyCert = corev1.TLSCertKey       // tls.crt
	SecretKeyKey  = corev1.TLSPrivateKeyKey // tls.key
)

// CAFromSecret 从 Secret 的 tls.crt / tls.key 读出 CA。
func CAFromSecret(s *corev1.Secret) (*CA, error) {
	if s == nil {
		return nil, errors.New("nil secret")
	}
	certPEM, ok := s.Data[SecretKeyCert]
	if !ok || len(certPEM) == 0 {
		return nil, fmt.Errorf("secret %s/%s: missing %s", s.Namespace, s.Name, SecretKeyCert)
	}
	keyPEM, ok := s.Data[SecretKeyKey]
	if !ok || len(keyPEM) == 0 {
		return nil, fmt.Errorf("secret %s/%s: missing %s", s.Namespace, s.Name, SecretKeyKey)
	}
	ca, err := ParseCA(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("secret %s/%s: %w", s.Namespace, s.Name, err)
	}
	return ca, nil
}

// SecretData 返回写入 Secret 的 data（tls.crt / tls.key）。
func (ca *CA) SecretData() map[string][]byte {
	return map[string][]byte{
		SecretKeyCert: ca.CertPEM,
		SecretKeyKey:  ca.KeyPEM,
	}
}

// WriteToSecret 把 CA 写入 Secret（类型 kubernetes.io/tls）。
func (ca *CA) WriteToSecret(s *corev1.Secret) {
	s.Type = corev1.SecretTypeTLS
	s.Data = ca.SecretData()
}

// CertificateSecretData 返回叶子证书写入 kubernetes.io/tls Secret 的 data，附带 ca.crt。
func (c *Certificate) CertificateSecretData(caPEM []byte) map[string][]byte {
	data := map[string][]byte{
		SecretKeyCert: c.CertPEM,
		SecretKeyKey:  c.KeyPEM,
	}
	if len(caPEM) > 0 {
		data["ca.crt"] = caPEM
	}
	return data
}
