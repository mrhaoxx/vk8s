package audit

import (
	"time"

	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"

	"vk8s.io/vk8s/internal/names"
)

// FromEventList 把 apiserver 审计 webhook 送来的一批事件转成行。
// vcluster 一律取调用方给的值（来自 webhook 客户端证书的 CN），不信任事件体里的任何集群信息。
func FromEventList(vcluster string, list *auditv1.EventList, receivedAt time.Time) []Event {
	out := make([]Event, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, fromKubeEvent(vcluster, &list.Items[i], receivedAt))
	}
	return out
}

func fromKubeEvent(vcluster string, ev *auditv1.Event, receivedAt time.Time) Event {
	e := Event{
		TS:          ev.StageTimestamp.Time,
		ReceivedAt:  receivedAt,
		Source:      SourceKube,
		VCluster:    vcluster,
		ID:          string(ev.AuditID),
		Stage:       string(ev.Stage),
		Level:       string(ev.Level),
		Verb:        ev.Verb,
		User:        ev.User.Username,
		Groups:      append([]string{}, ev.User.Groups...),
		SourceIPs:   append([]string{}, ev.SourceIPs...),
		UserAgent:   ev.UserAgent,
		RequestURI:  ev.RequestURI,
		Annotations: map[string]string{},
	}
	if e.TS.IsZero() {
		e.TS = ev.RequestReceivedTimestamp.Time
	}
	if ev.ImpersonatedUser != nil {
		e.ImpersonatedUser = ev.ImpersonatedUser.Username
	}
	if ref := ev.ObjectRef; ref != nil {
		e.Namespace, e.APIGroup, e.APIVersion = ref.Namespace, ref.APIGroup, ref.APIVersion
		e.Resource, e.Subresource, e.Name = ref.Resource, ref.Subresource, ref.Name
	}
	if st := ev.ResponseStatus; st != nil {
		if st.Code >= 0 && st.Code <= 0xFFFF {
			e.ResponseCode = uint16(st.Code)
		}
		e.ResponseReason = string(st.Reason)
		e.ResponseMessage = st.Message
	}
	if ev.RequestObject != nil && len(ev.RequestObject.Raw) > 0 {
		e.RequestObject, e.RequestTruncated = Truncate(string(ev.RequestObject.Raw))
	}
	for k, v := range ev.Annotations {
		e.Annotations[k] = v
	}
	// TokenReview 放进 user.extra 的令牌 ID；放在 apiserver 注解之后写入，不可能被请求方伪造
	if v := ev.User.Extra[names.TokenIDKey]; len(v) > 0 && v[0] != "" {
		e.Annotations[names.TokenIDKey] = v[0]
	}
	return e
}
