package resources

import (
	"context"

	networkingv1 "k8s.io/api/networking/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	"vk8s.io/vk8s/internal/syncer/translate"
)

// setupIngress Ingress 虚 → 宿（host 白名单、名字改写、自动补 TLS）；宿 → 虚 status.loadBalancer。
func setupIngress(mgr ctrl.Manager, c *Context) error {
	env := translate.IngressEnv{
		HostNamespace: c.HostNamespace,
		VCName:        c.VCName,
		AppsDomain:    c.AppsDomain,
		AppsTLSSecret: c.AppsTLSSecret,
	}
	d := &downSync[*networkingv1.Ingress]{
		c:        c,
		resource: "ingress",
		newObj:   func() *networkingv1.Ingress { return &networkingv1.Ingress{} },
		translate: func(_ context.Context, v *networkingv1.Ingress) (*networkingv1.Ingress, error) {
			return translate.TranslateIngress(v, env)
		},
		mutate:    translate.ApplyIngressMutations,
		writeback: translate.IngressStatusChanged,
	}
	return d.setup(mgr)
}
