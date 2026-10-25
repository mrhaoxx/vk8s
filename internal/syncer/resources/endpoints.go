package resources

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	"vk8s.io/vk8s/internal/syncer/translate"
)

// selectorlessServiceOnly 仅当虚拟 Service 存在、无 selector 且非系统 Service 时才下沉 Endpoints/EndpointSlice；
// 有 selector 的由宿主 kcm 自己算。
func selectorlessServiceOnly(c *Context, ctx context.Context, ns, svcName string) (skip bool, err error) {
	if svcName == "" || translate.IsSystemService(ns, svcName) {
		return true, nil
	}
	svc := &corev1.Service{}
	if err := c.Virtual.Get(ctx, types.NamespacedName{Namespace: ns, Name: svcName}, svc); err != nil {
		if isNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return len(svc.Spec.Selector) > 0 || svc.Spec.Type == corev1.ServiceTypeExternalName, nil
}

// setupEndpoints Endpoints 虚 → 宿，仅无 selector 的 Service。
func setupEndpoints(mgr ctrl.Manager, c *Context) error {
	d := &downSync[*corev1.Endpoints]{
		c:        c,
		resource: "endpoints",
		newObj:   func() *corev1.Endpoints { return &corev1.Endpoints{} },
		skip: func(ctx context.Context, v *corev1.Endpoints) (bool, error) {
			return selectorlessServiceOnly(c, ctx, v.Namespace, v.Name)
		},
		translate: func(_ context.Context, v *corev1.Endpoints) (*corev1.Endpoints, error) {
			return translate.TranslateEndpoints(v, c.HostNamespace), nil
		},
		mutate: translate.ApplyEndpointsMutations,
	}
	return d.setup(mgr)
}

// setupEndpointSlice EndpointSlice 虚 → 宿，仅无 selector 的 Service（虚拟 kcm 的 mirroring 控制器或用户创建）。
func setupEndpointSlice(mgr ctrl.Manager, c *Context) error {
	d := &downSync[*discoveryv1.EndpointSlice]{
		c:        c,
		resource: "endpointslice",
		newObj:   func() *discoveryv1.EndpointSlice { return &discoveryv1.EndpointSlice{} },
		skip: func(ctx context.Context, v *discoveryv1.EndpointSlice) (bool, error) {
			return selectorlessServiceOnly(c, ctx, v.Namespace, v.Labels[discoveryv1.LabelServiceName])
		},
		translate: func(_ context.Context, v *discoveryv1.EndpointSlice) (*discoveryv1.EndpointSlice, error) {
			return translate.TranslateEndpointSlice(v, c.HostNamespace), nil
		},
		mutate: translate.ApplyEndpointSliceMutations,
		needRecreate: func(desired, actual *discoveryv1.EndpointSlice) bool {
			return desired.AddressType != actual.AddressType
		},
	}
	return d.setup(mgr)
}
