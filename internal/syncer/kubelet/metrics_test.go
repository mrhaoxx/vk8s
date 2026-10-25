package kubelet

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr"
)

func TestTargetHostFromTLSConn(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	tc := tls.Server(&targetConn{Conn: a, target: "10.0.0.7:10250"}, &tls.Config{})
	ctx := connContext(context.Background(), tc)
	if got := TargetHost(ctx); got != "10.0.0.7" {
		t.Fatalf("TargetHost = %q", got)
	}
	if got := TargetHost(context.Background()); got != "" {
		t.Fatalf("no target → empty, got %q", got)
	}
}

func metricsKubelet(manager func(ctx context.Context, node, path string) (io.ReadCloser, int, error)) *FakeKubelet {
	return &FakeKubelet{
		Log: logr.Discard(), HostNamespace: "vc-alice",
		NodeForIP:      func(_ context.Context, ip string) (string, bool) { return "n1", ip == "10.0.0.7" },
		ManagerMetrics: manager,
		HostPodLookup: func(_ context.Context, hostPod string) (string, string, bool) {
			if hostPod == "web-x-demo" {
				return "demo", "web", true
			}
			return "", "", false
		},
	}
}

func get(t *testing.T, k *FakeKubelet, path, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if target != "" {
		req = req.WithContext(context.WithValue(req.Context(), targetKey{}, target))
	}
	rec := httptest.NewRecorder()
	k.Handler().ServeHTTP(rec, req)
	return rec
}

func TestFakeKubeletServesRenamedCadvisor(t *testing.T) {
	var asked string
	k := metricsKubelet(func(_ context.Context, node, path string) (io.ReadCloser, int, error) {
		asked = node + "/" + path
		return io.NopCloser(strings.NewReader("container_memory_working_set_bytes{namespace=\"vc-alice\",pod=\"web-x-demo\"} 1\ncontainer_memory_working_set_bytes{namespace=\"vc-alice\",pod=\"gone-x-demo\"} 2\nmachine_cpu_cores 8\n")), 200, nil
	})
	rec := get(t, k, "/metrics/cadvisor", "10.0.0.7")
	body := rec.Body.String()
	if rec.Code != 200 || asked != "n1/cadvisor" || !strings.Contains(body, `namespace="demo"`) || !strings.Contains(body, `pod="web"`) ||
		strings.Contains(body, "gone") || !strings.Contains(body, "machine_cpu_cores 8") {
		t.Fatalf("code=%d asked=%s body=\n%s", rec.Code, asked, body)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("content-type %q", rec.Header().Get("Content-Type"))
	}
}

func TestFakeKubeletMetricsErrors(t *testing.T) {
	k := metricsKubelet(func(context.Context, string, string) (io.ReadCloser, int, error) {
		return io.NopCloser(strings.NewReader("forbidden")), 403, nil
	})
	if rec := get(t, k, "/metrics/resource", "10.0.0.7"); rec.Code != 403 {
		t.Fatalf("manager status must pass through: %d", rec.Code)
	}
	if rec := get(t, k, "/metrics/cadvisor", "10.9.9.9"); rec.Code != 404 {
		t.Fatalf("unknown node ip: %d", rec.Code)
	}
	if rec := get(t, k, "/metrics", "10.0.0.7"); rec.Code != 404 {
		t.Fatalf("/metrics: %d", rec.Code)
	}
	k.ManagerMetrics = nil
	if rec := get(t, k, "/metrics/cadvisor", "10.0.0.7"); rec.Code != http.StatusNotFound {
		t.Fatalf("not configured → 404, got %d", rec.Code)
	}
}

func TestFakeKubeletLooksUpEachPodOnce(t *testing.T) {
	var lookups int
	k := metricsKubelet(func(context.Context, string, string) (io.ReadCloser, int, error) {
		var b strings.Builder
		for i := 0; i < 50; i++ {
			b.WriteString("m{namespace=\"vc-alice\",pod=\"web-x-demo\",container=\"c" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "\"} 1\n")
		}
		return io.NopCloser(strings.NewReader(b.String())), 200, nil
	})
	inner := k.HostPodLookup
	k.HostPodLookup = func(ctx context.Context, p string) (string, string, bool) { lookups++; return inner(ctx, p) }
	if rec := get(t, k, "/metrics/cadvisor", "10.0.0.7"); rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	if lookups != 1 {
		t.Fatalf("one pod must be looked up once per scrape, got %d", lookups)
	}
}
