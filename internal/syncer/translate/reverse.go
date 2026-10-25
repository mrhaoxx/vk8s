package translate

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"vk8s.io/vk8s/internal/names"
)

// VirtualKey 从宿主对象 label 反查其对应的虚拟对象 namespace/name。
// 不带 vname/vnamespace 标签的对象（如 SA token Secret、控制平面自身）返回 false；
// 宿主 kcm 复制了 Service label 的 Endpoints / EndpointSlice 也返回 false（见 IsManaged）。
func VirtualKey(obj metav1.Object) (types.NamespacedName, bool) {
	if !IsManaged(obj) {
		return types.NamespacedName{}, false
	}
	l := obj.GetLabels()
	name, ns := l[names.LabelVName], l[names.LabelVNamespace]
	if name == "" || ns == "" {
		return types.NamespacedName{}, false
	}
	return types.NamespacedName{Namespace: ns, Name: name}, true
}

// VirtualUID 宿主对象上记录的虚拟对象 UID，可能为空。
func VirtualUID(obj metav1.Object) types.UID {
	return types.UID(obj.GetAnnotations()[names.AnnotationVUID])
}

// IsManagedFor 宿主对象是否正是虚拟对象 v 的映射（忽略 UID，允许虚拟对象同名重建后被接管）。
func IsManagedFor(host, v metav1.Object) bool {
	key, ok := VirtualKey(host)
	return ok && key.Name == v.GetName() && key.Namespace == v.GetNamespace()
}
