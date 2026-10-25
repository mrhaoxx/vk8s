package translate

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
)

// TranslateConfigMap 虚 → 宿 ConfigMap，全量复制。
func TranslateConfigMap(v *corev1.ConfigMap, hostNamespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: HostObjectMeta(v, hostNamespace),
		Immutable:  v.Immutable,
		Data:       v.Data,
		BinaryData: v.BinaryData,
	}
}

// ApplyConfigMapMutations 覆盖 data/binaryData（immutable 的 ConfigMap 宿主会拒绝修改，用户侧同样不可改）。
func ApplyConfigMapMutations(desired, actual *corev1.ConfigMap) bool {
	changed := MergeMeta(&actual.ObjectMeta, desired.ObjectMeta)
	if !equality.Semantic.DeepEqual(actual.Data, desired.Data) {
		actual.Data = desired.Data
		changed = true
	}
	if !equality.Semantic.DeepEqual(actual.BinaryData, desired.BinaryData) {
		actual.BinaryData = desired.BinaryData
		changed = true
	}
	return changed
}

// IsServiceAccountTokenSecret 旧式 SA token Secret，不同步。
func IsServiceAccountTokenSecret(s *corev1.Secret) bool {
	return s.Type == corev1.SecretTypeServiceAccountToken
}

// TranslateSecret 虚 → 宿 Secret，全量复制（stringData 在虚拟侧落库时已合并进 data）。
func TranslateSecret(v *corev1.Secret, hostNamespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: HostObjectMeta(v, hostNamespace),
		Immutable:  v.Immutable,
		Type:       v.Type,
		Data:       v.Data,
	}
}

// ApplySecretMutations 覆盖 data；type 不可变，若两侧不同由调用方删除重建。
func ApplySecretMutations(desired, actual *corev1.Secret) bool {
	changed := MergeMeta(&actual.ObjectMeta, desired.ObjectMeta)
	if !equality.Semantic.DeepEqual(actual.Data, desired.Data) {
		actual.Data = desired.Data
		changed = true
	}
	return changed
}

// TranslatePVC 虚 → 宿 PVC：storageClassName 透传、volumeName 丢弃（由宿主绑定）、dataSource 若指向 PVC 则改名。
func TranslatePVC(v *corev1.PersistentVolumeClaim, hostNamespace string) *corev1.PersistentVolumeClaim {
	h := &corev1.PersistentVolumeClaim{
		ObjectMeta: HostObjectMeta(v, hostNamespace),
		Spec:       *v.Spec.DeepCopy(),
	}
	h.Spec.VolumeName = ""
	rewriteDataSource := func(ref *corev1.TypedLocalObjectReference) {
		if ref != nil && ref.Kind == "PersistentVolumeClaim" && (ref.APIGroup == nil || *ref.APIGroup == "") {
			ref.Name = HostName(ref.Name, v.Namespace)
		}
	}
	rewriteDataSource(h.Spec.DataSource)
	if ref := h.Spec.DataSourceRef; ref != nil && ref.Namespace == nil &&
		ref.Kind == "PersistentVolumeClaim" && (ref.APIGroup == nil || *ref.APIGroup == "") {
		ref.Name = HostName(ref.Name, v.Namespace)
	}
	// v2: VolumeSnapshot 作为 dataSource 时的名字映射（快照对象本身不同步）。
	return h
}

// ApplyPVCMutations PVC 只允许扩容：requests.storage 变大时跟随。
func ApplyPVCMutations(desired, actual *corev1.PersistentVolumeClaim) bool {
	changed := MergeMeta(&actual.ObjectMeta, desired.ObjectMeta)
	want, ok := desired.Spec.Resources.Requests[corev1.ResourceStorage]
	if ok {
		cur, has := actual.Spec.Resources.Requests[corev1.ResourceStorage]
		if !has || want.Cmp(cur) > 0 {
			if actual.Spec.Resources.Requests == nil {
				actual.Spec.Resources.Requests = corev1.ResourceList{}
			}
			actual.Spec.Resources.Requests[corev1.ResourceStorage] = want.DeepCopy()
			changed = true
		}
	}
	return changed
}

// PVCStatusChanged 回写 PVC status（phase/capacity/accessModes/conditions）。
func PVCStatusChanged(v, h *corev1.PersistentVolumeClaim) bool {
	if equality.Semantic.DeepEqual(v.Status, h.Status) {
		return false
	}
	v.Status = *h.Status.DeepCopy()
	return true
}
