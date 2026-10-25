package translate

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"vk8s.io/vk8s/internal/names"
)

const (
	// MaxHostNameLength 宿主对象名上限；超出则走截断 + 哈希。
	MaxHostNameLength = 63
	// hostNameSeparator 虚名与虚 ns 之间的分隔。
	hostNameSeparator = "-x-"
	// truncatedPartLength 截断模式下虚名、虚 ns 各保留的长度。
	truncatedPartLength = 20
	// hashLength 截断模式下附加的 sha256 十六进制前缀长度。
	hashLength = 10

	// AnnotationSyncedLabels 宿主对象上记录「由 syncer 写入的用户 label key 列表」，
	// 用于下次同步时删除虚拟侧已移除的 label，而不误删宿主组件自己加的。
	AnnotationSyncedLabels = names.LabelPrefix + "synced-labels"
	// AnnotationSyncedAnnotations 同上，针对 annotation。
	AnnotationSyncedAnnotations = names.LabelPrefix + "synced-annotations"

	// FieldManager server-side 操作时的 field manager 名。
	FieldManager = "vk8s-syncer"
)

// HostName 返回虚拟对象 (vname, vns) 在宿主 ns 内的名字：`<vname>-x-<vns>`；
// 超过 63 字符时各截到 20 字符再加 `-<sha256 前 10 位>`，哈希取自完整的 `<vname>/<vns>`。
func HostName(vname, vns string) string {
	full := vname + hostNameSeparator + vns
	if len(full) <= MaxHostNameLength {
		return full
	}
	sum := sha256.Sum256([]byte(vname + "/" + vns))
	h := hex.EncodeToString(sum[:])[:hashLength]
	return trimEdges(truncate(vname, truncatedPartLength)) + hostNameSeparator +
		trimEdges(truncate(vns, truncatedPartLength)) + "-" + h
}

// HostNameFor 以对象的 name/namespace 调用 HostName。
func HostNameFor(obj metav1.Object) string {
	return HostName(obj.GetName(), obj.GetNamespace())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// trimEdges 去掉首尾非字母数字字符，保证截断后的片段仍是合法 DNS label 片段。
func trimEdges(s string) string {
	return strings.Trim(s, "-.")
}

// FilterUserKeys 复制 map 并丢弃 `vk8s.io/` 前缀的 key（保留给平台）。
func FilterUserKeys(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if strings.HasPrefix(k, names.LabelPrefix) {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sortedKeys 返回排序后的 key 列表，供写入 synced-* 注解。
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// HostLabels 宿主对象的 labels：用户 label（过滤后）+ managed/vname/vnamespace。
func HostLabels(v metav1.Object) map[string]string {
	out := FilterUserKeys(v.GetLabels())
	if out == nil {
		out = map[string]string{}
	}
	out[names.LabelManaged] = "true"
	out[names.LabelVName] = v.GetName()
	out[names.LabelVNamespace] = v.GetNamespace()
	return out
}

// HostAnnotations 宿主对象的 annotations：用户 annotation（过滤后）+ vuid + synced-* 记录。
func HostAnnotations(v metav1.Object) map[string]string {
	user := FilterUserKeys(v.GetAnnotations())
	out := make(map[string]string, len(user)+3)
	for k, val := range user {
		out[k] = val
	}
	if uid := v.GetUID(); uid != "" {
		out[names.AnnotationVUID] = string(uid)
	}
	out[AnnotationSyncedLabels] = strings.Join(sortedKeys(FilterUserKeys(v.GetLabels())), ",")
	out[AnnotationSyncedAnnotations] = strings.Join(sortedKeys(user), ",")
	return out
}

// HostObjectMeta 构造宿主对象的 ObjectMeta：名字映射、labels/annotations 处理，
// 不带 ownerReferences / finalizers / uid 等任何虚拟侧元信息。
func HostObjectMeta(v metav1.Object, hostNamespace string) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name:        HostNameFor(v),
		Namespace:   hostNamespace,
		Labels:      HostLabels(v),
		Annotations: HostAnnotations(v),
	}
}

// MergeMeta 把期望的 labels/annotations 合并进宿主对象现有的 ObjectMeta：
// 写入全部期望项；删除上次由 syncer 写入、如今已不在期望里的 key；
// 其余（宿主组件自己加的，如 CNI、PV 绑定注解）原样保留。返回是否有改动。
func MergeMeta(actual *metav1.ObjectMeta, desired metav1.ObjectMeta) bool {
	changed := false
	if actual.Labels == nil {
		actual.Labels = map[string]string{}
	}
	if actual.Annotations == nil {
		actual.Annotations = map[string]string{}
	}
	for _, k := range splitKeys(actual.Annotations[AnnotationSyncedLabels]) {
		if _, want := desired.Labels[k]; !want {
			if _, ok := actual.Labels[k]; ok {
				delete(actual.Labels, k)
				changed = true
			}
		}
	}
	for _, k := range splitKeys(actual.Annotations[AnnotationSyncedAnnotations]) {
		if _, want := desired.Annotations[k]; !want {
			if _, ok := actual.Annotations[k]; ok {
				delete(actual.Annotations, k)
				changed = true
			}
		}
	}
	for k, v := range desired.Labels {
		if cur, ok := actual.Labels[k]; !ok || cur != v {
			actual.Labels[k] = v
			changed = true
		}
	}
	for k, v := range desired.Annotations {
		if cur, ok := actual.Annotations[k]; !ok || cur != v {
			actual.Annotations[k] = v
			changed = true
		}
	}
	return changed
}

func splitKeys(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// ManagedSelector 宿主侧「由 syncer 创建」的 label selector。
func ManagedSelector() labels.Selector {
	return labels.SelectorFromSet(labels.Set{names.LabelManaged: "true"})
}

// IsManaged 对象是否由 syncer 创建：带 `vk8s.io/managed=true`，且不是宿主 kcm 的 endpoints /
// endpointslice 控制器建的对象。这两个控制器会把 Service 的全部 label（含 vk8s.io/managed、vname、
// vnamespace）复制到它们为有 selector 的宿主 Service 生成的 Endpoints / EndpointSlice 上，
// 只能靠它们自己打的 managed-by 标签区分；否则 syncer 会把它们当孤儿反复删除，与 kcm 打架。
func IsManaged(obj metav1.Object) bool {
	l := obj.GetLabels()
	if l[names.LabelManaged] != "true" {
		return false
	}
	if _, kcm := l[LabelEndpointsManagedBy]; kcm {
		return false
	}
	if mb, ok := l[LabelEndpointSliceManagedBy]; ok && mb != EndpointSliceManager {
		return false
	}
	return true
}
