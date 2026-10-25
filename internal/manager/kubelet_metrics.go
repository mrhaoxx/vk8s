package manager

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/kubeletmetrics"
	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/placement"
)

const (
	defaultKubeletMetricsBody = 32 << 20
	defaultKubeletConcurrency = 8
	kubeletFetchTimeout       = 8 * time.Second
)

var (
	kubeletProxyRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "vk8s_manager_kubelet_proxy_requests_total", Help: "kubelet metrics proxied for virtual clusters, by path and status code.",
	}, []string{"path", "code"})
	kubeletProxyDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "vk8s_manager_kubelet_proxy_duration_seconds", Help: "Time to fetch and filter kubelet metrics.", Buckets: prometheus.DefBuckets,
	})
)

func init() { crmetrics.Registry.MustRegister(kubeletProxyRequests, kubeletProxyDuration) }

// KubeletMetricsHandler 虚拟集群的 cAdvisor / resource 指标（设计 2026-10-26 §3.3）：manager 是隔离边界。
// 与 TokenReview / 审计 webhook 同一条 mTLS 通道，客户端证书 CN 必须等于路径里集群的宿主 ns 名。
type KubeletMetricsHandler struct {
	Client client.Reader
	// Fetch 取宿主 kubelet 的 /metrics/<path>（经宿主 apiserver 的节点代理）。
	Fetch func(ctx context.Context, node, path string) (io.ReadCloser, error)
	// MaxBody 宿主返回的上限（零值 32 MiB）；超出 502，绝不转出半截数据。
	MaxBody int64
	// MaxConcurrent 同时处理的请求数（零值 8）；满了 503，Prometheus 下一轮再抓。解析会把原文放大数倍，必须有背压。
	MaxConcurrent int

	semOnce sync.Once
	sem     chan struct{}
}

// Register 挂到 mux。
func (h *KubeletMetricsHandler) Register(mux *http.ServeMux) {
	mux.Handle("GET /kubelet/{vc}/nodes/{node}/metrics/{path}", h)
}

func (h *KubeletMetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	path := r.PathValue("path")
	pathLabel := path
	if path != "cadvisor" && path != "resource" {
		pathLabel = "other" // 指标标签基数有界
	}
	code := http.StatusOK
	defer func() {
		kubeletProxyRequests.WithLabelValues(pathLabel, strconv.Itoa(code)).Inc()
		kubeletProxyDuration.Observe(time.Since(start).Seconds())
	}()
	fail := func(c int, msg string) {
		code = c
		http.Error(w, msg, c)
	}
	vcName, nodeName := r.PathValue("vc"), r.PathValue("node")
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != names.HostNamespace(vcName) {
		fail(http.StatusForbidden, "forbidden")
		return
	}
	if path != "cadvisor" && path != "resource" {
		fail(http.StatusNotFound, "not found")
		return
	}
	vc := &v1alpha1.VirtualCluster{}
	if err := h.Client.Get(r.Context(), client.ObjectKey{Name: vcName}, vc); err != nil {
		fail(http.StatusNotFound, "virtual cluster not found")
		return
	}
	node := &corev1.Node{}
	if err := h.Client.Get(r.Context(), client.ObjectKey{Name: nodeName}, node); err != nil {
		if apierrors.IsNotFound(err) {
			fail(http.StatusNotFound, "node not found")
		} else {
			fail(http.StatusBadGateway, "read node")
		}
		return
	}
	if !placement.Allows(placement.FromVCluster(vc), node) {
		fail(http.StatusNotFound, "node not found")
		return
	}
	h.semOnce.Do(func() {
		n := h.MaxConcurrent
		if n <= 0 {
			n = defaultKubeletConcurrency
		}
		h.sem = make(chan struct{}, n)
	})
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	default:
		fail(http.StatusServiceUnavailable, "busy")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), kubeletFetchTimeout)
	defer cancel()
	body, err := h.Fetch(ctx, nodeName, path)
	if err != nil {
		logf.FromContext(r.Context()).Info("kubelet metrics fetch failed", "vcluster", vcName, "node", nodeName, "path", path, "err", err.Error())
		fail(http.StatusBadGateway, "kubelet metrics unavailable")
		return
	}
	defer body.Close()
	max := h.MaxBody
	if max <= 0 {
		max = defaultKubeletMetricsBody
	}
	raw, err := io.ReadAll(io.LimitReader(body, max+1))
	if err != nil {
		fail(http.StatusBadGateway, "read kubelet metrics")
		return
	}
	if int64(len(raw)) > max {
		fail(http.StatusBadGateway, "kubelet metrics too large")
		return
	}
	out, err := kubeletmetrics.Filter(bytes.NewReader(raw), names.HostNamespace(vcName))
	if err != nil {
		fail(http.StatusBadGateway, "kubelet metrics unparseable")
		return
	}
	w.Header().Set("Content-Type", kubeletmetrics.ContentType)
	_, _ = w.Write(out)
}

// HostKubeletFetch 经宿主 apiserver 的 /api/v1/nodes/<node>/proxy/metrics/<path> 取数（需要 nodes/proxy）。
func HostKubeletFetch(cs kubernetes.Interface) func(ctx context.Context, node, path string) (io.ReadCloser, error) {
	return func(ctx context.Context, node, path string) (io.ReadCloser, error) {
		return cs.CoreV1().RESTClient().Get().AbsPath("/api/v1/nodes", node, "proxy", "metrics", path).Stream(ctx)
	}
}
