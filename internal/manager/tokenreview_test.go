package manager

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/manager/tokens"
	"vk8s.io/vk8s/internal/names"
)

const (
	plainAlice = "vk8s_alice-token-plaintext"
	plainBob   = "vk8s_bob-token-plaintext"
	plainOld   = "vk8s_expired-token-plaintext"
	plainEve   = "vk8s_eve-token-plaintext"
	plainRoot  = "vk8s_root-token-plaintext"
	plainOther = "vk8s_other-cluster-token"
)

func accessToken(name, user, vc, plaintext string, expires time.Time) *v1alpha1.AccessToken {
	return &v1alpha1.AccessToken{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.AccessTokenSpec{
			User: user, VCluster: vc, TokenHash: names.TokenHash(plaintext),
			ExpiresAt: metav1.NewTime(expires),
		},
	}
}

func newReviewHandler(t *testing.T, objs ...client.Object) *TokenReviewHandler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithIndex(&v1alpha1.AccessToken{}, tokens.IndexTokenHash, func(obj client.Object) []string {
			return []string{obj.(*v1alpha1.AccessToken).Spec.TokenHash}
		}).
		WithObjects(objs...).Build()
	return &TokenReviewHandler{Client: c, Tokens: &tokens.Service{Client: c}}
}

func TestTokenReviewDecisions(t *testing.T) {
	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)
	h := newReviewHandler(t,
		&v1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice", UID: "u-alice"}, Spec: v1alpha1.UserSpec{Role: v1alpha1.UserRoleUser}},
		&v1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "bob"}, Spec: v1alpha1.UserSpec{Role: v1alpha1.UserRoleUser}},
		&v1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "eve"}, Spec: v1alpha1.UserSpec{Role: v1alpha1.UserRoleUser, Disabled: true}},
		&v1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "root"}, Spec: v1alpha1.UserSpec{Role: v1alpha1.UserRoleAdmin}},
		&v1alpha1.VirtualCluster{ObjectMeta: metav1.ObjectMeta{Name: "dev"}, Spec: v1alpha1.VirtualClusterSpec{Owner: "alice"}},
		&v1alpha1.VirtualCluster{ObjectMeta: metav1.ObjectMeta{Name: "other"}, Spec: v1alpha1.VirtualClusterSpec{Owner: "bob"}},
		accessToken("tok-alice", "alice", "dev", plainAlice, future),
		accessToken("tok-bob", "bob", "dev", plainBob, future),    // bob 不是 dev 的成员
		accessToken("tok-old", "alice", "dev", plainOld, past),    // 过期
		accessToken("tok-eve", "eve", "dev", plainEve, future),    // 用户被禁用（先要是成员才会到那一步，这里用 owner 覆盖）
		accessToken("tok-root", "root", "dev", plainRoot, future), // 平台 admin 不是成员也可访问
		accessToken("tok-other", "bob", "other", plainOther, future),
	)
	ctx := context.Background()
	cases := []struct {
		name, vc, token string
		ok              bool
		reason          string
	}{
		{"owner", "dev", plainAlice, true, ""},
		{"owner with spaces", "dev", "  " + plainAlice + "\n", true, ""},
		{"platform admin", "dev", plainRoot, true, ""},
		{"not a member", "dev", plainBob, false, "not a member"},
		{"expired", "dev", plainOld, false, "expired"},
		{"disabled user", "dev", plainEve, false, "disabled"},
		{"token for another cluster", "dev", plainOther, false, "not valid for this cluster"},
		{"unknown token", "dev", "vk8s_nope", false, "unknown token"},
		{"wrong prefix", "dev", "Bearer x", false, "unknown token"},
		{"empty", "dev", "", false, "unknown token"},
		{"cluster missing", "gone", plainAlice, false, "not valid for this cluster"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, err := h.review(ctx, c.vc, c.token, []string{"aud"})
			if err != nil {
				t.Fatalf("review error: %v", err)
			}
			if st.Authenticated != c.ok {
				t.Fatalf("authenticated=%v want %v (error=%q)", st.Authenticated, c.ok, st.Error)
			}
			if c.ok {
				if st.User.Groups[0] != names.GroupAdmins || st.Audiences[0] != "aud" {
					t.Fatalf("user info: %+v", st)
				}
				return
			}
			if c.reason != "" && !contains(st.Error, c.reason) {
				t.Fatalf("error %q does not contain %q", st.Error, c.reason)
			}
		})
	}
	st, _ := h.review(ctx, "dev", plainAlice, nil)
	if st.User.Username != "alice" || st.User.UID != "u-alice" {
		t.Fatalf("user identity: %+v", st.User)
	}
}

func TestTokenReviewMembershipIsLive(t *testing.T) {
	future := time.Now().Add(time.Hour)
	vc := &v1alpha1.VirtualCluster{ObjectMeta: metav1.ObjectMeta{Name: "dev"}, Spec: v1alpha1.VirtualClusterSpec{Owner: "alice", Members: []string{"bob"}}}
	h := newReviewHandler(t,
		&v1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice"}},
		&v1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "bob"}},
		vc,
		accessToken("tok-bob", "bob", "dev", plainBob, future),
	)
	ctx := context.Background()
	if st, _ := h.review(ctx, "dev", plainBob, nil); !st.Authenticated {
		t.Fatalf("member must be accepted: %+v", st)
	}
	// 移除成员后同一个 token 立即失效（apiserver 侧还有 2 分钟 cache，但 manager 不再放行）
	vc.Spec.Members = nil
	if err := h.Client.(client.Client).Update(ctx, vc); err != nil {
		t.Fatal(err)
	}
	if st, _ := h.review(ctx, "dev", plainBob, nil); st.Authenticated {
		t.Fatalf("removed member must be rejected")
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// 审计要能对应到具体令牌：UserInfo.Extra 带 vk8s.io/token-id，apiserver 会原样写进审计事件的 user.extra。
func TestTokenReviewCarriesTokenIDInExtra(t *testing.T) {
	future := time.Now().Add(time.Hour)
	h := newReviewHandler(t,
		&v1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: "alice", UID: "u-alice"}, Spec: v1alpha1.UserSpec{Role: v1alpha1.UserRoleUser}},
		&v1alpha1.VirtualCluster{ObjectMeta: metav1.ObjectMeta{Name: "dev"}, Spec: v1alpha1.VirtualClusterSpec{Owner: "alice"}},
		accessToken("tok-alice", "alice", "dev", plainAlice, future),
	)
	st, err := h.review(context.Background(), "dev", plainAlice, nil)
	if err != nil || !st.Authenticated {
		t.Fatalf("review: %+v err=%v", st, err)
	}
	if v := st.User.Extra[names.TokenIDKey]; len(v) != 1 || v[0] != "tok-alice" {
		t.Fatalf("extra[%s] = %v, want [tok-alice]", names.TokenIDKey, v)
	}
}
