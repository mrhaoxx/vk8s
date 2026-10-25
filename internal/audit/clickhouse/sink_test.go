package clickhouse

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"vk8s.io/vk8s/internal/audit"
)

type recorded struct {
	method, query, body string
	headers             http.Header
}

type capture struct {
	mu   sync.Mutex
	reqs []recorded
}

func (c *capture) get(i int) recorded {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reqs[i]
}

func (c *capture) n() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

func newCH(t *testing.T, respond func(w http.ResponseWriter, r recorded)) (*httptest.Server, *capture) {
	t.Helper()
	c := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rd io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				t.Errorf("gzip: %v", err)
				return
			}
			rd = gz
		}
		body, _ := io.ReadAll(rd)
		rec := recorded{method: r.Method, query: r.URL.RawQuery, body: string(body), headers: r.Header.Clone()}
		c.mu.Lock()
		c.reqs = append(c.reqs, rec)
		c.mu.Unlock()
		respond(w, rec)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func TestWriteSendsGzipJSONEachRow(t *testing.T) {
	srv, c := newCH(t, func(w http.ResponseWriter, r recorded) { w.WriteHeader(200) })
	s, err := New(Config{URL: srv.URL, Database: "vk8s_audit", Username: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write(context.Background(), []audit.Event{{ID: "a", Source: audit.SourceKube}, {ID: "b"}}); err != nil {
		t.Fatal(err)
	}
	r := c.get(0)
	q, _ := url.ParseQuery(r.query)
	if !strings.Contains(q.Get("query"), "INSERT INTO vk8s_audit.events FORMAT JSONEachRow") || q.Get("date_time_input_format") != "best_effort" {
		t.Fatal(r.query)
	}
	if r.headers.Get("Content-Encoding") != "gzip" || r.headers.Get("X-ClickHouse-User") != "u" || r.headers.Get("X-ClickHouse-Key") != "p" {
		t.Fatalf("headers = %v", r.headers)
	}
	if lines := strings.Split(strings.TrimSpace(r.body), "\n"); len(lines) != 2 || !strings.Contains(lines[0], `"id":"a"`) {
		t.Fatalf("body = %q", r.body)
	}
}

func TestWriteMapsServerErrorToUnavailable(t *testing.T) {
	srv, _ := newCH(t, func(w http.ResponseWriter, r recorded) { http.Error(w, "boom", 503) })
	s, _ := New(Config{URL: srv.URL})
	if err := s.Write(context.Background(), []audit.Event{{ID: "a"}}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	down, _ := New(Config{URL: "http://127.0.0.1:1"})
	if err := down.Ping(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("connection refused must map to ErrUnavailable, got %v", err)
	}
}

func TestQueryDecodesRows(t *testing.T) {
	srv, c := newCH(t, func(w http.ResponseWriter, r recorded) {
		fmt.Fprintln(w, `{"ts":"2026-10-24 06:01:58.312","received_at":"2026-10-24 06:01:59.000","source":"kube","vcluster":"alice-dev","id":"x","stage":"ResponseComplete","level":"Request","verb":"create","user":"alice","groups":["vk8s:admins"],"impersonated_user":"","source_ips":["10.0.0.1"],"user_agent":"kubectl","namespace":"demo","api_group":"apps","api_version":"v1","resource":"deployments","subresource":"","name":"api","request_uri":"/apis/apps/v1/namespaces/demo/deployments","response_code":201,"response_reason":"","response_message":"","request_object":"","request_truncated":0,"annotations":{"authorization.k8s.io/decision":"allow"},"platform_action":"","platform_target":""}`)
	})
	s, _ := New(Config{URL: srv.URL})
	got, err := s.Query(context.Background(), audit.Filter{From: time.Unix(0, 0), To: time.Unix(10, 0), User: "alice", Limit: 10})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v err %v", got, err)
	}
	if !got[0].TS.Equal(time.Date(2026, 10, 24, 6, 1, 58, 312e6, time.UTC)) || got[0].ResponseCode != 201 || got[0].Groups[0] != "vk8s:admins" || got[0].Annotations["authorization.k8s.io/decision"] != "allow" {
		t.Fatalf("event = %+v", got[0])
	}
	q, _ := url.ParseQuery(c.get(0).query)
	if q.Get("param_user") != "alice" || q.Get("default_format") != "JSONEachRow" || q.Get("max_execution_time") != "10" {
		t.Fatalf("query = %v", q)
	}
	if !strings.Contains(c.get(0).body, "SELECT") {
		t.Fatalf("SQL must travel in the body, got %q", c.get(0).body)
	}
}

func TestGetReturnsNilWhenMissing(t *testing.T) {
	srv, _ := newCH(t, func(w http.ResponseWriter, r recorded) { w.WriteHeader(200) })
	s, _ := New(Config{URL: srv.URL})
	e, err := s.Get(context.Background(), "nope", "alice-dev")
	if err != nil || e != nil {
		t.Fatalf("e=%v err=%v", e, err)
	}
}

func TestMigrateRunsThreeStatements(t *testing.T) {
	srv, c := newCH(t, func(w http.ResponseWriter, r recorded) { w.WriteHeader(200) })
	s, _ := New(Config{URL: srv.URL, Database: "d"})
	if err := s.Migrate(context.Background(), 90); err != nil {
		t.Fatal(err)
	}
	if c.n() != 3 || !strings.Contains(c.get(0).body, "CREATE DATABASE IF NOT EXISTS d") || !strings.Contains(c.get(2).body, "INTERVAL 90 DAY") {
		t.Fatalf("reqs = %+v", c.reqs)
	}
}

func TestMigrateDoesNotMaterializeTTLOnEveryStart(t *testing.T) {
	srv, c := newCH(t, func(w http.ResponseWriter, r recorded) { w.WriteHeader(200) })
	s, _ := New(Config{URL: srv.URL, Database: "d"})
	if err := s.Migrate(context.Background(), 30); err != nil {
		t.Fatal(err)
	}
	q, _ := url.ParseQuery(c.get(2).query)
	if q.Get("materialize_ttl_after_modify") != "0" {
		t.Fatalf("MODIFY TTL must not schedule a full-table materialization, query = %v", q)
	}
}

func TestErrorMappingForTimeoutsAndMissingTable(t *testing.T) {
	cases := []struct {
		status int
		code   string
		want   bool
	}{
		{408, "159", true}, // TIMEOUT_EXCEEDED
		{404, "60", true},  // UNKNOWN_TABLE（迁移未完成）
		{404, "81", true},  // UNKNOWN_DATABASE
		{429, "", true},
		{403, "516", false}, // AUTHENTICATION_FAILED：运维可修，但不是「暂不可用」
		{400, "27", false},  // CANNOT_PARSE_INPUT
	}
	for _, c := range cases {
		srv, _ := newCH(t, func(w http.ResponseWriter, r recorded) {
			if c.code != "" {
				w.Header().Set("X-ClickHouse-Exception-Code", c.code)
			}
			w.WriteHeader(c.status)
			_, _ = w.Write([]byte("boom"))
		})
		s, _ := New(Config{URL: srv.URL})
		err := s.Write(context.Background(), []audit.Event{{ID: "a"}})
		if got := errors.Is(err, ErrUnavailable); got != c.want {
			t.Errorf("status %d code %s: unavailable=%v want %v (err=%v)", c.status, c.code, got, c.want, err)
		}
	}
}
