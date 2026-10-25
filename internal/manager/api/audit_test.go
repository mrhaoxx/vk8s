package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/audit/clickhouse"
)

type fakeQuerier struct {
	events []audit.Event
	last   audit.Filter
	err    error
}

func (f *fakeQuerier) Query(_ context.Context, fl audit.Filter) ([]audit.Event, error) {
	f.last = fl
	if f.err != nil {
		return nil, f.err
	}
	out := []audit.Event{}
	for _, e := range f.events {
		if fl.VCluster == "" || e.VCluster == fl.VCluster {
			out = append(out, e)
		}
	}
	if fl.Limit > 0 && len(out) > fl.Limit {
		out = out[:fl.Limit]
	}
	return out, nil
}

func (f *fakeQuerier) Get(_ context.Context, id, vc string) (*audit.Event, error) {
	for _, e := range f.events {
		if e.ID == id && (vc == "" || e.VCluster == vc) {
			return &e, nil
		}
	}
	return nil, nil
}

func TestParseAuditFilter(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	f, err := parseAuditFilter(url.Values{}, now)
	if err != nil || !f.From.Equal(now.Add(-24*time.Hour)) || !f.To.Equal(now) || f.Limit != 100 {
		t.Fatalf("f=%+v err=%v", f, err)
	}
	f, err = parseAuditFilter(url.Values{"before": {"1792821718312:abc"}, "limit": {"9999"}, "outcome": {"failure"}, "source": {"kube"}, "verb": {"create"}}, now)
	if err != nil || f.Limit != 500 || f.BeforeID != "abc" || f.BeforeTS.UnixMilli() != 1792821718312 || f.Outcome != "failure" || f.Source != audit.SourceKube || f.Verb != "create" {
		t.Fatalf("f=%+v err=%v", f, err)
	}
	if f, err := parseAuditFilter(url.Values{"token": {"tok-abc"}}, now); err != nil || f.Token != "tok-abc" {
		t.Fatalf("token filter: f=%+v err=%v", f, err)
	}
	if _, err := parseAuditFilter(url.Values{"from": {"2026-01-01T00:00:00Z"}, "to": {"2026-06-01T00:00:00Z"}}, now); err == nil {
		t.Fatal("window > 90d must fail")
	}
	if _, err := parseAuditFilter(url.Values{"outcome": {"maybe"}}, now); err == nil {
		t.Fatal("unknown outcome must fail")
	}
	if _, err := parseAuditFilter(url.Values{"before": {"garbage"}}, now); err == nil {
		t.Fatal("bad cursor must fail")
	}
}

func TestListAuditEventsPermissions(t *testing.T) {
	fq := &fakeQuerier{events: []audit.Event{
		{ID: "1", VCluster: "alice-dev", TS: apiAt(10), Source: audit.SourceKube},
		{ID: "2", VCluster: "team-x", TS: apiAt(9), Source: audit.SourceKube},
		{ID: "3", VCluster: "", Source: audit.SourcePlatform, TS: apiAt(8)},
	}}
	s, _ := newAPITest(t, apiVC("alice-dev", "alice", "bob"), apiVC("team-x", "bob"))
	s.Audit = fq
	if rec := do(t, s.listAuditEvents, apiUser("alice", false), http.MethodGet, "/api/v1/audit/events", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("tenant without vcluster: %d", rec.Code)
	}
	if rec := do(t, s.listAuditEvents, apiUser("alice", false), http.MethodGet, "/api/v1/audit/events?vcluster=team-x", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("tenant foreign cluster: %d", rec.Code)
	}
	if rec := do(t, s.listAuditEvents, apiUser("alice", false), http.MethodGet, "/api/v1/audit/events?vcluster=nope", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("tenant missing cluster must also be 404: %d", rec.Code)
	}
	rec := do(t, s.listAuditEvents, apiUser("alice", false), http.MethodGet, "/api/v1/audit/events?vcluster=alice-dev", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tenant own: %d %s", rec.Code, rec.Body)
	}
	if v := decodeBody[AuditListView](t, rec); len(v.Items) != 1 || v.Items[0].ID != "1" || fq.last.VCluster != "alice-dev" || v.NextBefore != "" {
		t.Fatalf("v=%+v last=%+v", v, fq.last)
	}
	rec = do(t, s.listAuditEvents, apiUser("admin", true), http.MethodGet, "/api/v1/audit/events?limit=2", nil, nil)
	v := decodeBody[AuditListView](t, rec)
	if len(v.Items) != 2 || v.NextBefore != fmt.Sprintf("%d:2", apiAt(9).UnixMilli()) {
		t.Fatalf("v=%+v", v)
	}
	if strings.Contains(rec.Body.String(), "requestObject") {
		t.Fatal("list must not carry request bodies")
	}
}

func TestAuditDisabledAndUnavailable(t *testing.T) {
	s, _ := newAPITest(t)
	if rec := do(t, s.listAuditEvents, apiUser("admin", true), http.MethodGet, "/api/v1/audit/events", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled: %d", rec.Code)
	}
	s.Audit = &fakeQuerier{err: fmt.Errorf("wrap: %w", clickhouse.ErrUnavailable)}
	if rec := do(t, s.listAuditEvents, apiUser("admin", true), http.MethodGet, "/api/v1/audit/events", nil, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable: %d", rec.Code)
	}
}

func TestGetAuditEventScopesTenantToCluster(t *testing.T) {
	fq := &fakeQuerier{events: []audit.Event{{ID: "1", VCluster: "alice-dev", RequestObject: `{"a":1}`, TS: apiAt(1)}}}
	s, _ := newAPITest(t, apiVC("alice-dev", "alice"), apiVC("team-x", "bob"))
	s.Audit = fq
	rec := do(t, s.getAuditEvent, apiUser("alice", false), http.MethodGet, "/api/v1/audit/events/1?vcluster=alice-dev", nil, map[string]string{"id": "1"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"requestObject":"{\"a\":1}"`) {
		t.Fatalf("own: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, s.getAuditEvent, apiUser("bob", false), http.MethodGet, "/api/v1/audit/events/1?vcluster=team-x", nil, map[string]string{"id": "1"}); rec.Code != http.StatusNotFound {
		t.Fatalf("other cluster must 404: %d", rec.Code)
	}
	if rec := do(t, s.getAuditEvent, apiUser("bob", false), http.MethodGet, "/api/v1/audit/events/1", nil, map[string]string{"id": "1"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("tenant without vcluster: %d", rec.Code)
	}
	if rec := do(t, s.getAuditEvent, apiUser("admin", true), http.MethodGet, "/api/v1/audit/events/1", nil, map[string]string{"id": "1"}); rec.Code != http.StatusOK {
		t.Fatalf("admin: %d", rec.Code)
	}
}

func TestConfigViewExposesAuditEnabled(t *testing.T) {
	s, _ := newAPITest(t)
	if configView(s.Config).AuditEnabled {
		t.Fatal("disabled by default")
	}
	s.Config.Audit.Enabled = true
	if !configView(s.Config).AuditEnabled {
		t.Fatal("must follow config")
	}
}

func TestAuditEventViewKeepsMilliseconds(t *testing.T) {
	v := auditEventView(audit.Event{ID: "x", TS: time.Date(2026, 10, 24, 6, 1, 58, 312e6, time.UTC)}, false)
	b, _ := json.Marshal(v)
	if !strings.Contains(string(b), `"ts":"2026-10-24T06:01:58.312Z"`) {
		t.Fatalf("ts must keep milliseconds: %s", b)
	}
}
