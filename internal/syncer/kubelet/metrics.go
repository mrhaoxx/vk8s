package kubelet

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"

	"vk8s.io/vk8s/internal/kubeletmetrics"
)

// targetConn 隧道交给假 kubelet 的连接：记住 CONNECT 的目标 host:port（apiserver 要连的节点地址）。
type targetConn struct {
	net.Conn
	target string
}

type targetKey struct{}

// connContext http.Server.ConnContext：把 CONNECT 目标放进请求 context。
func connContext(ctx context.Context, c net.Conn) context.Context {
	if tc, ok := c.(*tls.Conn); ok {
		c = tc.NetConn()
	}
	if t, ok := c.(*targetConn); ok {
		return context.WithValue(ctx, targetKey{}, t.target)
	}
	return ctx
}

// TargetHost 请求经隧道到达时 CONNECT 的目标主机（节点 IP）；不是经隧道来的返回空串。
func TargetHost(ctx context.Context) string {
	t, _ := ctx.Value(targetKey{}).(string)
	if t == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(t); err == nil {
		return host
	}
	return t
}

// metrics GET /metrics/{path}：cadvisor / resource 经 manager 取数（manager 已按集群过滤），再把名字翻回虚拟名。
func (k *FakeKubelet) metrics(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if k.ManagerMetrics == nil || k.NodeForIP == nil || k.HostPodLookup == nil {
			http.NotFound(w, r)
			return
		}
		node, ok := k.NodeForIP(r.Context(), TargetHost(r.Context()))
		if !ok {
			http.NotFound(w, r)
			return
		}
		body, code, err := k.ManagerMetrics(r.Context(), node, path)
		if err != nil {
			k.Log.Info("kubelet metrics via manager failed", "node", node, "path", path, "err", err.Error())
			http.Error(w, "metrics unavailable", http.StatusBadGateway)
			return
		}
		defer body.Close()
		if code != http.StatusOK {
			msg, _ := io.ReadAll(io.LimitReader(body, 4096))
			http.Error(w, string(msg), code)
			return
		}
		// 每个 Pod 只查一次：一次抓取有成千上万个样本，但 Pod 只有几十个
		type res struct {
			ns, name string
			ok       bool
		}
		memo := map[string]res{}
		out, err := kubeletmetrics.Rename(body, k.HostNamespace, func(hostPod string) (string, string, bool) {
			if v, ok := memo[hostPod]; ok {
				return v.ns, v.name, v.ok
			}
			ns, name, ok := k.HostPodLookup(r.Context(), hostPod)
			memo[hostPod] = res{ns, name, ok}
			return ns, name, ok
		})
		if err != nil {
			http.Error(w, "metrics unparseable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", kubeletmetrics.ContentType)
		_, _ = w.Write(out)
	}
}
