package webhook

import (
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/utils/ptr"

	"vk8s.io/vk8s/internal/names"
)

func TestDesiredQuotaConfiguration(t *testing.T) {
	vwc := DesiredQuotaConfiguration([]byte("ca"), 9443)
	if vwc.Name != names.QuotaWebhookName || vwc.Labels[names.LabelManaged] != "true" {
		t.Fatalf("meta = %+v", vwc.ObjectMeta)
	}
	if len(vwc.Webhooks) != 1 {
		t.Fatalf("webhooks = %d", len(vwc.Webhooks))
	}
	w := vwc.Webhooks[0]
	if w.Name != QuotaWebhookEntryName || QuotaWebhookEntryName != "quota.vk8s.io" {
		t.Fatalf("entry name = %q", w.Name)
	}
	if ptr.Deref(w.ClientConfig.URL, "") != "https://127.0.0.1:9443/validate-quota" || string(w.ClientConfig.CABundle) != "ca" {
		t.Fatalf("clientConfig = %+v", w.ClientConfig)
	}
	if len(w.Rules) != 2 {
		t.Fatalf("rules = %+v", w.Rules)
	}
	wantRules := []struct {
		res string
		ops []admissionregistrationv1.OperationType
	}{
		{"pods", []admissionregistrationv1.OperationType{admissionregistrationv1.Create}},
		{"persistentvolumeclaims", []admissionregistrationv1.OperationType{admissionregistrationv1.Create, admissionregistrationv1.Update}},
	}
	for i, want := range wantRules {
		r := w.Rules[i]
		if len(r.Resources) != 1 || r.Resources[0] != want.res || len(r.APIGroups) != 1 || r.APIGroups[0] != "" ||
			len(r.APIVersions) != 1 || r.APIVersions[0] != "v1" || ptr.Deref(r.Scope, "") != admissionregistrationv1.NamespacedScope {
			t.Fatalf("rule %d = %+v", i, r)
		}
		if len(r.Operations) != len(want.ops) {
			t.Fatalf("rule %d operations = %v, want %v", i, r.Operations, want.ops)
		}
		for j := range want.ops {
			if r.Operations[j] != want.ops[j] {
				t.Fatalf("rule %d operations = %v, want %v", i, r.Operations, want.ops)
			}
		}
	}
	for _, r := range w.Rules {
		for _, res := range r.Resources {
			if res == "services" {
				t.Fatal("services must not be validated by the quota webhook (ServiceMutator handles them)")
			}
		}
	}
	if ptr.Deref(w.FailurePolicy, "") != admissionregistrationv1.Ignore {
		t.Fatalf("failurePolicy = %v, want Ignore", ptr.Deref(w.FailurePolicy, ""))
	}
	if ptr.Deref(w.MatchPolicy, "") != admissionregistrationv1.Equivalent || ptr.Deref(w.SideEffects, "") != admissionregistrationv1.SideEffectClassNone ||
		ptr.Deref(w.TimeoutSeconds, 0) != 5 || len(w.AdmissionReviewVersions) != 1 || w.AdmissionReviewVersions[0] != "v1" {
		t.Fatalf("policy fields = matchPolicy %v sideEffects %v timeout %v versions %v",
			ptr.Deref(w.MatchPolicy, ""), ptr.Deref(w.SideEffects, ""), ptr.Deref(w.TimeoutSeconds, 0), w.AdmissionReviewVersions)
	}
}

func TestApplyQuotaConfigurationIsIdempotent(t *testing.T) {
	desired := DesiredQuotaConfiguration([]byte("ca"), 9443)
	actual := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	if !ApplyQuotaConfiguration(desired, actual) {
		t.Fatal("first apply must report a change")
	}
	if len(actual.Webhooks) != 1 || actual.Webhooks[0].Name != desired.Webhooks[0].Name || len(actual.Webhooks[0].Rules) != 2 {
		t.Fatalf("actual after apply = %+v", actual.Webhooks)
	}
	if ApplyQuotaConfiguration(desired, actual) {
		t.Fatal("second apply must be a no-op")
	}
	actual.Webhooks[0].FailurePolicy = ptr.To(admissionregistrationv1.Fail)
	if !ApplyQuotaConfiguration(desired, actual) || ptr.Deref(actual.Webhooks[0].FailurePolicy, "") != admissionregistrationv1.Ignore {
		t.Fatalf("drift must be repaired, got %v", ptr.Deref(actual.Webhooks[0].FailurePolicy, ""))
	}
}
