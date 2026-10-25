package controllers

import (
	"testing"

	"k8s.io/client-go/tools/clientcmd"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/config"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/pki"
)

func TestAuditWebhookKubeconfigPointsAtManagerAuditPath(t *testing.T) {
	ca, err := pki.NewCA("vk8s-platform-ca")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := ca.Sign(pki.CertConfig{CommonName: "vc-alice-dev", Usage: pki.UsageClient})
	if err != nil {
		t.Fatal(err)
	}
	server := "https://vk8s-manager.vk8s-system.svc:8443/webhook/audit/alice-dev"
	b, err := auditWebhookKubeconfig(server, ca, leaf)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := clientcmd.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	cl := cfg.Clusters["vk8s-audit"]
	if cl == nil || cl.Server != server || len(cl.CertificateAuthorityData) == 0 {
		t.Fatalf("clusters = %+v", cfg.Clusters)
	}
	if u := cfg.AuthInfos["vc-alice-dev"]; u == nil || len(u.ClientCertificateData) == 0 || len(u.ClientKeyData) == 0 {
		t.Fatalf("authinfos = %+v", cfg.AuthInfos)
	}
}

func TestAuditWebhookURL(t *testing.T) {
	r := &VirtualClusterReconciler{Config: testConfig()}
	vc := &v1alpha1.VirtualCluster{}
	vc.Name = "alice-dev"
	if got := r.auditWebhookURL(vc); got != "https://"+ManagerServiceName+"."+r.Config.SystemNamespace+".svc:8443/webhook/audit/alice-dev" {
		t.Fatalf("url = %s", got)
	}
}

func TestRequiredCertKeysDoNotIncludeAuditKubeconfig(t *testing.T) {
	for _, k := range requiredCertKeys {
		if k == names.KeyAuditWebhookKubeconfig {
			t.Fatal("audit kubeconfig must stay optional")
		}
	}
}

func testConfig() *config.Config {
	c := &config.Config{SystemNamespace: "vk8s-system"}
	return c
}
