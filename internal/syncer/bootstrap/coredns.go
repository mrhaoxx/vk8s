package bootstrap

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"vk8s.io/vk8s/internal/names"
)

const (
	// CoreDNSImage 投放到虚拟集群的 CoreDNS 镜像。
	CoreDNSImage = "registry.k8s.io/coredns/coredns:v1.12.1"
	// coreDNSAppLabel k8s-app 标签值，Service selector 与 Deployment selector 共用。
	coreDNSAppLabel    = "kube-dns"
	coreDNSClusterRole = "system:coredns"
	corefileKey        = "Corefile"
)

// corefile Corefile 内容：kubernetes 插件读虚拟 apiserver，集群外名字转发到宿主 DNS。
func corefile(hostDNSServiceIP string) string {
	return fmt.Sprintf(`.:53 {
    errors
    health
    ready
    prometheus :9153
    kubernetes cluster.local in-addr.arpa ip6.arpa {
        pods insecure
        fallthrough in-addr.arpa ip6.arpa
    }
    forward . %s
    cache 30
    loop
    reload
    loadbalance
}
`, hostDNSServiceIP)
}

func coreDNSLabels() map[string]string {
	return map[string]string{
		"k8s-app":                       coreDNSAppLabel,
		"kubernetes.io/cluster-service": "true",
		"kubernetes.io/name":            "CoreDNS",
		names.LabelManaged:              "true",
	}
}

// ensureCoreDNS 向虚拟集群 kube-system 投放 CoreDNS：SA、ClusterRole、ClusterRoleBinding、Corefile、Deployment、Service。
// CoreDNS Pod 作为普通租户 Pod 被 syncer 自己同步到宿主；Service kube-dns 的 ClusterIP 经 webhook 对齐后，
// 即 Pod 翻译器写进所有 Pod 的 nameserver。
func (r *Runner) ensureCoreDNS(ctx context.Context) error {
	ns := names.VirtualSystemNamespace

	sa := &corev1.ServiceAccount{}
	sa.Name, sa.Namespace = names.CoreDNSName, ns
	if err := r.ensure(ctx, sa, func() { setManaged(sa) }); err != nil {
		return err
	}

	cr := &rbacv1.ClusterRole{}
	cr.Name = coreDNSClusterRole
	if err := r.ensure(ctx, cr, func() {
		setManaged(cr)
		cr.Rules = []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"endpoints", "services", "pods", "namespaces"}, Verbs: []string{"list", "watch"}},
			{APIGroups: []string{"discovery.k8s.io"}, Resources: []string{"endpointslices"}, Verbs: []string{"list", "watch"}},
		}
	}); err != nil {
		return err
	}

	crb := &rbacv1.ClusterRoleBinding{}
	crb.Name = coreDNSClusterRole
	if err := r.ensure(ctx, crb, func() {
		setManaged(crb)
		crb.RoleRef = rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: coreDNSClusterRole}
		crb.Subjects = []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: names.CoreDNSName, Namespace: ns}}
	}); err != nil {
		return err
	}

	cm := &corev1.ConfigMap{}
	cm.Name, cm.Namespace = names.CoreDNSName, ns
	if err := r.ensure(ctx, cm, func() {
		setManaged(cm)
		cm.Data = map[string]string{corefileKey: corefile(r.HostDNSServiceIP)}
	}); err != nil {
		return err
	}

	dep := &appsv1.Deployment{}
	dep.Name, dep.Namespace = names.CoreDNSName, ns
	if err := r.ensure(ctx, dep, func() {
		dep.Labels = coreDNSLabels()
		dep.Spec.Replicas = ptr.To[int32](1)
		dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": coreDNSAppLabel}}
		dep.Spec.Strategy = appsv1.DeploymentStrategy{
			Type: appsv1.RollingUpdateDeploymentStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDeployment{
				MaxUnavailable: ptr.To(intstr.FromInt32(1)),
				MaxSurge:       ptr.To(intstr.FromInt32(0)),
			},
		}
		dep.Spec.Template = coreDNSPodTemplate()
	}); err != nil {
		return err
	}

	svc := &corev1.Service{}
	svc.Name, svc.Namespace = names.CoreDNSServiceName, ns
	return r.ensure(ctx, svc, func() {
		svc.Labels = coreDNSLabels()
		if svc.Annotations == nil {
			svc.Annotations = map[string]string{}
		}
		svc.Annotations["prometheus.io/port"] = "9153"
		svc.Annotations["prometheus.io/scrape"] = "true"
		svc.Spec.Type = corev1.ServiceTypeClusterIP
		svc.Spec.Selector = map[string]string{"k8s-app": coreDNSAppLabel}
		svc.Spec.Ports = []corev1.ServicePort{
			{Name: "dns", Port: 53, TargetPort: intstr.FromInt32(53), Protocol: corev1.ProtocolUDP},
			{Name: "dns-tcp", Port: 53, TargetPort: intstr.FromInt32(53), Protocol: corev1.ProtocolTCP},
			{Name: "metrics", Port: 9153, TargetPort: intstr.FromInt32(9153), Protocol: corev1.ProtocolTCP},
		}
	})
}

// coreDNSPodTemplate 上游 coredns 清单的精简版；securityContext 满足 PSA restricted（宿主 ns 可能是 restricted）。
// dnsPolicy Default：CoreDNS 自己用宿主节点的 DNS，不能指向自己。
func coreDNSPodTemplate() corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{"k8s-app": coreDNSAppLabel},
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: names.CoreDNSName,
			DNSPolicy:          corev1.DNSDefault,
			SecurityContext: &corev1.PodSecurityContext{
				// coredns 镜像的 USER 是非数字的 "nonroot"，只写 runAsNonRoot 时 kubelet 无法校验，
				// 会报 CreateContainerConfigError；显式给出 uid/gid（distroless nonroot = 65532）。
				RunAsNonRoot:   ptr.To(true),
				RunAsUser:      ptr.To[int64](65532),
				RunAsGroup:     ptr.To[int64](65532),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:            "coredns",
				Image:           CoreDNSImage,
				ImagePullPolicy: corev1.PullIfNotPresent,
				Args:            []string{"-conf", "/etc/coredns/" + corefileKey},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("70Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("170Mi"),
					},
				},
				Ports: []corev1.ContainerPort{
					{Name: "dns", ContainerPort: 53, Protocol: corev1.ProtocolUDP},
					{Name: "dns-tcp", ContainerPort: 53, Protocol: corev1.ProtocolTCP},
					{Name: "metrics", ContainerPort: 9153, Protocol: corev1.ProtocolTCP},
				},
				VolumeMounts: []corev1.VolumeMount{{Name: "config-volume", MountPath: "/etc/coredns", ReadOnly: true}},
				LivenessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
						Path: "/health", Port: intstr.FromInt32(8080), Scheme: corev1.URISchemeHTTP,
					}},
					InitialDelaySeconds: 60, TimeoutSeconds: 5, SuccessThreshold: 1, FailureThreshold: 5,
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{
						Path: "/ready", Port: intstr.FromInt32(8181), Scheme: corev1.URISchemeHTTP,
					}},
				},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					ReadOnlyRootFilesystem:   ptr.To(true),
					Capabilities: &corev1.Capabilities{
						Add:  []corev1.Capability{"NET_BIND_SERVICE"},
						Drop: []corev1.Capability{"ALL"},
					},
				},
			}},
			Volumes: []corev1.Volume{{
				Name: "config-volume",
				VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: names.CoreDNSName},
					Items:                []corev1.KeyToPath{{Key: corefileKey, Path: corefileKey}},
				}},
			}},
		},
	}
}
