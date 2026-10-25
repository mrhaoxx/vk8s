package manager

import (
	"encoding/json"
	"net/http"
	"time"

	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/names"
)

// maxAuditWebhookBody 一批审计事件的体上限（apiserver 侧单批 10 MiB）。
const maxAuditWebhookBody = 16 << 20

// AuditWebhookHandler 接收虚拟 apiserver 的审计 webhook（设计 2026-10-24 审计 §4.2）。
// 与 TokenReview 同一条 mTLS 通道：客户端证书 CN 必须等于路径里集群的宿主 ns 名，集群名一律取自 CN。
type AuditWebhookHandler struct {
	Pipeline *audit.Pipeline
	// Now 可注入以便测试；为空取 time.Now。
	Now func() time.Time
}

// Register 挂到 mux：POST /webhook/audit/{vc}。
func (h *AuditWebhookHandler) Register(mux *http.ServeMux) {
	mux.Handle("POST "+names.AuditWebhookPathPrefix+"{vc}", h)
}

func (h *AuditWebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log := logf.FromContext(r.Context()).WithName("audit-webhook")
	vc := r.PathValue("vc")
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		http.Error(w, "client certificate required", http.StatusForbidden)
		return
	}
	if cn := r.TLS.PeerCertificates[0].Subject.CommonName; cn != names.HostNamespace(vc) {
		log.Info("audit webhook rejected: certificate CN does not match path", "cn", cn, "vcluster", vc)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var list auditv1.EventList
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuditWebhookBody)).Decode(&list); err != nil {
		http.Error(w, "invalid EventList body", http.StatusBadRequest)
		return
	}
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	events := audit.FromEventList(vc, &list, now)
	if !h.Pipeline.Enqueue(events...) {
		log.Info("audit queue full, asking apiserver to retry", "vcluster", vc, "events", len(events))
		http.Error(w, "audit queue full", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}
