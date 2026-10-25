package manager

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"vk8s.io/vk8s/api/v1alpha1"
)

const hostCadvisor = `# TYPE container_memory_working_set_bytes gauge
container_memory_working_set_bytes{namespace="vc-alice-dev",pod="web-x-demo"} 1
container_memory_working_set_bytes{namespace="vc-bob",pod="db-x-default"} 2
container_memory_working_set_bytes{id="/"} 3
# TYPE machine_cpu_cores gauge
machine_cpu_cores 64
`

func kubeletMetricsMux(t *testing.T, fetch func(ctx context.Context, node, path string) (io.ReadCloser, error)) *http.ServeMux {
	t.Helper()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	vc := &v1alpha1.VirtualCluster{ObjectMeta: metav1.ObjectMeta{Name: "alice-dev"},
		Spec: v1alpha1.VirtualClusterSpec{Owner: "alice", NodePlacement: v1alpha1.NodePlacement{NodeSelector: map[string]string{"pool": "a"}}}}
	nodes := []*corev1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "n1", Labels: map[string]string{"pool": "a"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "n2", Labels: map[string]string{"pool": "b"}}},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(vc, nodes[0], nodes[1]).Build()
	mux := http.NewServeMux()
	(&KubeletMetricsHandler{Client: c, Fetch: fetch}).Register(mux)
	return mux
}

func okFetch(_ context.Context, _, _ string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(hostCadvisor)), nil
}

func TestKubeletMetricsFiltersToOwnCluster(t *testing.T) {
	mux := kubeletMetricsMux(t, okFetch)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, withCN(httptest.NewRequest("GET", "/kubelet/alice-dev/nodes/n1/metrics/cadvisor", nil), "vc-alice-dev"))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "web-x-demo") || !strings.Contains(body, "machine_cpu_cores") ||
		strings.Contains(body, "vc-bob") || strings.Contains(body, " 3") {
		t.Fatalf("code=%d body=\n%s", rec.Code, body)
	}
}

func TestKubeletMetricsRejectsWrongCNAndForeignNode(t *testing.T) {
	mux := kubeletMetricsMux(t, okFetch)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, withCN(httptest.NewRequest("GET", "/kubelet/alice-dev/nodes/n1/metrics/cadvisor", nil), "vc-bob"))
	if rec.Code != 403 {
		t.Fatalf("wrong CN: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, withCN(httptest.NewRequest("GET", "/kubelet/alice-dev/nodes/n2/metrics/cadvisor", nil), "vc-alice-dev"))
	if rec.Code != 404 {
		t.Fatalf("node outside placement: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, withCN(httptest.NewRequest("GET", "/kubelet/alice-dev/nodes/n1/metrics/probes", nil), "vc-alice-dev"))
	if rec.Code != 404 {
		t.Fatalf("unsupported path: %d", rec.Code)
	}
}

func TestKubeletMetricsUpstreamErrorsAre502(t *testing.T) {
	mux := kubeletMetricsMux(t, func(context.Context, string, string) (io.ReadCloser, error) { return nil, errors.New("boom") })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, withCN(httptest.NewRequest("GET", "/kubelet/alice-dev/nodes/n1/metrics/resource", nil), "vc-alice-dev"))
	if rec.Code != 502 {
		t.Fatalf("upstream error: %d", rec.Code)
	}
	mux = kubeletMetricsMux(t, func(context.Context, string, string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("garbage { not metrics")), nil
	})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, withCN(httptest.NewRequest("GET", "/kubelet/alice-dev/nodes/n1/metrics/cadvisor", nil), "vc-alice-dev"))
	if rec.Code != 502 || strings.Contains(rec.Body.String(), "garbage") {
		t.Fatalf("unparseable upstream must be 502 without echoing: %d %s", rec.Code, rec.Body)
	}
}

func TestKubeletMetricsOversizeIs502(t *testing.T) {
	big := strings.Repeat("# HELP x y\n", 2000) // ~22KB
	mux := http.NewServeMux()
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	vc := &v1alpha1.VirtualCluster{ObjectMeta: metav1.ObjectMeta{Name: "alice-dev"}}
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(vc, n).Build()
	(&KubeletMetricsHandler{Client: c, MaxBody: 1024, Fetch: func(context.Context, string, string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(big)), nil
	}}).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, withCN(httptest.NewRequest("GET", "/kubelet/alice-dev/nodes/n1/metrics/cadvisor", nil), "vc-alice-dev"))
	if rec.Code != 502 {
		t.Fatalf("oversize upstream must be 502 (never half the data), got %d", rec.Code)
	}
}

func TestKubeletMetricsConcurrencyLimitIs503(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = v1alpha1.AddToScheme(s)
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(&v1alpha1.VirtualCluster{ObjectMeta: metav1.ObjectMeta{Name: "alice-dev"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}).Build()
	h := &KubeletMetricsHandler{Client: c, MaxConcurrent: 1, Fetch: func(context.Context, string, string) (io.ReadCloser, error) {
		started <- struct{}{}
		<-release
		return io.NopCloser(strings.NewReader("machine_cpu_cores 1\n")), nil
	}}
	mux := http.NewServeMux()
	h.Register(mux)
	done := make(chan int)
	go func() {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, withCN(httptest.NewRequest("GET", "/kubelet/alice-dev/nodes/n1/metrics/cadvisor", nil), "vc-alice-dev"))
		done <- rec.Code
	}()
	<-started
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, withCN(httptest.NewRequest("GET", "/kubelet/alice-dev/nodes/n1/metrics/cadvisor", nil), "vc-alice-dev"))
	if rec.Code != 503 {
		t.Fatalf("second concurrent request must be 503, got %d", rec.Code)
	}
	close(release)
	if code := <-done; code != 200 {
		t.Fatalf("first request: %d", code)
	}
}
