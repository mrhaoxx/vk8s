package metrics

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	apiregistrationv1 "k8s.io/kube-aggregator/pkg/apis/apiregistration/v1"
	"k8s.io/utils/ptr"

	"vk8s.io/vk8s/internal/names"
)

// 以下三个对象由 bootstrap 幂等投放到虚拟集群：APIService 把 metrics.k8s.io 聚合到 kube-system/metrics-server，
// 该 Service 无 selector，EndpointSlice 手写指向 syncer 自身 POD_IP:8443（apiserver 聚合层经 egress 隧道到达）。

var managedLabels = map[string]string{names.LabelManaged: "true"}

// DesiredAPIService v1beta1.metrics.k8s.io。
func DesiredAPIService() *apiregistrationv1.APIService {
	return &apiregistrationv1.APIService{
		ObjectMeta: metav1.ObjectMeta{Name: names.MetricsAPIServiceName, Labels: managedLabels},
		Spec: apiregistrationv1.APIServiceSpec{
			Group:                 groupName,
			Version:               versionName,
			GroupPriorityMinimum:  100,
			VersionPriority:       100,
			InsecureSkipTLSVerify: true,
			Service: &apiregistrationv1.ServiceReference{
				Namespace: names.VirtualSystemNamespace,
				Name:      names.MetricsServiceName,
				Port:      ptr.To[int32](names.ServicePort),
			},
		},
	}
}

// ApplyAPIService 期望 spec 覆盖，返回是否有改动。
func ApplyAPIService(desired, actual *apiregistrationv1.APIService) bool {
	changed := false
	if actual.Labels[names.LabelManaged] != "true" {
		if actual.Labels == nil {
			actual.Labels = map[string]string{}
		}
		actual.Labels[names.LabelManaged] = "true"
		changed = true
	}
	if !equality.Semantic.DeepEqual(actual.Spec, desired.Spec) {
		actual.Spec = desired.Spec
		changed = true
	}
	return changed
}

// DesiredService kube-system/metrics-server，无 selector，443 → 8443。
func DesiredService() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.MetricsServiceName,
			Namespace: names.VirtualSystemNamespace,
			Labels:    managedLabels,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{
				Name:       "https",
				Port:       names.ServicePort,
				TargetPort: intstr.FromInt32(names.SyncerMetricsAPIPort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

// ApplyService 只校正 ports 与 selector（ClusterIP 由虚拟 apiserver 分配，无需对齐宿主）。
func ApplyService(desired, actual *corev1.Service) bool {
	changed := false
	if actual.Labels[names.LabelManaged] != "true" {
		if actual.Labels == nil {
			actual.Labels = map[string]string{}
		}
		actual.Labels[names.LabelManaged] = "true"
		changed = true
	}
	if len(actual.Spec.Selector) > 0 {
		actual.Spec.Selector = nil
		changed = true
	}
	if !equality.Semantic.DeepEqual(actual.Spec.Ports, desired.Spec.Ports) {
		actual.Spec.Ports = desired.Spec.Ports
		changed = true
	}
	return changed
}

// DesiredEndpointSlice 指向 syncer 自身 podIP:8443。
// apiserver 聚合层（kube-aggregator ≥1.33）只通过 EndpointSlice 解析后端，不读 Endpoints；
// Service 无 selector，slice 由 syncer 自己维护，managed-by 标成 vk8s 以免被 endpointslice 控制器接管。
func DesiredEndpointSlice(podIP string) *discoveryv1.EndpointSlice {
	addressType := discoveryv1.AddressTypeIPv4
	if strings.Contains(podIP, ":") {
		addressType = discoveryv1.AddressTypeIPv6
	}
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.MetricsServiceName,
			Namespace: names.VirtualSystemNamespace,
			Labels: map[string]string{
				names.LabelManaged:           "true",
				discoveryv1.LabelServiceName: names.MetricsServiceName,
				discoveryv1.LabelManagedBy:   "vk8s-syncer",
			},
		},
		AddressType: addressType,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses:  []string{podIP},
			Conditions: discoveryv1.EndpointConditions{Ready: ptr.To(true)},
		}},
		Ports: []discoveryv1.EndpointPort{{
			// 端口名必须与 Service 端口名一致，聚合层按名字匹配
			Name:     ptr.To("https"),
			Port:     ptr.To[int32](names.SyncerMetricsAPIPort),
			Protocol: ptr.To(corev1.ProtocolTCP),
		}},
	}
}

// ApplyEndpointSlice pod IP 变化时更新；addressType 不可变，只在新建时设置。
func ApplyEndpointSlice(desired, actual *discoveryv1.EndpointSlice) bool {
	changed := false
	for k, v := range desired.Labels {
		if actual.Labels[k] != v {
			if actual.Labels == nil {
				actual.Labels = map[string]string{}
			}
			actual.Labels[k] = v
			changed = true
		}
	}
	if actual.AddressType == "" {
		actual.AddressType = desired.AddressType
		changed = true
	}
	if !equality.Semantic.DeepEqual(actual.Endpoints, desired.Endpoints) {
		actual.Endpoints = desired.Endpoints
		changed = true
	}
	if !equality.Semantic.DeepEqual(actual.Ports, desired.Ports) {
		actual.Ports = desired.Ports
		changed = true
	}
	return changed
}
