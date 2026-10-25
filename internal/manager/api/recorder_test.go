package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/manager/auth"
)

// user() 必须把会话用户写进审计标注，这样包在外层的记录器才知道操作者是谁。
func TestUserMiddlewareAnnotatesActor(t *testing.T) {
	s, _ := newAPITest(t, apiUser("alice", false))
	s.Auth = &auth.Authenticator{Session: auth.NewSession([]byte("0123456789abcdef0123456789abcdef"), time.Hour, false), Reader: s.Client}
	var actor string
	h := s.user(func(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
		actor = audit.Annotate(r.Context()).Actor
		w.WriteHeader(http.StatusNoContent)
	})
	issued := httptest.NewRecorder()
	if err := s.Auth.Session.Issue(issued, "alice"); err != nil {
		t.Fatal(err)
	}
	ctx, ann := audit.WithAnnotation(context.Background()) // 模拟 rec() 包装
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vclusters", nil).WithContext(ctx)
	req.Header.Set("X-Requested-With", "vk8s")
	for _, c := range issued.Result().Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	if actor != "alice" || ann.Actor != "alice" {
		t.Fatalf("actor = %q / %q", actor, ann.Actor)
	}
}

// 审计未启用（Recorder 为 nil）时 rec() 不改变处理器。
func TestRecIsIdentityWithoutRecorder(t *testing.T) {
	s, _ := newAPITest(t)
	called := false
	s.rec("x", func(w http.ResponseWriter, r *http.Request) { called = true })(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil))
	if !called {
		t.Fatal("handler must run")
	}
}

// createVCluster 的平台事件必须带上集群名，租户在集群「审计」Tab 才能看到是谁建的集群。
func TestCreateVClusterAnnotatesTargetCluster(t *testing.T) {
	s, _ := newAPITest(t, apiUser("admin", true))
	s.Config.Defaults.PodSecurity = "privileged"
	ctx, ann := audit.WithAnnotation(context.Background())
	body := strings.NewReader(`{"name":"audit-new"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vclusters", body).WithContext(ctx)
	rec := httptest.NewRecorder()
	s.createVCluster(rec, req, apiUser("admin", true))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if ann.VCluster != "audit-new" || ann.Target != "audit-new" {
		t.Fatalf("annotation = %+v", ann)
	}
}
