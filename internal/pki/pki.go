// Package pki 实现平台的两层 CA：平台 CA 与每集群内部 CA 的生成、叶子证书签发、
// ServiceAccount 签名密钥对、kubeconfig 构造，以及与 Secret 的互转。
//
// 全部 CA 与叶子证书使用 ECDSA P-256；只有 ServiceAccount 签名密钥用 RSA 2048
// （kube-apiserver 的 SA token 校验链路对 RSA 支持最稳）。私钥统一以 PKCS#8 PEM 编码，
// Go 的 tls.X509KeyPair 与 k8s 的 keyutil 都能直接读取。
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"
)

const (
	// CAValidity CA 证书有效期（10 年）。
	CAValidity = 10 * 365 * 24 * time.Hour
	// LeafValidity 叶子证书有效期（1 年）。
	LeafValidity = 365 * 24 * time.Hour
	// RenewBefore 叶子证书剩余有效期低于该值时换发。
	RenewBefore = 30 * 24 * time.Hour

	pemTypeCertificate = "CERTIFICATE"
	pemTypePrivateKey  = "PRIVATE KEY"
	pemTypeECKey       = "EC PRIVATE KEY"
	pemTypeRSAKey      = "RSA PRIVATE KEY"
	pemTypePublicKey   = "PUBLIC KEY"
)

// Usage 叶子证书用途，可按位组合。
type Usage int

const (
	// UsageClient 客户端认证。
	UsageClient Usage = 1 << iota
	// UsageServer 服务端认证。
	UsageServer
)

// CA 一个可签发证书的 CA（证书 + 私钥）。
type CA struct {
	Cert    *x509.Certificate
	Key     *ecdsa.PrivateKey
	CertPEM []byte
	KeyPEM  []byte
}

// Certificate 一张签发出来的叶子证书。
type Certificate struct {
	Cert    *x509.Certificate
	CertPEM []byte
	KeyPEM  []byte
}

// CertConfig 叶子证书签发参数。
type CertConfig struct {
	CommonName   string
	Organization []string
	DNSNames     []string
	IPAddresses  []net.IP
	Usage        Usage
	// Validity 为 0 时取 LeafValidity。
	Validity time.Duration
}

// NewCA 生成一个新的自签 CA（ECDSA P-256，10 年）。
func NewCA(commonName string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate CA key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"vk8s"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create CA certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	keyPEM, err := MarshalPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &CA{
		Cert:    cert,
		Key:     key,
		CertPEM: encodePEM(pemTypeCertificate, der),
		KeyPEM:  keyPEM,
	}, nil
}

// ParseCA 从 PEM 编码的证书与私钥还原 CA。私钥必须是 ECDSA。
func ParseCA(certPEM, keyPEM []byte) (*CA, error) {
	cert, err := ParseCertificate(certPEM)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("certificate is not a CA")
	}
	signer, err := ParsePrivateKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse CA key: %w", err)
	}
	ecKey, ok := signer.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("CA key must be ECDSA, got %T", signer)
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !pub.Equal(&ecKey.PublicKey) {
		return nil, errors.New("CA key does not match CA certificate")
	}
	return &CA{Cert: cert, Key: ecKey, CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

// Sign 用本 CA 签发一张叶子证书（ECDSA P-256）。
func (ca *CA) Sign(cfg CertConfig) (*Certificate, error) {
	if cfg.CommonName == "" {
		return nil, errors.New("sign: CommonName is required")
	}
	if cfg.Usage == 0 {
		return nil, errors.New("sign: Usage is required")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	validity := cfg.Validity
	if validity <= 0 {
		validity = LeafValidity
	}
	now := time.Now()
	notAfter := now.Add(validity)
	// 叶子证书不能比 CA 活得更久。
	if notAfter.After(ca.Cert.NotAfter) {
		notAfter = ca.Cert.NotAfter
	}
	var extUsage []x509.ExtKeyUsage
	if cfg.Usage&UsageClient != 0 {
		extUsage = append(extUsage, x509.ExtKeyUsageClientAuth)
	}
	if cfg.Usage&UsageServer != 0 {
		extUsage = append(extUsage, x509.ExtKeyUsageServerAuth)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cfg.CommonName, Organization: cfg.Organization},
		DNSNames:              cfg.DNSNames,
		IPAddresses:           cfg.IPAddresses,
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           extUsage,
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, fmt.Errorf("sign certificate %q: %w", cfg.CommonName, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse signed certificate: %w", err)
	}
	keyPEM, err := MarshalPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &Certificate{Cert: cert, CertPEM: encodePEM(pemTypeCertificate, der), KeyPEM: keyPEM}, nil
}

// CertPool 返回只含本 CA 的证书池，用于 tls.Config 的 RootCAs / ClientCAs。
func (ca *CA) CertPool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return pool
}

// Issued 判断 PEM 证书是否由本 CA 直接签发（链校验，忽略用途与有效期之外的策略）。
func (ca *CA) Issued(certPEM []byte) bool {
	cert, err := ParseCertificate(certPEM)
	if err != nil {
		return false
	}
	return cert.CheckSignatureFrom(ca.Cert) == nil
}

// NewServiceAccountKeyPair 生成 ServiceAccount 签名密钥对（RSA 2048）。
// 返回 PKIX 公钥 PEM 与 PKCS#8 私钥 PEM，分别对应 kube-apiserver 的
// --service-account-key-file 与 --service-account-signing-key-file。
func NewServiceAccountKeyPair() (pubPEM, keyPEM []byte, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, fmt.Errorf("generate RSA key: %w", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal RSA public key: %w", err)
	}
	keyPEM, err = MarshalPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return encodePEM(pemTypePublicKey, pubDER), keyPEM, nil
}

// ExpiresWithin 证书是否在 d 内过期（或已经过期）。
func ExpiresWithin(certPEM []byte, d time.Duration) (bool, error) {
	cert, err := ParseCertificate(certPEM)
	if err != nil {
		return false, err
	}
	return time.Now().Add(d).After(cert.NotAfter), nil
}

// NeedsRenewal 证书是否应当换发：解析失败、已过期或剩余不足 RenewBefore 都视为需要换发。
func NeedsRenewal(certPEM []byte) bool {
	expiring, err := ExpiresWithin(certPEM, RenewBefore)
	return err != nil || expiring
}

// HasSANs 证书的 SAN 是否覆盖了全部给定的 DNS 名与 IP。
func HasSANs(cert *x509.Certificate, dnsNames []string, ips []net.IP) bool {
	for _, want := range dnsNames {
		found := false
		for _, have := range cert.DNSNames {
			if have == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, want := range ips {
		found := false
		for _, have := range cert.IPAddresses {
			if have.Equal(want) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// ParseCertificate 解析 PEM 中的第一张证书。
func ParseCertificate(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != pemTypeCertificate {
		return nil, errors.New("no CERTIFICATE block found in PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

// ParsePrivateKey 解析 PKCS#8 / EC / RSA 三种 PEM 私钥。
func ParsePrivateKey(keyPEM []byte) (crypto.Signer, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("no PEM block found in private key")
	}
	switch block.Type {
	case pemTypePrivateKey:
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		signer, ok := k.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("unsupported private key type %T", k)
		}
		return signer, nil
	case pemTypeECKey:
		return x509.ParseECPrivateKey(block.Bytes)
	case pemTypeRSAKey:
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported PEM block type %q", block.Type)
	}
}

// MarshalPrivateKey 以 PKCS#8 PEM 编码私钥。
func MarshalPrivateKey(key crypto.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}
	return encodePEM(pemTypePrivateKey, der), nil
}

func encodePEM(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

// newSerial 生成 128 位随机序列号。
func newSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}
	return serial, nil
}
