package webhook

import (
	"fmt"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"vk8s.io/vk8s/internal/names"
)

// QuotaWebhookEntryName ValidatingWebhookConfiguration 内 webhook 条目名（必须是 FQDN）。
const QuotaWebhookEntryName = "quota." + names.Group

// DesiredQuotaConfiguration 平台配额即时拒绝的 ValidatingWebhookConfiguration 期望对象（设计 2026-10-25 §3.1）：
// url https://127.0.0.1:<port>/validate-quota，caBundle=内部 CA；拦 pods CREATE 与 persistentvolumeclaims CREATE/UPDATE。
// failurePolicy Ignore：syncer 不在时退化为「宿主拒绝后写 Event」，绝不阻塞创建；sideEffects None，dry-run 也只读。
// Service 不在这里：ServiceMutator 在准入时已同步创建宿主 Service，宿主 RQ 当场拒绝并由它转成 403。
func DesiredQuotaConfiguration(caBundle []byte, port int) *admissionregistrationv1.ValidatingWebhookConfiguration {
	url := fmt.Sprintf("https://127.0.0.1:%d%s", port, names.QuotaWebhookPath)
	rule := func(resource string, ops ...admissionregistrationv1.OperationType) admissionregistrationv1.RuleWithOperations {
		return admissionregistrationv1.RuleWithOperations{
			Operations: ops,
			Rule: admissionregistrationv1.Rule{
				APIGroups:   []string{""},
				APIVersions: []string{"v1"},
				Resources:   []string{resource},
				Scope:       ptr.To(admissionregistrationv1.NamespacedScope),
			},
		}
	}
	return &admissionregistrationv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:   names.QuotaWebhookName,
			Labels: map[string]string{names.LabelManaged: "true"},
		},
		Webhooks: []admissionregistrationv1.ValidatingWebhook{{
			Name: QuotaWebhookEntryName,
			ClientConfig: admissionregistrationv1.WebhookClientConfig{
				URL:      ptr.To(url),
				CABundle: caBundle,
			},
			Rules: []admissionregistrationv1.RuleWithOperations{
				rule("pods", admissionregistrationv1.Create),
				rule("persistentvolumeclaims", admissionregistrationv1.Create, admissionregistrationv1.Update),
			},
			FailurePolicy:           ptr.To(admissionregistrationv1.Ignore),
			MatchPolicy:             ptr.To(admissionregistrationv1.Equivalent),
			SideEffects:             ptr.To(admissionregistrationv1.SideEffectClassNone),
			TimeoutSeconds:          ptr.To[int32](5),
			AdmissionReviewVersions: []string{"v1"},
		}},
	}
}

// ApplyQuotaConfiguration 把期望值写进现有对象（保留 resourceVersion 等），返回是否有改动。
func ApplyQuotaConfiguration(desired, actual *admissionregistrationv1.ValidatingWebhookConfiguration) bool {
	changed := false
	if actual.Labels[names.LabelManaged] != "true" {
		if actual.Labels == nil {
			actual.Labels = map[string]string{}
		}
		actual.Labels[names.LabelManaged] = "true"
		changed = true
	}
	if len(actual.Webhooks) != 1 || !validatingWebhookEqual(actual.Webhooks[0], desired.Webhooks[0]) {
		actual.Webhooks = desired.DeepCopy().Webhooks // 深拷贝，避免 actual 与 desired 共享指针字段
		changed = true
	}
	return changed
}

// validatingWebhookEqual 比较我们关心的字段（apiserver 会补默认值，不能整体 DeepEqual）。
func validatingWebhookEqual(a, b admissionregistrationv1.ValidatingWebhook) bool {
	if a.Name != b.Name ||
		ptr.Deref(a.ClientConfig.URL, "") != ptr.Deref(b.ClientConfig.URL, "") ||
		string(a.ClientConfig.CABundle) != string(b.ClientConfig.CABundle) ||
		ptr.Deref(a.FailurePolicy, "") != ptr.Deref(b.FailurePolicy, "") ||
		ptr.Deref(a.SideEffects, "") != ptr.Deref(b.SideEffects, "") ||
		ptr.Deref(a.TimeoutSeconds, 0) != ptr.Deref(b.TimeoutSeconds, 0) ||
		a.NamespaceSelector != nil || a.ObjectSelector != nil ||
		len(a.Rules) != len(b.Rules) {
		return false
	}
	for i := range a.Rules {
		ra, rb := a.Rules[i], b.Rules[i]
		if len(ra.Operations) != len(rb.Operations) ||
			len(ra.APIGroups) != 1 || ra.APIGroups[0] != rb.APIGroups[0] ||
			len(ra.APIVersions) != 1 || ra.APIVersions[0] != rb.APIVersions[0] ||
			len(ra.Resources) != 1 || ra.Resources[0] != rb.Resources[0] {
			return false
		}
		for j := range ra.Operations {
			if ra.Operations[j] != rb.Operations[j] {
				return false
			}
		}
	}
	return true
}
