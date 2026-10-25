package translate

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"vk8s.io/vk8s/internal/names"
)

// IsSystemService 由控制平面 / syncer 自己维护、不得下沉到宿主的虚拟 Service：
// default/kubernetes 的 ClusterIP 就是宿主 vc-<name> Service；kube-system/metrics-server 指向 syncer 自身。
func IsSystemService(namespace, name string) bool {
	switch {
	case namespace == metav1.NamespaceDefault && name == "kubernetes":
		return true
	case namespace == names.VirtualSystemNamespace && name == names.MetricsServiceName:
		return true
	}
	return false
}

// IsHeadless Service 是否 headless。
func IsHeadless(svc *corev1.Service) bool {
	return svc.Spec.ClusterIP == corev1.ClusterIPNone
}

// TranslateService 虚 → 宿 Service：名字映射、selector 追加 vnamespace、ClusterIP/nodePort 显式带上
// （webhook 已对齐过，reconciler 重建时也要求宿主沿用同一 IP）。
func TranslateService(vsvc *corev1.Service, hostNamespace string) *corev1.Service {
	hsvc := &corev1.Service{
		ObjectMeta: HostObjectMeta(vsvc, hostNamespace),
		Spec:       *vsvc.Spec.DeepCopy(),
	}
	if hsvc.Spec.Selector != nil {
		hsvc.Spec.Selector[names.LabelVNamespace] = vsvc.Namespace
	}
	if hsvc.Spec.Type == corev1.ServiceTypeExternalName {
		hsvc.Spec.ClusterIP = ""
		hsvc.Spec.ClusterIPs = nil
	}
	return hsvc
}

// ApplyServiceMutations 把期望的可变字段应用到现有宿主 Service；ClusterIP/ClusterIPs/ipFamilies 等
// 不可变字段保持宿主现值。返回是否有改动。
func ApplyServiceMutations(desired, actual *corev1.Service) bool {
	changed := MergeMeta(&actual.ObjectMeta, desired.ObjectMeta)
	next := *desired.Spec.DeepCopy()
	// 不可变 / 由宿主分配的字段沿用现值
	next.ClusterIP = actual.Spec.ClusterIP
	next.ClusterIPs = actual.Spec.ClusterIPs
	next.IPFamilies = actual.Spec.IPFamilies
	next.IPFamilyPolicy = actual.Spec.IPFamilyPolicy
	next.LoadBalancerClass = actual.Spec.LoadBalancerClass
	if next.HealthCheckNodePort == 0 {
		next.HealthCheckNodePort = actual.Spec.HealthCheckNodePort
	}
	// nodePort：虚拟侧未指定时沿用宿主已分配值
	for i := range next.Ports {
		if next.Ports[i].NodePort != 0 {
			continue
		}
		for _, ap := range actual.Spec.Ports {
			if ap.Name == next.Ports[i].Name && ap.Port == next.Ports[i].Port && ap.Protocol == next.Ports[i].Protocol {
				next.Ports[i].NodePort = ap.NodePort
			}
		}
	}
	if next.Type != corev1.ServiceTypeNodePort && next.Type != corev1.ServiceTypeLoadBalancer {
		for i := range next.Ports {
			next.Ports[i].NodePort = 0
		}
		next.ExternalTrafficPolicy = ""
		next.HealthCheckNodePort = 0
	}
	if !equality.Semantic.DeepEqual(actual.Spec, next) {
		actual.Spec = next
		changed = true
	}
	return changed
}

// ServiceClusterIPMismatch 两侧 ClusterIP 是否不一致（均非空且非 None 时才比较）。
func ServiceClusterIPMismatch(vsvc, hsvc *corev1.Service) bool {
	v, h := vsvc.Spec.ClusterIP, hsvc.Spec.ClusterIP
	if v == "" || h == "" || v == corev1.ClusterIPNone || h == corev1.ClusterIPNone {
		return false
	}
	return v != h
}

// RealignedVirtualService 以宿主 Service 的 ClusterIP(s)/nodePort 为准重建虚拟 Service（兜底路径）。
func RealignedVirtualService(vsvc, hsvc *corev1.Service) *corev1.Service {
	out := vsvc.DeepCopy()
	out.ResourceVersion = ""
	out.UID = ""
	out.CreationTimestamp = metav1.Time{}
	out.DeletionTimestamp = nil
	out.DeletionGracePeriodSeconds = nil
	out.ManagedFields = nil
	out.Status = corev1.ServiceStatus{}
	out.Spec.ClusterIP = hsvc.Spec.ClusterIP
	out.Spec.ClusterIPs = append([]string(nil), hsvc.Spec.ClusterIPs...)
	for i := range out.Spec.Ports {
		for _, hp := range hsvc.Spec.Ports {
			if hp.Name == out.Spec.Ports[i].Name && hp.Port == out.Spec.Ports[i].Port && hp.Protocol == out.Spec.Ports[i].Protocol {
				out.Spec.Ports[i].NodePort = hp.NodePort
			}
		}
	}
	return out
}

// ServiceStatusChanged 宿主 status 是否需要回写到虚拟对象；需要则就地更新虚拟对象并返回 true。
func ServiceStatusChanged(vsvc, hsvc *corev1.Service) bool {
	if equality.Semantic.DeepEqual(vsvc.Status, hsvc.Status) {
		return false
	}
	vsvc.Status = *hsvc.Status.DeepCopy()
	return true
}
