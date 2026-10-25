package translate

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	nodev1 "k8s.io/api/node/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"vk8s.io/vk8s/internal/names"
)

// VirtualRuntimeClass 宿 → 虚 RuntimeClass 只读镜像（GPU 集群常用 `runtimeClassName: nvidia`，
// 虚拟 apiserver 的 RuntimeClass admission 要求对象存在）。scheduling 原样复制：虚拟侧没有调度器，
// 宿主侧 admission 会按自己的 RuntimeClass 再补一次 nodeSelector/tolerations。
func VirtualRuntimeClass(host *nodev1.RuntimeClass) *nodev1.RuntimeClass {
	rc := &nodev1.RuntimeClass{
		ObjectMeta: mirrorMeta(host),
		Handler:    host.Handler,
	}
	if host.Overhead != nil {
		rc.Overhead = host.Overhead.DeepCopy()
	}
	if host.Scheduling != nil {
		rc.Scheduling = host.Scheduling.DeepCopy()
	}
	return rc
}

// ApplyRuntimeClassMutations handler 与 overhead 不可变，宿主改了则删除重建；scheduling 与 metadata 覆盖。
// 返回 (changed, needRecreate)。
func ApplyRuntimeClassMutations(desired, actual *nodev1.RuntimeClass) (bool, bool) {
	if actual.Handler != desired.Handler || !equality.Semantic.DeepEqual(actual.Overhead, desired.Overhead) {
		return false, true
	}
	changed := applyMirrorMeta(&actual.ObjectMeta, desired.ObjectMeta)
	if !equality.Semantic.DeepEqual(actual.Scheduling, desired.Scheduling) {
		actual.Scheduling = desired.Scheduling
		changed = true
	}
	return changed, false
}

// VirtualLimitRange 宿主租户 ns 的 LimitRange 镜像到虚拟 namespace：两侧按同一规则补默认
// request/limit，虚拟 Pod 才有 requests（HPA、`describe node` 依赖），qosClass 也一致。
func VirtualLimitRange(host *corev1.LimitRange, namespace string) *corev1.LimitRange {
	return &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      host.Name,
			Namespace: namespace,
			Labels:    map[string]string{names.LabelManaged: "true"},
		},
		Spec: *host.Spec.DeepCopy(),
	}
}

// ApplyLimitRangeMutations 覆盖 labels 与 spec。
func ApplyLimitRangeMutations(desired, actual *corev1.LimitRange) bool {
	changed := false
	if actual.Labels[names.LabelManaged] != "true" {
		if actual.Labels == nil {
			actual.Labels = map[string]string{}
		}
		actual.Labels[names.LabelManaged] = "true"
		changed = true
	}
	if !equality.Semantic.DeepEqual(actual.Spec, desired.Spec) {
		actual.Spec = *desired.Spec.DeepCopy()
		changed = true
	}
	return changed
}

// mirrorMeta 宿 → 虚 只读镜像对象的 ObjectMeta：同名，复制 labels/annotations，打 managed 标签。
func mirrorMeta(host metav1.Object) metav1.ObjectMeta {
	labels := make(map[string]string, len(host.GetLabels())+1)
	for k, v := range host.GetLabels() {
		labels[k] = v
	}
	labels[names.LabelManaged] = "true"
	var ann map[string]string
	if len(host.GetAnnotations()) > 0 {
		ann = make(map[string]string, len(host.GetAnnotations()))
		for k, v := range host.GetAnnotations() {
			if k == "kubectl.kubernetes.io/last-applied-configuration" {
				continue
			}
			ann[k] = v
		}
	}
	return metav1.ObjectMeta{Name: host.GetName(), Labels: labels, Annotations: ann}
}

// applyMirrorMeta 覆盖虚拟镜像对象的 labels/annotations（用户改动被覆盖）。
func applyMirrorMeta(actual *metav1.ObjectMeta, desired metav1.ObjectMeta) bool {
	changed := false
	if !equality.Semantic.DeepEqual(actual.Labels, desired.Labels) {
		actual.Labels = desired.Labels
		changed = true
	}
	if !equality.Semantic.DeepEqual(actual.Annotations, desired.Annotations) {
		actual.Annotations = desired.Annotations
		changed = true
	}
	return changed
}

// VirtualNode 宿 → 虚 Node：复制 labels/annotations/spec（taints、podCIDR、providerID、unschedulable），
// status 由 VirtualNodeStatus 单独产生（需走 status 子资源）。
func VirtualNode(host *corev1.Node) *corev1.Node {
	n := &corev1.Node{ObjectMeta: mirrorMeta(host)}
	n.Spec = corev1.NodeSpec{
		PodCIDR:       host.Spec.PodCIDR,
		PodCIDRs:      append([]string(nil), host.Spec.PodCIDRs...),
		ProviderID:    host.Spec.ProviderID,
		Unschedulable: host.Spec.Unschedulable,
		Taints:        append([]corev1.Taint(nil), host.Spec.Taints...),
	}
	return n
}

// VirtualNodeStatus 宿 → 虚 Node status：真实地址、容量、conditions、nodeInfo；
// kubeletEndpoint.Port 固定 10250（apiserver 经隧道访问假 kubelet）；images 列表体积大且无用，丢弃。
func VirtualNodeStatus(host *corev1.Node) corev1.NodeStatus {
	st := *host.Status.DeepCopy()
	st.Images = nil
	st.DaemonEndpoints = corev1.NodeDaemonEndpoints{
		KubeletEndpoint: corev1.DaemonEndpoint{Port: names.FakeKubeletPort},
	}
	return st
}

// ApplyNodeMutations 覆盖虚拟 Node 的 metadata 与 spec。
func ApplyNodeMutations(desired, actual *corev1.Node) bool {
	changed := applyMirrorMeta(&actual.ObjectMeta, desired.ObjectMeta)
	if !equality.Semantic.DeepEqual(actual.Spec, desired.Spec) {
		actual.Spec = desired.Spec
		changed = true
	}
	return changed
}

// NodeStatusChanged 比较时忽略 conditions 的 lastHeartbeatTime（宿主每 10s 一跳），避免无意义写放大；
// 其他字段有变化时整体回写（含当时的心跳时间）。
func NodeStatusChanged(actual *corev1.Node, desired corev1.NodeStatus) bool {
	if equality.Semantic.DeepEqual(stripHeartbeat(actual.Status), stripHeartbeat(desired)) {
		return false
	}
	actual.Status = desired
	return true
}

func stripHeartbeat(st corev1.NodeStatus) corev1.NodeStatus {
	out := *st.DeepCopy()
	for i := range out.Conditions {
		out.Conditions[i].LastHeartbeatTime = metav1.Time{}
	}
	return out
}

// VirtualStorageClass 宿 → 虚 StorageClass 只读镜像。
func VirtualStorageClass(host *storagev1.StorageClass) *storagev1.StorageClass {
	return &storagev1.StorageClass{
		ObjectMeta:           mirrorMeta(host),
		Provisioner:          host.Provisioner,
		Parameters:           host.Parameters,
		ReclaimPolicy:        host.ReclaimPolicy,
		MountOptions:         host.MountOptions,
		AllowVolumeExpansion: host.AllowVolumeExpansion,
		VolumeBindingMode:    host.VolumeBindingMode,
		AllowedTopologies:    host.AllowedTopologies,
	}
}

// ApplyStorageClassMutations 覆盖可变字段；provisioner/parameters/reclaimPolicy/volumeBindingMode 不可变，
// 若宿主改了这些（删了重建），调用方需删除重建虚拟对象。返回 (changed, needRecreate)。
func ApplyStorageClassMutations(desired, actual *storagev1.StorageClass) (bool, bool) {
	if actual.Provisioner != desired.Provisioner ||
		!equality.Semantic.DeepEqual(actual.Parameters, desired.Parameters) ||
		!equality.Semantic.DeepEqual(actual.ReclaimPolicy, desired.ReclaimPolicy) ||
		!equality.Semantic.DeepEqual(actual.VolumeBindingMode, desired.VolumeBindingMode) {
		return false, true
	}
	changed := applyMirrorMeta(&actual.ObjectMeta, desired.ObjectMeta)
	if !equality.Semantic.DeepEqual(actual.MountOptions, desired.MountOptions) {
		actual.MountOptions = desired.MountOptions
		changed = true
	}
	if !equality.Semantic.DeepEqual(actual.AllowVolumeExpansion, desired.AllowVolumeExpansion) {
		actual.AllowVolumeExpansion = desired.AllowVolumeExpansion
		changed = true
	}
	if !equality.Semantic.DeepEqual(actual.AllowedTopologies, desired.AllowedTopologies) {
		actual.AllowedTopologies = desired.AllowedTopologies
		changed = true
	}
	return changed, false
}

// VirtualIngressClass 宿 → 虚 IngressClass 只读镜像。
func VirtualIngressClass(host *networkingv1.IngressClass) *networkingv1.IngressClass {
	return &networkingv1.IngressClass{
		ObjectMeta: mirrorMeta(host),
		Spec:       *host.Spec.DeepCopy(),
	}
}

// ApplyIngressClassMutations 覆盖 metadata 与 spec。
func ApplyIngressClassMutations(desired, actual *networkingv1.IngressClass) bool {
	changed := applyMirrorMeta(&actual.ObjectMeta, desired.ObjectMeta)
	if !equality.Semantic.DeepEqual(actual.Spec, desired.Spec) {
		actual.Spec = desired.Spec
		changed = true
	}
	return changed
}
