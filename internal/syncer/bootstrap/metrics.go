package bootstrap

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/metrics"
)

// ensureMetrics APIService + 无 selector Service + 指向 syncer POD_IP:8443 的 EndpointSlice。
func (r *Runner) ensureMetrics(ctx context.Context) error {
	svc := &corev1.Service{}
	svc.Name = names.MetricsServiceName
	svc.Namespace = names.VirtualSystemNamespace
	desiredSvc := metrics.DesiredService()
	if err := r.ensure(ctx, svc, func() { metrics.ApplyService(desiredSvc, svc) }); err != nil {
		return err
	}

	// 聚合层只用 EndpointSlice 解析后端（kube-aggregator ≥1.33），不写 Endpoints。
	eps := &discoveryv1.EndpointSlice{}
	eps.Name = names.MetricsServiceName
	eps.Namespace = names.VirtualSystemNamespace
	desiredEPS := metrics.DesiredEndpointSlice(r.PodIP)
	if err := r.ensure(ctx, eps, func() { metrics.ApplyEndpointSlice(desiredEPS, eps) }); err != nil {
		return err
	}

	apiSvc := &apiregistrationv1.APIService{}
	apiSvc.Name = names.MetricsAPIServiceName
	desiredAPI := metrics.DesiredAPIService()
	return r.ensure(ctx, apiSvc, func() { metrics.ApplyAPIService(desiredAPI, apiSvc) })
}
