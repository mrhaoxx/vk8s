// Package webhook 实现 Service ClusterIP 对齐的 MutatingWebhook（设计文档 7.4）。
//
// 虚拟集群与宿主使用相同的 Service CIDR。Service CREATE 时，webhook 同步地先在宿主创建翻译后的
// Service，取得宿主分配的 ClusterIP / nodePort，以 JSON patch 写回虚拟对象后再落库，两边 IP 永远一致。
package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"

	"github.com/go-logr/logr"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crwebhook "sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/translate"
)

// ServiceMutator 处理 Service CREATE 的 admission handler。
type ServiceMutator struct {
	Log logr.Logger
	// Host 直连宿主集群的 client（不经 cache：admission 必须读到刚创建对象的真实 IP）。
	Host          client.Client
	HostNamespace string
}

var _ admission.Handler = (*ServiceMutator)(nil)

// Handle 实现 admission.Handler。
func (m *ServiceMutator) Handle(ctx context.Context, req admission.Request) admission.Response {
	if req.Operation != admissionv1.Create {
		return admission.Allowed("")
	}
	svc := &corev1.Service{}
	if err := json.Unmarshal(req.Object.Raw, svc); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("decode service: %w", err))
	}
	if svc.Namespace == "" {
		svc.Namespace = req.Namespace
	}
	// 系统 Service 由 bootstrap 以显式 IP 创建，不在宿主对应
	if translate.IsSystemService(svc.Namespace, svc.Name) {
		return admission.Allowed("system service")
	}
	// Headless / ExternalName 没有 ClusterIP 需要对齐，直接放行，由 reconciler 下沉
	if translate.IsHeadless(svc) || svc.Spec.Type == corev1.ServiceTypeExternalName {
		return admission.Allowed("no clusterIP to align")
	}
	// sideEffects=NoneOnDryRun：dry-run 不碰宿主
	if req.DryRun != nil && *req.DryRun {
		return admission.Allowed("dry run")
	}
	if svc.Name == "" {
		return admission.Denied("vk8s: metadata.generateName is not supported for Services; set metadata.name")
	}

	hsvc := translate.TranslateService(svc, m.HostNamespace)
	if err := m.Host.Create(ctx, hsvc); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			// 宿主拒绝（IP 已被占用、配额超限等）→ 原话返回给用户
			return admission.Denied(fmt.Sprintf("vk8s: host cluster rejected the service: %v", err))
		}
		existing := &corev1.Service{}
		if gerr := m.Host.Get(ctx, client.ObjectKey{Namespace: m.HostNamespace, Name: hsvc.Name}, existing); gerr != nil {
			return admission.Errored(http.StatusInternalServerError, fmt.Errorf("read existing host service: %w", gerr))
		}
		if !translate.IsManagedFor(existing, svc) {
			return admission.Denied(fmt.Sprintf("vk8s: host object %s/%s already exists and is not managed for this service", m.HostNamespace, hsvc.Name))
		}
		// 同名虚拟 Service 刚被删、宿主对象尚未清理：沿用其 IP；用户显式指定了不同 IP 则拒绝
		if svc.Spec.ClusterIP != "" && svc.Spec.ClusterIP != existing.Spec.ClusterIP {
			return admission.Denied(fmt.Sprintf("vk8s: clusterIP %s is not available on the host cluster (previous host service still holds %s); retry shortly or omit clusterIP",
				svc.Spec.ClusterIP, existing.Spec.ClusterIP))
		}
		hsvc = existing
	}

	mutated := svc.DeepCopy()
	mutated.Spec.ClusterIP = hsvc.Spec.ClusterIP
	mutated.Spec.ClusterIPs = append([]string(nil), hsvc.Spec.ClusterIPs...)
	if len(hsvc.Spec.IPFamilies) > 0 {
		mutated.Spec.IPFamilies = append([]corev1.IPFamily(nil), hsvc.Spec.IPFamilies...)
	}
	if hsvc.Spec.IPFamilyPolicy != nil {
		p := *hsvc.Spec.IPFamilyPolicy
		mutated.Spec.IPFamilyPolicy = &p
	}
	if mutated.Spec.Type == corev1.ServiceTypeNodePort || mutated.Spec.Type == corev1.ServiceTypeLoadBalancer {
		for i := range mutated.Spec.Ports {
			p := &mutated.Spec.Ports[i]
			for _, hp := range hsvc.Spec.Ports {
				if hp.Name == p.Name && hp.Port == p.Port && hp.Protocol == p.Protocol {
					p.NodePort = hp.NodePort
				}
			}
		}
		if hsvc.Spec.HealthCheckNodePort != 0 {
			mutated.Spec.HealthCheckNodePort = hsvc.Spec.HealthCheckNodePort
		}
	}
	raw, err := json.Marshal(mutated)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	m.Log.V(1).Info("aligned service clusterIP", "service", svc.Namespace+"/"+svc.Name, "clusterIP", hsvc.Spec.ClusterIP)
	return admission.PatchResponseFromRaw(req.Object.Raw, raw)
}

// NewServer 构造监听 listen（host:port）的 webhook TLS server，证书用 syncer-serving。
func NewServer(listen, certsDir string) (crwebhook.Server, error) {
	host, port, err := SplitListen(listen)
	if err != nil {
		return nil, err
	}
	return crwebhook.NewServer(crwebhook.Options{
		Host:     host,
		Port:     port,
		CertDir:  certsDir,
		CertName: names.KeySyncerServingCert,
		KeyName:  names.KeySyncerServingKey,
	}), nil
}

// Register 把 ServiceMutator 挂到 names.ServiceWebhookPath。
func Register(srv crwebhook.Server, m *ServiceMutator) {
	srv.Register(names.ServiceWebhookPath, &admission.Webhook{Handler: m})
}

// SplitListen 解析 ":9443" / "0.0.0.0:9443" 形式的监听地址。
func SplitListen(listen string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return "", 0, fmt.Errorf("invalid listen address %q: %w", listen, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port in listen address %q", listen)
	}
	return host, port, nil
}
