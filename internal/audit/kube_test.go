package audit

import (
	"strings"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"

	"vk8s.io/vk8s/internal/names"
)

func TestFromEventListUsesCNClusterAndTruncates(t *testing.T) {
	big := []byte(`{"kind":"Pod","blob":"` + strings.Repeat("x", MaxRequestObjectBytes) + `"}`)
	list := &auditv1.EventList{Items: []auditv1.Event{{
		AuditID: "7c1e0f3a", Stage: auditv1.StageResponseComplete, Level: auditv1.LevelRequest, Verb: "create",
		User: authnv1.UserInfo{Username: "alice", Groups: []string{"vk8s:admins"}}, SourceIPs: []string{"10.0.0.1"}, UserAgent: "kubectl",
		ObjectRef:  &auditv1.ObjectReference{Namespace: "demo", Resource: "pods", Subresource: "exec", Name: "web", APIGroup: "", APIVersion: "v1"},
		RequestURI: "/api/v1/namespaces/demo/pods/web/exec", ResponseStatus: &metav1.Status{Code: 101},
		RequestObject:  &runtime.Unknown{Raw: big},
		StageTimestamp: metav1.NewMicroTime(time.Date(2026, 10, 24, 6, 1, 58, 312e6, time.UTC)),
		Annotations:    map[string]string{"authorization.k8s.io/decision": "allow", "vcluster": "team-x"},
	}}}
	got := FromEventList("alice-dev", list, time.Unix(0, 0))
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	e := got[0]
	if e.VCluster != "alice-dev" || e.Source != SourceKube || e.ID != "7c1e0f3a" || e.Verb != "create" || e.Subresource != "exec" || e.ResponseCode != 101 || e.User != "alice" {
		t.Fatalf("event = %+v", e)
	}
	if e.RequestTruncated != 1 || len(e.RequestObject) > MaxRequestObjectBytes {
		t.Fatalf("truncation: %d/%d", len(e.RequestObject), e.RequestTruncated)
	}
	if e.Annotations["authorization.k8s.io/decision"] != "allow" || !e.TS.Equal(time.Date(2026, 10, 24, 6, 1, 58, 312e6, time.UTC)) || e.Stage != "ResponseComplete" || e.Level != "Request" {
		t.Fatalf("event = %+v", e)
	}
}

func TestFromEventListHandlesNilOptionals(t *testing.T) {
	got := FromEventList("vc", &auditv1.EventList{Items: []auditv1.Event{{AuditID: "x", Verb: "get"}}}, time.Now())
	if got[0].Annotations == nil || got[0].Groups == nil || got[0].ResponseCode != 0 || got[0].ImpersonatedUser != "" {
		t.Fatalf("event = %+v", got[0])
	}
}

// TokenReview 放进 user.extra 的令牌 ID 落到事件 annotations，审计才能对应到具体令牌。
func TestFromEventListCopiesTokenIDFromUserExtra(t *testing.T) {
	list := &auditv1.EventList{Items: []auditv1.Event{
		{AuditID: "a", Verb: "get", User: authnv1.UserInfo{Username: "alice", Extra: map[string]authnv1.ExtraValue{names.TokenIDKey: {"tok-abc"}}}},
		{AuditID: "b", Verb: "get", User: authnv1.UserInfo{Username: "system:apiserver"}},
	}}
	got := FromEventList("vc", list, time.Now())
	if got[0].Annotations[names.TokenIDKey] != "tok-abc" {
		t.Fatalf("annotations = %v", got[0].Annotations)
	}
	if _, ok := got[1].Annotations[names.TokenIDKey]; ok {
		t.Fatalf("no token id expected for system users: %v", got[1].Annotations)
	}
}
