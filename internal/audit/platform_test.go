package audit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRedactMasksSensitiveKeysRecursively(t *testing.T) {
	out := Redact([]byte(`{"username":"alice","password":"p@ss","nested":{"token":"abc","keep":1},"list":[{"kubeconfig":"x"}],"tokenHash":"sha256:zz"}`))
	for _, bad := range []string{"p@ss", "abc", `"x"`, "sha256:zz"} {
		if strings.Contains(out, bad) {
			t.Fatalf("leaked %s in %s", bad, out)
		}
	}
	if !strings.Contains(out, `"username":"alice"`) || !strings.Contains(out, `"keep":1`) || !strings.Contains(out, `"password":"***"`) {
		t.Fatal(out)
	}
	if Redact([]byte("not json")) != "" || Redact(nil) != "" {
		t.Fatal("non-JSON must be dropped")
	}
}

func TestVerbFor(t *testing.T) {
	cases := map[[2]string]string{
		{"POST", "auth.login"}: "login", {"POST", "auth.logout"}: "logout", {"GET", "auth.oidc.login"}: "login",
		{"POST", "vcluster.create"}: "create", {"PATCH", "user.patch"}: "update", {"PUT", "member.add"}: "update", {"DELETE", "token.delete"}: "delete",
	}
	for in, want := range cases {
		if got := VerbFor(in[0], in[1]); got != want {
			t.Errorf("%v: got %s want %s", in, got, want)
		}
	}
}

func startRecorder(t *testing.T) (*Recorder, *collectSink) {
	t.Helper()
	sink := &collectSink{}
	p := NewPipeline(sink, Options{BatchSize: 1, BatchInterval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = p.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return NewRecorder(p), sink
}

func TestRecorderRecordsMutationAndSkipsGET(t *testing.T) {
	rec, sink := startRecorder(t)
	mux := http.NewServeMux()
	mux.Handle("PATCH /api/v1/vclusters/{name}", rec.Wrap("vcluster.patch", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Annotate(r.Context()).Actor = "admin"
		w.WriteHeader(200)
	})))
	mux.Handle("GET /api/v1/vclusters/{name}", rec.Wrap("vcluster.get", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })))
	req := httptest.NewRequest("PATCH", "/api/v1/vclusters/alice-dev?x=1", strings.NewReader(`{"quota":{"cpu":"32"},"password":"nope"}`))
	req.RemoteAddr = "10.1.2.3:4444"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("User-Agent", "vitest")
	mux.ServeHTTP(httptest.NewRecorder(), req)
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/vclusters/alice-dev", nil))
	waitFor(t, func() bool { return sink.count() == 1 })
	e := sink.first()
	if e.Source != SourcePlatform || e.User != "admin" || e.VCluster != "alice-dev" || e.PlatformAction != "vcluster.patch" || e.Verb != "update" || e.ResponseCode != 200 || e.PlatformTarget != "alice-dev" {
		t.Fatalf("event = %+v", e)
	}
	if strings.Contains(e.RequestObject, "nope") || !strings.Contains(e.RequestObject, `"cpu":"32"`) {
		t.Fatalf("request_object = %s", e.RequestObject)
	}
	if len(e.SourceIPs) != 2 || e.SourceIPs[0] != "10.1.2.3" || e.SourceIPs[1] != "203.0.113.9" || e.UserAgent != "vitest" || e.RequestURI != "/api/v1/vclusters/alice-dev?x=1" || e.ID == "" || e.Stage != "ResponseComplete" {
		t.Fatalf("event = %+v", e)
	}
	time.Sleep(20 * time.Millisecond)
	if sink.count() != 1 {
		t.Fatal("GET must not be recorded")
	}
}

func TestRecorderLoginFailureNeverStoresBody(t *testing.T) {
	rec, sink := startRecorder(t)
	h := rec.Wrap("auth.login", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := Annotate(r.Context())
		a.Actor = "carol"
		a.SkipBody = true
		a.Reason = "failure"
		w.WriteHeader(401)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{"username":"carol","password":"hunter2"}`)))
	waitFor(t, func() bool { return sink.count() == 1 })
	e := sink.first()
	if e.Verb != "login" || e.User != "carol" || e.ResponseCode != 401 || e.ResponseReason != "failure" || e.RequestObject != "" || e.PlatformTarget != "carol" {
		t.Fatalf("event = %+v", e)
	}
}

func TestRecorderBodyStillReachesHandler(t *testing.T) {
	rec, sink := startRecorder(t)
	var seen string
	h := rec.Wrap("booking.create", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 64)
		n, _ := r.Body.Read(b)
		seen = string(b[:n])
		a := Annotate(r.Context())
		a.VCluster, a.Target = "team-x", "nb-1"
		w.WriteHeader(201)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/bookings", strings.NewReader(`{"node":"gpu-07"}`)))
	if seen != `{"node":"gpu-07"}` {
		t.Fatalf("handler saw %q", seen)
	}
	waitFor(t, func() bool { return sink.count() == 1 })
	if e := sink.first(); e.VCluster != "team-x" || e.PlatformTarget != "nb-1" || e.ResponseCode != 201 || !strings.Contains(e.RequestObject, "gpu-07") {
		t.Fatalf("event = %+v", e)
	}
}

func TestNilRecorderIsNoop(t *testing.T) {
	var r *Recorder
	called := false
	r.Wrap("x", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil))
	if !called {
		t.Fatal("next must run")
	}
	if NewRecorder(nil) != nil {
		t.Fatal("NewRecorder(nil) must be nil")
	}
}

// 登录 / 设密码的请求体无条件不记：处理器在标注之前就返回（CSRF 403、JSON 400）时也一样。
func TestRecorderNeverStoresBodyForLoginEvenWhenHandlerBailsEarly(t *testing.T) {
	rec, sink := startRecorder(t)
	h := rec.Wrap("auth.login", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) }))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/auth/login", strings.NewReader(`{"username":"carol","pwd":"hunter2"}`)))
	h2 := rec.Wrap("user.password", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(400) }))
	h2.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/users/bob/password", strings.NewReader(`{"newPassword":"hunter2"}`)))
	waitFor(t, func() bool { return sink.count() == 2 })
	for _, e := range []Event{sink.first(), sink.at(1)} {
		if e.RequestObject != "" || strings.Contains(e.RequestObject, "hunter2") {
			t.Fatalf("%s must never store a body, got %q", e.PlatformAction, e.RequestObject)
		}
	}
}

// OIDC 回调的查询串里是授权码与 state，不进 request_uri。
func TestRecorderStripsQueryFromOIDCCallback(t *testing.T) {
	rec, sink := startRecorder(t)
	h := rec.Wrap("auth.oidc.login", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := Annotate(r.Context())
		a.Actor, a.Reason = "alice", "success"
		w.WriteHeader(302)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/auth/oidc/callback?code=SECRETCODE&state=abc", nil))
	waitFor(t, func() bool { return sink.count() == 1 })
	if e := sink.first(); strings.Contains(e.RequestURI, "SECRETCODE") || e.RequestURI != "/auth/oidc/callback" || e.Verb != "login" {
		t.Fatalf("event = %+v", e)
	}
}
