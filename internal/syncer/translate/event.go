package translate

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"vk8s.io/vk8s/internal/names"
)

// AnnotationHostEvent 虚拟 Event 上记录其来源宿主 Event 名。
const AnnotationHostEvent = names.LabelPrefix + "host-event"

// VirtualEventName 把宿主 Event 名映射到虚拟侧：宿主事件名通常是 `<宿主对象名>.<唯一后缀>`，
// 保留后缀换成虚名；格式不符时用宿主事件名的哈希做后缀。
func VirtualEventName(hostEventName, hostObjectName, vname string) string {
	if strings.HasPrefix(hostEventName, hostObjectName+".") {
		return vname + hostEventName[len(hostObjectName):]
	}
	sum := sha256.Sum256([]byte(hostEventName))
	return vname + "." + hex.EncodeToString(sum[:])[:16]
}

// VirtualEvent 宿 → 虚 Event：involvedObject 翻回虚拟对象，其余字段原样（它们已通过宿主的校验）。
// related 指向宿主上的其他对象，丢弃。
func VirtualEvent(hev *corev1.Event, vkey types.NamespacedName, vuid types.UID) *corev1.Event {
	return &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:        VirtualEventName(hev.Name, hev.InvolvedObject.Name, vkey.Name),
			Namespace:   vkey.Namespace,
			Labels:      map[string]string{names.LabelManaged: "true"},
			Annotations: map[string]string{AnnotationHostEvent: hev.Name},
		},
		InvolvedObject: corev1.ObjectReference{
			Kind:       hev.InvolvedObject.Kind,
			APIVersion: hev.InvolvedObject.APIVersion,
			Namespace:  vkey.Namespace,
			Name:       vkey.Name,
			UID:        vuid,
			FieldPath:  hev.InvolvedObject.FieldPath,
		},
		Reason:              hev.Reason,
		Message:             hev.Message,
		Source:              hev.Source,
		FirstTimestamp:      hev.FirstTimestamp,
		LastTimestamp:       hev.LastTimestamp,
		Count:               hev.Count,
		Type:                hev.Type,
		EventTime:           hev.EventTime,
		Series:              hev.Series.DeepCopy(),
		Action:              hev.Action,
		ReportingController: hev.ReportingController,
		ReportingInstance:   hev.ReportingInstance,
	}
}

// ApplyEventMutations 把宿主事件的累计字段更新到已存在的虚拟事件上。
func ApplyEventMutations(desired, actual *corev1.Event) bool {
	changed := false
	if actual.Count != desired.Count {
		actual.Count = desired.Count
		changed = true
	}
	if !actual.LastTimestamp.Equal(&desired.LastTimestamp) {
		actual.LastTimestamp = desired.LastTimestamp
		changed = true
	}
	if actual.Message != desired.Message {
		actual.Message = desired.Message
		changed = true
	}
	if (actual.Series == nil) != (desired.Series == nil) ||
		(actual.Series != nil && (actual.Series.Count != desired.Series.Count || !actual.Series.LastObservedTime.Equal(&desired.Series.LastObservedTime))) {
		actual.Series = desired.Series.DeepCopy()
		changed = true
	}
	return changed
}
