package pki

import (
	"errors"
	"fmt"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// KubeconfigOptions kubeconfig 构造参数。ClientCertPEM/ClientKeyPEM 与 Token 二选一。
type KubeconfigOptions struct {
	// ClusterName / UserName / ContextName 为空时取 "vk8s"。
	ClusterName string
	UserName    string
	ContextName string

	Server string
	CAPEM  []byte

	ClientCertPEM []byte
	ClientKeyPEM  []byte
	Token         string

	// Namespace 可选，写入 context。
	Namespace string
}

// Kubeconfig 用 clientcmd/api 类型构造 kubeconfig 并序列化为 YAML。
func Kubeconfig(o KubeconfigOptions) ([]byte, error) {
	if o.Server == "" {
		return nil, errors.New("kubeconfig: Server is required")
	}
	if len(o.CAPEM) == 0 {
		return nil, errors.New("kubeconfig: CAPEM is required")
	}
	hasCert := len(o.ClientCertPEM) > 0 || len(o.ClientKeyPEM) > 0
	hasToken := o.Token != ""
	if hasCert == hasToken {
		return nil, errors.New("kubeconfig: exactly one of client certificate or token must be set")
	}
	if hasCert && (len(o.ClientCertPEM) == 0 || len(o.ClientKeyPEM) == 0) {
		return nil, errors.New("kubeconfig: both ClientCertPEM and ClientKeyPEM are required")
	}

	cluster := orDefault(o.ClusterName, "vk8s")
	user := orDefault(o.UserName, "vk8s")
	context := orDefault(o.ContextName, cluster)

	cfg := clientcmdapi.NewConfig()
	cfg.Clusters[cluster] = &clientcmdapi.Cluster{
		Server:                   o.Server,
		CertificateAuthorityData: o.CAPEM,
	}
	authInfo := &clientcmdapi.AuthInfo{}
	if hasCert {
		authInfo.ClientCertificateData = o.ClientCertPEM
		authInfo.ClientKeyData = o.ClientKeyPEM
	} else {
		authInfo.Token = o.Token
	}
	cfg.AuthInfos[user] = authInfo
	cfg.Contexts[context] = &clientcmdapi.Context{
		Cluster:   cluster,
		AuthInfo:  user,
		Namespace: o.Namespace,
	}
	cfg.CurrentContext = context

	out, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, fmt.Errorf("serialize kubeconfig: %w", err)
	}
	return out, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
