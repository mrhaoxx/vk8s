package controlplane

import (
	"strings"
	"testing"
)

func TestAPIServerArgsAuditFlagsOnlyWhenEnabled(t *testing.T) {
	base := Options{VClusterName: "a", CertsDir: "/certs", DataDir: "/data", RunDir: "/run/vk8s", ServiceClusterIPRange: "10.96.0.0/12"}
	off := strings.Join(APIServerArgs(base), "\n")
	if strings.Contains(off, "--audit-") {
		t.Fatalf("no audit flags when disabled:\n%s", off)
	}
	on := base
	on.AuditWebhookKubeconfig = "/certs/audit-webhook.kubeconfig"
	got := strings.Join(APIServerArgs(on), "\n")
	for _, want := range []string{
		"--audit-policy-file=/run/vk8s/audit-policy.yaml",
		"--audit-webhook-config-file=/certs/audit-webhook.kubeconfig",
		"--audit-webhook-mode=batch",
		"--audit-webhook-batch-max-size=400",
		"--audit-webhook-batch-max-wait=2s",
		"--audit-webhook-batch-buffer-size=20000",
		"--audit-webhook-initial-backoff=2s",
		"--audit-webhook-truncate-enabled=true",
		"--audit-webhook-truncate-max-event-size=102400",
		"--audit-webhook-truncate-max-batch-size=10485760",
		"--audit-webhook-version=audit.k8s.io/v1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s", want)
		}
	}
	if p := on.Paths(); p.AuditPolicyFile != "/run/vk8s/audit-policy.yaml" {
		t.Fatalf("AuditPolicyFile = %s", p.AuditPolicyFile)
	}
}
