// Command egressprobe 是设计文档第 13 节验证 2 的本机桩：复用 syncer 真实的 kubelet.ConnectServer，
// 在 konnectivity unix socket 上接收虚拟 apiserver egress selector 发来的 HTTP CONNECT，并按端口分流到
//   - 10250：桩 kubelet，/containerLogs 返回固定文本（验证 kubectl logs 走隧道 + kubelet 客户端证书）；
//   - 8443：桩 metrics.k8s.io/v1beta1，返回一个固定的 NodeMetrics（验证聚合层经隧道 + front-proxy 证书）。
//
// 只验证「apiserver → UDS HTTPConnect → 端口分流 → TLS 后端」这条链路；假 kubelet / metrics 的翻译逻辑
// 需要宿主集群，由 kind e2e 覆盖。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/kubelet"
)

const (
	groupName    = "metrics.k8s.io"
	groupVersion = groupName + "/v1beta1"
	basePath     = "/apis/" + groupVersion
)

func main() {
	socket := flag.String("socket", "", "konnectivity unix socket 路径（必填）")
	certsDir := flag.String("certs-dir", "", "hack/localcerts 生成的证书目录（必填）")
	nodeName := flag.String("node", "probe-node", "metrics 桩返回的 Node 名")
	flag.Parse()
	if *socket == "" || *certsDir == "" {
		fmt.Fprintln(os.Stderr, "-socket and -certs-dir are required")
		os.Exit(2)
	}
	if err := run(*socket, *certsDir, *nodeName); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(socket, certsDir, nodeName string) error {
	log := klog.Background().WithName("egressprobe")
	cert := func(key string) string { return filepath.Join(certsDir, key) }
	internalCA, err := os.ReadFile(cert(names.KeyInternalCA))
	if err != nil {
		return err
	}
	// 与 internal/syncer/command.go 完全相同的 TLS 要求：kubelet 侧要求内部 CA 签的客户端证书，
	// metrics 侧额外要求 CN=front-proxy-client。
	kubeletTLS, err := kubelet.ServerTLSConfig(cert(names.KeyFakeKubeletCert), cert(names.KeyFakeKubeletKey), internalCA, "")
	if err != nil {
		return fmt.Errorf("kubelet tls: %w", err)
	}
	metricsTLS, err := kubelet.ServerTLSConfig(cert(names.KeySyncerServingCert), cert(names.KeySyncerServingKey), internalCA, names.CNFrontProxyClient)
	if err != nil {
		return fmt.Errorf("metrics tls: %w", err)
	}
	srv := &kubelet.ConnectServer{
		Log:            log,
		SocketPath:     socket,
		KubeletHandler: logged(log.WithName("kubelet"), kubeletStub()),
		KubeletTLS:     kubeletTLS,
		MetricsHandler: logged(log.WithName("metrics"), metricsStub(nodeName)),
		MetricsTLS:     metricsTLS,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return srv.Start(ctx)
}

// logged 记录每个经隧道到达的请求与对端证书 CN。
func logged(log klog.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Info("request", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery, "clientCN", peerCN(r))
		h.ServeHTTP(w, r)
	})
}

func peerCN(r *http.Request) string {
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		return r.TLS.PeerCertificates[0].Subject.CommonName
	}
	return ""
}

func kubeletStub() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /containerLogs/{namespace}/{pod}/{container}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "egress-probe: logs for %s/%s/%s via CONNECT tunnel; client=%s\n",
			r.PathValue("namespace"), r.PathValue("pod"), r.PathValue("container"), peerCN(r))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "egress-probe kubelet stub: unhandled "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	})
	return mux
}

func metricsStub(nodeName string) http.Handler {
	group := metav1.APIGroup{
		TypeMeta:         metav1.TypeMeta{Kind: "APIGroup", APIVersion: "v1"},
		Name:             groupName,
		Versions:         []metav1.GroupVersionForDiscovery{{GroupVersion: groupVersion, Version: "v1beta1"}},
		PreferredVersion: metav1.GroupVersionForDiscovery{GroupVersion: groupVersion, Version: "v1beta1"},
	}
	resources := metav1.APIResourceList{
		TypeMeta:     metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"},
		GroupVersion: groupVersion,
		APIResources: []metav1.APIResource{
			{Name: "nodes", SingularName: "", Namespaced: false, Kind: "NodeMetrics", Verbs: metav1.Verbs{"get", "list"}},
			{Name: "pods", SingularName: "", Namespaced: true, Kind: "PodMetrics", Verbs: metav1.Verbs{"get", "list"}},
		},
	}
	node := func() metricsv1beta1.NodeMetrics {
		return metricsv1beta1.NodeMetrics{
			TypeMeta:   metav1.TypeMeta{Kind: "NodeMetrics", APIVersion: groupVersion},
			ObjectMeta: metav1.ObjectMeta{Name: nodeName},
			Timestamp:  metav1.Now(),
			Window:     metav1.Duration{Duration: 30 * time.Second},
			Usage: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("250m"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
		}
	}
	mux := http.NewServeMux()
	for _, p := range []string{"/healthz", "/readyz", "/livez"} {
		mux.HandleFunc("GET "+p, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	}
	mux.HandleFunc("GET /apis", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, metav1.APIGroupList{TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"}, Groups: []metav1.APIGroup{group}})
	})
	mux.HandleFunc("GET /apis/"+groupName, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, group) })
	mux.HandleFunc("GET "+basePath, func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, resources) })
	mux.HandleFunc("GET "+basePath+"/nodes", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, metricsv1beta1.NodeMetricsList{
			TypeMeta: metav1.TypeMeta{Kind: "NodeMetricsList", APIVersion: groupVersion},
			Items:    []metricsv1beta1.NodeMetrics{node()},
		})
	})
	mux.HandleFunc("GET "+basePath+"/nodes/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") != nodeName {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, node())
	})
	emptyPods := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, metricsv1beta1.PodMetricsList{TypeMeta: metav1.TypeMeta{Kind: "PodMetricsList", APIVersion: groupVersion}, Items: []metricsv1beta1.PodMetrics{}})
	}
	mux.HandleFunc("GET "+basePath+"/pods", emptyPods)
	mux.HandleFunc("GET "+basePath+"/namespaces/{namespace}/pods", emptyPods)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "egress-probe metrics stub: unhandled "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
