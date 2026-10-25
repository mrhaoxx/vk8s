package kubelet

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/httpstream"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/apimachinery/pkg/util/proxy"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/transport"

	"vk8s.io/vk8s/internal/syncer/translate"
)

// FakeKubelet 应答 apiserver 的 kubelet 子资源请求，翻成宿主 Pod 名后转发到宿主 apiserver 的
// pods/log|exec|attach|portforward 子资源（用 syncer 自己的 SA）。
type FakeKubelet struct {
	Log           logr.Logger
	HostNamespace string
	// hostAPI 宿主 apiserver 地址。
	hostAPI *url.URL
	// transport 普通请求（logs）用：带 bearer token 与 CA。
	transport http.RoundTripper
	// upgradeTransport upgrade 请求（exec/attach/portForward）用：底层 TLS 拨号 + 写入 bearer token。
	upgradeTransport proxy.UpgradeRequestRoundTripper
	// PodExists 判定宿主 Pod 是否存在且由 syncer 管理；nil 表示不检查，交给宿主 apiserver 返回 404。
	PodExists func(ctx context.Context, hostPodName string) bool

	// 指标（设计 2026-10-26）：三者都设置时才提供 /metrics/cadvisor 与 /metrics/resource。
	// NodeForIP 节点 IP（隧道 CONNECT 的目标）→ 虚拟 Node 名。
	NodeForIP func(ctx context.Context, ip string) (string, bool)
	// ManagerMetrics 经 manager（mTLS）取已按本集群过滤的指标；返回 body 与 manager 的状态码。
	ManagerMetrics func(ctx context.Context, node, path string) (io.ReadCloser, int, error)
	// HostPodLookup 宿主 Pod 名 → 虚拟 (namespace, name)。
	HostPodLookup func(ctx context.Context, hostPod string) (string, string, bool)
}

// NewFakeKubelet 用宿主 rest.Config 构造。
func NewFakeKubelet(log logr.Logger, hostCfg *rest.Config, hostNamespace string, podExists func(context.Context, string) bool) (*FakeKubelet, error) {
	hostAPI, err := url.Parse(hostCfg.Host)
	if err != nil {
		return nil, fmt.Errorf("parse host apiserver url %q: %w", hostCfg.Host, err)
	}
	rt, err := rest.TransportFor(hostCfg)
	if err != nil {
		return nil, fmt.Errorf("build host transport: %w", err)
	}
	tlsCfg, err := rest.TLSConfigFor(hostCfg)
	if err != nil {
		return nil, fmt.Errorf("build host tls config: %w", err)
	}
	dialer := utilnet.SetTransportDefaults(&http.Transport{TLSClientConfig: tlsCfg})
	bearer, err := transport.NewBearerAuthWithRefreshRoundTripper(hostCfg.BearerToken, hostCfg.BearerTokenFile, proxy.MirrorRequest)
	if err != nil {
		return nil, fmt.Errorf("build bearer round tripper: %w", err)
	}
	return &FakeKubelet{
		Log:              log,
		HostNamespace:    hostNamespace,
		hostAPI:          hostAPI,
		transport:        rt,
		upgradeTransport: proxy.NewUpgradeRequestRoundTripper(dialer, bearer),
		PodExists:        podExists,
	}, nil
}

// Handler kubelet 路由。
func (k *FakeKubelet) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /containerLogs/{namespace}/{pod}/{container}", k.logs)
	mux.HandleFunc("GET /metrics/cadvisor", k.metrics("cadvisor"))
	mux.HandleFunc("GET /metrics/resource", k.metrics("resource"))
	mux.HandleFunc("/exec/{namespace}/{pod}/{container}", k.stream("exec"))
	mux.HandleFunc("/attach/{namespace}/{pod}/{container}", k.stream("attach"))
	mux.HandleFunc("/portForward/{namespace}/{pod}", k.portForward)
	return mux
}

// resolve 把路径里的虚 ns/pod 翻成宿主 Pod 名；不存在则 404。
func (k *FakeKubelet) resolve(w http.ResponseWriter, r *http.Request) (string, bool) {
	vns, vpod := r.PathValue("namespace"), r.PathValue("pod")
	if vns == "" || vpod == "" {
		http.Error(w, "namespace and pod are required", http.StatusBadRequest)
		return "", false
	}
	hostPod := translate.HostName(vpod, vns)
	if k.PodExists != nil && !k.PodExists(r.Context(), hostPod) {
		http.Error(w, fmt.Sprintf("pod %s/%s not found", vns, vpod), http.StatusNotFound)
		return "", false
	}
	return hostPod, true
}

// subresourceURL 宿主 apiserver 上 Pod 子资源的完整 URL。
func (k *FakeKubelet) subresourceURL(hostPod, subresource string, q url.Values) *url.URL {
	u := *k.hostAPI
	u.Path = path.Join(u.Path, "api/v1/namespaces", k.HostNamespace, "pods", hostPod, subresource)
	u.RawQuery = q.Encode()
	return &u
}

// logs GET /containerLogs/{ns}/{pod}/{container}：流式透传 pods/log。
// kubelet 与 apiserver 的 log 参数同名，加 container 后透传。
func (k *FakeKubelet) logs(w http.ResponseWriter, r *http.Request) {
	hostPod, ok := k.resolve(w, r)
	if !ok {
		return
	}
	in := r.URL.Query()
	q := url.Values{}
	q.Set("container", r.PathValue("container"))
	for _, p := range []string{"follow", "previous", "sinceSeconds", "sinceTime", "timestamps", "tailLines", "limitBytes", "stream"} {
		if v := in.Get(p); v != "" {
			q.Set(p, v)
		}
	}
	loc := k.subresourceURL(hostPod, "log", q)
	h := proxy.NewUpgradeAwareHandler(loc, k.transport, false, false, k.responder())
	h.UseLocationHost = true
	// 负值表示立即刷出，follow 日志才不会被缓冲
	h.FlushInterval = -1
	req := r.Clone(r.Context())
	req.URL.RawQuery = loc.RawQuery
	h.ServeHTTP(w, req)
}

// stream /exec 与 /attach：kubelet 的 input/output/error/tty/command 参数翻成 apiserver 子资源的
// stdin/stdout/stderr/tty/command，SPDY/WebSocket upgrade 双向透传。
func (k *FakeKubelet) stream(subresource string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostPod, ok := k.resolve(w, r)
		if !ok {
			return
		}
		in := r.URL.Query()
		q := url.Values{}
		q.Set("container", r.PathValue("container"))
		for _, c := range in["command"] {
			q.Add("command", c)
		}
		for from, to := range map[string]string{"input": "stdin", "output": "stdout", "error": "stderr", "tty": "tty"} {
			if v := in.Get(from); v != "" && v != "0" && v != "false" {
				q.Set(to, "true")
			}
		}
		k.upgrade(w, r, k.subresourceURL(hostPod, subresource, q))
	}
}

// portForward /portForward/{ns}/{pod}：kubelet 的 port 参数翻成 apiserver 的 ports（SPDY 时端口在流头里协商）。
func (k *FakeKubelet) portForward(w http.ResponseWriter, r *http.Request) {
	hostPod, ok := k.resolve(w, r)
	if !ok {
		return
	}
	in := r.URL.Query()
	q := url.Values{}
	for _, p := range in["port"] {
		q.Add("ports", p)
	}
	for _, p := range in["ports"] {
		q.Add("ports", p)
	}
	k.upgrade(w, r, k.subresourceURL(hostPod, "portforward", q))
}

// upgrade 把 upgrade 请求整体代理到宿主 apiserver。
func (k *FakeKubelet) upgrade(w http.ResponseWriter, r *http.Request, loc *url.URL) {
	if !httpstream.IsUpgradeRequest(r) {
		http.Error(w, "upgrade request required", http.StatusBadRequest)
		return
	}
	h := proxy.NewUpgradeAwareHandler(loc, k.transport, false, true, k.responder())
	h.UpgradeTransport = k.upgradeTransport
	h.UseLocationHost = true
	req := r.Clone(r.Context())
	req.URL.RawQuery = loc.RawQuery
	h.ServeHTTP(w, req)
}

func (k *FakeKubelet) responder() proxy.ErrorResponder {
	return errorResponder{log: k.Log}
}

// errorResponder 把代理错误写成 502。
type errorResponder struct {
	log logr.Logger
}

func (e errorResponder) Error(w http.ResponseWriter, _ *http.Request, err error) {
	e.log.V(1).Info("fake kubelet proxy error", "error", err.Error())
	http.Error(w, err.Error(), http.StatusBadGateway)
}
