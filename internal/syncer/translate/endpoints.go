package translate

import (
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/equality"
)

const (
	// LabelEndpointsManagedBy kcm 的 endpoints 控制器打在自己创建的 Endpoints 上的标签（值 endpoint-controller）。
	LabelEndpointsManagedBy = "endpoints.kubernetes.io/managed-by"
	// LabelSkipMirror 宿主 endpointslicemirroring 控制器看到该标签后不再把 Endpoints 镜像成 EndpointSlice，
	// EndpointSlice 由 syncer 自己从虚拟侧同步。
	LabelSkipMirror = "endpointslice.kubernetes.io/skip-mirror"
	// LabelEndpointSliceManagedBy 宿主 EndpointSlice 的 managed-by 值，让宿主 endpointslice 控制器忽略它。
	LabelEndpointSliceManagedBy = "endpointslice.kubernetes.io/managed-by"
	// EndpointSliceManager syncer 作为 EndpointSlice 管理者的标识。
	EndpointSliceManager = "vk8s.io/syncer"
)

// TranslateEndpoints 虚 → 宿 Endpoints（仅用于无 selector 的 Service）。
// targetRef 指向虚拟 Pod，在宿主无意义，丢弃。
func TranslateEndpoints(vep *corev1.Endpoints, hostNamespace string) *corev1.Endpoints {
	hep := &corev1.Endpoints{
		ObjectMeta: HostObjectMeta(vep, hostNamespace),
		Subsets:    make([]corev1.EndpointSubset, 0, len(vep.Subsets)),
	}
	hep.Labels[LabelSkipMirror] = "true"
	// 虚拟侧若带 kcm 的 managed-by 标签，宿主副本不能保留，否则会被 IsManaged 判为 kcm 的对象。
	delete(hep.Labels, LabelEndpointsManagedBy)
	for _, s := range vep.Subsets {
		ss := *s.DeepCopy()
		for i := range ss.Addresses {
			ss.Addresses[i].TargetRef = nil
		}
		for i := range ss.NotReadyAddresses {
			ss.NotReadyAddresses[i].TargetRef = nil
		}
		hep.Subsets = append(hep.Subsets, ss)
	}
	return hep
}

// ApplyEndpointsMutations 覆盖 subsets。
func ApplyEndpointsMutations(desired, actual *corev1.Endpoints) bool {
	changed := MergeMeta(&actual.ObjectMeta, desired.ObjectMeta)
	if !equality.Semantic.DeepEqual(actual.Subsets, desired.Subsets) {
		actual.Subsets = desired.Subsets
		changed = true
	}
	return changed
}

// TranslateEndpointSlice 虚 → 宿 EndpointSlice：名字与 service-name 标签映射到宿主名，managed-by 改为 syncer。
func TranslateEndpointSlice(ves *discoveryv1.EndpointSlice, hostNamespace string) *discoveryv1.EndpointSlice {
	hes := &discoveryv1.EndpointSlice{
		ObjectMeta:  HostObjectMeta(ves, hostNamespace),
		AddressType: ves.AddressType,
		Endpoints:   make([]discoveryv1.Endpoint, 0, len(ves.Endpoints)),
		Ports:       append([]discoveryv1.EndpointPort(nil), ves.Ports...),
	}
	if svc := ves.Labels[discoveryv1.LabelServiceName]; svc != "" {
		hes.Labels[discoveryv1.LabelServiceName] = HostName(svc, ves.Namespace)
	}
	hes.Labels[LabelEndpointSliceManagedBy] = EndpointSliceManager
	for _, e := range ves.Endpoints {
		ee := *e.DeepCopy()
		ee.TargetRef = nil
		hes.Endpoints = append(hes.Endpoints, ee)
	}
	return hes
}

// ApplyEndpointSliceMutations 覆盖 endpoints/ports（addressType 不可变）。
func ApplyEndpointSliceMutations(desired, actual *discoveryv1.EndpointSlice) bool {
	changed := MergeMeta(&actual.ObjectMeta, desired.ObjectMeta)
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
