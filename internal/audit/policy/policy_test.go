package policy

import (
	"testing"

	auditinternal "k8s.io/apiserver/pkg/apis/audit"
	apipolicy "k8s.io/apiserver/pkg/audit/policy"
	k8suser "k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
)

func attrs(user, verb, group, resource, sub, ns string, groups ...string) authorizer.Attributes {
	return authorizer.AttributesRecord{User: &k8suser.DefaultInfo{Name: user, Groups: groups}, Verb: verb, APIGroup: group, Resource: resource, Subresource: sub, Namespace: ns, ResourceRequest: true}
}

func nonResource(user, verb, path string) authorizer.Attributes {
	return authorizer.AttributesRecord{User: &k8suser.DefaultInfo{Name: user}, Verb: verb, Path: path, ResourceRequest: false}
}

func TestPolicyLevels(t *testing.T) {
	p, err := apipolicy.LoadPolicyFromBytes(YAML)
	if err != nil {
		t.Fatal(err)
	}
	ev := apipolicy.NewPolicyRuleEvaluator(p)
	cases := []struct {
		name string
		a    authorizer.Attributes
		want auditinternal.Level
	}{
		{"kcm list pods", attrs("system:kube-controller-manager", "list", "", "pods", "", "demo"), auditinternal.LevelNone},
		{"syncer watch nodes", attrs("vk8s:syncer", "watch", "", "nodes", "", ""), auditinternal.LevelNone},
		{"kube-system sa get", attrs("system:serviceaccount:kube-system:coredns", "get", "", "endpoints", "", "kube-system", "system:serviceaccounts:kube-system"), auditinternal.LevelNone},
		{"kcm create event is recorded", attrs("system:kube-controller-manager", "create", "", "events", "", "demo"), auditinternal.LevelRequest},
		{"apiserver lease renew in kube-system is not recorded", attrs("system:apiserver", "update", "coordination.k8s.io", "leases", "", "kube-system"), auditinternal.LevelNone},
		{"kcm lease renew in kube-system is not recorded", attrs("system:kube-controller-manager", "update", "coordination.k8s.io", "leases", "", "kube-system"), auditinternal.LevelNone},
		{"kube-system sa lease renew is not recorded", attrs("system:serviceaccount:kube-system:coredns", "update", "coordination.k8s.io", "leases", "", "kube-system", "system:serviceaccounts:kube-system"), auditinternal.LevelNone},
		{"user lease update in kube-system is recorded", attrs("alice", "update", "coordination.k8s.io", "leases", "", "kube-system"), auditinternal.LevelRequest},
		{"apiserver lease outside kube-system is recorded", attrs("system:apiserver", "update", "coordination.k8s.io", "leases", "", "demo"), auditinternal.LevelRequest},
		{"user get secret", attrs("alice", "get", "", "secrets", "", "demo"), auditinternal.LevelMetadata},
		{"user create secret has no body", attrs("alice", "create", "", "secrets", "", "demo"), auditinternal.LevelMetadata},
		{"user create deployment", attrs("alice", "create", "apps", "deployments", "", "demo"), auditinternal.LevelRequest},
		{"user exec", attrs("alice", "create", "", "pods", "exec", "demo"), auditinternal.LevelRequest},
		{"user list pods", attrs("alice", "list", "", "pods", "", "demo"), auditinternal.LevelMetadata},
		{"discovery", nonResource("alice", "get", "/apis/apps/v1"), auditinternal.LevelNone},
		{"healthz", nonResource("alice", "get", "/healthz"), auditinternal.LevelNone},
	}
	for _, c := range cases {
		got := ev.EvaluatePolicyRule(c.a)
		if got.Level != c.want {
			t.Errorf("%s: got %s want %s", c.name, got.Level, c.want)
		}
	}
	got := ev.EvaluatePolicyRule(attrs("alice", "get", "", "pods", "", "demo"))
	if len(got.OmitStages) != 1 || got.OmitStages[0] != auditinternal.StageRequestReceived {
		t.Fatalf("RequestReceived must be omitted, got %v", got.OmitStages)
	}
}
