package bootstrap

import (
	"context"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/webhook"
)

// ensureWebhook Service ClusterIP 对齐的 MutatingWebhookConfiguration。
func (r *Runner) ensureWebhook(ctx context.Context) error {
	desired := webhook.DesiredConfiguration(r.InternalCA, r.WebhookPort)
	mwc := &admissionregistrationv1.MutatingWebhookConfiguration{}
	mwc.Name = names.ServiceWebhookName
	return r.ensure(ctx, mwc, func() {
		webhook.ApplyConfiguration(desired, mwc)
	})
}

// ensureQuotaWebhook 平台配额即时拒绝的 ValidatingWebhookConfiguration（设计 2026-10-25）。
func (r *Runner) ensureQuotaWebhook(ctx context.Context) error {
	desired := webhook.DesiredQuotaConfiguration(r.InternalCA, r.WebhookPort)
	vwc := &admissionregistrationv1.ValidatingWebhookConfiguration{}
	vwc.Name = names.QuotaWebhookName
	return r.ensure(ctx, vwc, func() {
		webhook.ApplyQuotaConfiguration(desired, vwc)
	})
}
