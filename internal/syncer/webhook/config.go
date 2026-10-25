package webhook

import (
	"fmt"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"vk8s.io/vk8s/internal/names"
)

// WebhookName MutatingWebhookConfiguration 内 webhook 条目名（必须是 FQDN）。
const WebhookName = "clusterip.services." + names.Group

// DesiredConfiguration bootstrap 用的 MutatingWebhookConfiguration 期望对象：
// url https://127.0.0.1:<port><path>（apiserver 与 syncer 同 pod），caBundle=内部 CA，
// 只拦 services CREATE，failurePolicy Fail（宁可建不出 Service 也不能 IP 不一致），sideEffects NoneOnDryRun。
// 不设 namespaceSelector：kube-system 的 kube-dns 也必须对齐。
func DesiredConfiguration(caBundle []byte, port int) *admissionregistrationv1.MutatingWebhookConfiguration {
	url := fmt.Sprintf("https://127.0.0.1:%d%s", port, names.ServiceWebhookPath)
	return &admissionregistrationv1.MutatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   names.ServiceWebhookName,
			Labels: map[string]string{names.LabelManaged: "true"},
		},
		Webhooks: []admissionregistrationv1.MutatingWebhook{{
			Name: WebhookName,
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL:      ptr.To(url),
				CABundle: caBundle,
			},
			Rules: []admissionregistrationv1.RuleWithOperations{{
				Operations: []admissionregistrationv1.OperationType{admissionregistrationv1.Create},
				Rule: admissionregistrationv1.Rule{
					APIGroups:   []string{""},
					APIVersions: []string{"v1"},
					Resources:   []string{"services"},
					Scope:       ptr.To(admissionregistrationv1.NamespacedScope),
				},
			}},
			FailurePolicy:           ptr.To(admissionregistrationv1.Fail),
			MatchPolicy:             ptr.To(admissionregistrationv1.Equivalent),
			SideEffects:             ptr.To(admissionregistrationv1.SideEffectClassNoneOnDryRun),
			TimeoutSeconds:          ptr.To[int32](10),
			AdmissionReviewVersions: []string{"v1"},
			ReinvocationPolicy:      ptr.To(admissionregistrationv1.NeverReinvocationPolicy),
		}},
	}
}

// ApplyConfiguration 把期望值写进现有对象（保留 resourceVersion 等），返回是否有改动。
func ApplyConfiguration(desired, actual *admissionregistrationv1.MutatingWebhookConfiguration) bool {
	changed := false
	if actual.Labels[names.LabelManaged] != "true" {
		if actual.Labels == nil {
			actual.Labels = map[string]string{}
		}
		actual.Labels[names.LabelManaged] = "true"
		changed = true
	}
	if len(actual.Webhooks) != 1 || !webhookEqual(actual.Webhooks[0], desired.Webhooks[0]) {
		actual.Webhooks = desired.Webhooks
		changed = true
	}
	return changed
}

// webhookEqual 比较我们关心的字段（apiserver 会补默认值，不能整体 DeepEqual）。
func webhookEqual(a, b admissionregistrationv1.MutatingWebhook) bool {
	if a.Name != b.Name ||
		ptr.Deref(a.ClientConfig.URL, "") != ptr.Deref(b.ClientConfig.URL, "") ||
		string(a.ClientConfig.CABundle) != string(b.ClientConfig.CABundle) ||
		ptr.Deref(a.FailurePolicy, "") != ptr.Deref(b.FailurePolicy, "") ||
		ptr.Deref(a.SideEffects, "") != ptr.Deref(b.SideEffects, "") ||
		a.NamespaceSelector != nil || a.ObjectSelector != nil ||
		len(a.Rules) != 1 || len(b.Rules) != 1 {
		return false
	}
	ra, rb := a.Rules[0], b.Rules[0]
	return len(ra.Operations) == 1 && ra.Operations[0] == rb.Operations[0] &&
		len(ra.APIGroups) == 1 && ra.APIGroups[0] == rb.APIGroups[0] &&
		len(ra.APIVersions) == 1 && ra.APIVersions[0] == rb.APIVersions[0] &&
		len(ra.Resources) == 1 && ra.Resources[0] == rb.Resources[0]
}
