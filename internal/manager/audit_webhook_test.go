package manager

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"

	"vk8s.io/vk8s/internal/audit"
)

func withCN(r *http.Request, cn string) *http.Request {
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{Subject: pkix.Name{CommonName: cn}}}}
	return r
}

func eventList(vc string) []byte {
	b, _ := json.Marshal(auditv1.EventList{Items: []auditv1.Event{{AuditID: "a1", Verb: "get", Annotations: map[string]string{"vcluster": vc}}}})
	return b
}

func TestAuditWebhookRejectsMismatchedCN(t *testing.T) {
	p := audit.NewPipeline(audit.NopSink{}, audit.Options{})
	mux := http.NewServeMux()
	(&AuditWebhookHandler{Pipeline: p}).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, withCN(httptest.NewRequest("POST", "/webhook/audit/alice-dev", bytes.NewReader(eventList("x"))), "vc-team-x"))
	if rec.Code != 403 || p.Depth() != 0 {
		t.Fatalf("code=%d depth=%d", rec.Code, p.Depth())
	}
}

func TestAuditWebhookEnqueuesWithCNCluster(t *testing.T) {
	sink := &collectSink{}
	p := audit.NewPipeline(sink, audit.Options{BatchSize: 1, BatchInterval: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Run(ctx) }()
	mux := http.NewServeMux()
	(&AuditWebhookHandler{Pipeline: p}).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, withCN(httptest.NewRequest("POST", "/webhook/audit/alice-dev", bytes.NewReader(eventList("team-x"))), "vc-alice-dev"))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	waitFor(t, func() bool { return sink.count() == 1 })
	if e := sink.first(); e.VCluster != "alice-dev" || e.Source != audit.SourceKube || e.ID != "a1" {
		t.Fatalf("event = %+v", e)
	}
}

func TestAuditWebhookReturns503WhenQueueFull(t *testing.T) {
	p := audit.NewPipeline(audit.NopSink{}, audit.Options{QueueSize: 1}) // 不运行
	p.Enqueue(audit.Event{ID: "x"})
	mux := http.NewServeMux()
	(&AuditWebhookHandler{Pipeline: p}).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, withCN(httptest.NewRequest("POST", "/webhook/audit/alice-dev", bytes.NewReader(eventList(""))), "vc-alice-dev"))
	if rec.Code != 503 {
		t.Fatalf("code=%d", rec.Code)
	}
}

func TestAuditWebhookRejectsBadBody(t *testing.T) {
	p := audit.NewPipeline(audit.NopSink{}, audit.Options{})
	mux := http.NewServeMux()
	(&AuditWebhookHandler{Pipeline: p}).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, withCN(httptest.NewRequest("POST", "/webhook/audit/alice-dev", bytes.NewReader([]byte("not json"))), "vc-alice-dev"))
	if rec.Code != 400 {
		t.Fatalf("code=%d", rec.Code)
	}
}
