package clickhouse

import (
	"strings"
	"testing"
	"time"

	"vk8s.io/vk8s/internal/audit"
)

func TestBuildQueryOnlyBindsSetFilters(t *testing.T) {
	from := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	sql, params := BuildQuery("vk8s_audit", audit.Filter{From: from, To: from.Add(24 * time.Hour), VCluster: "alice-dev", Verb: "create", Limit: 100})
	for _, want := range []string{"FROM vk8s_audit.events", "ts >= {from:DateTime64(3)}", "ts < {to:DateTime64(3)}", "vcluster = {vcluster:String}", "verb = {verb:String}", "ORDER BY ts DESC, id DESC", "LIMIT {limit:UInt32}"} {
		if !strings.Contains(sql, want) {
			t.Errorf("sql lacks %q:\n%s", want, sql)
		}
	}
	for _, absent := range []string{"user =", "resource =", "before_ts", "response_code <", "response_code >="} {
		if strings.Contains(sql, absent) {
			t.Errorf("sql must not contain %q", absent)
		}
	}
	if params["from"] != "2026-10-23 00:00:00.000" || params["vcluster"] != "alice-dev" || params["limit"] != "100" {
		t.Fatalf("params = %v", params)
	}
	if strings.Contains(sql, "alice-dev") {
		t.Fatal("filter values must never be inlined")
	}
}

func TestBuildQueryCursorAndOutcome(t *testing.T) {
	sql, params := BuildQuery("db", audit.Filter{From: time.Unix(0, 0).UTC(), To: time.Unix(1, 0).UTC(), Outcome: "failure", BeforeTS: time.Date(2026, 10, 24, 6, 0, 0, 500e6, time.UTC), BeforeID: "abc", Limit: 5})
	if !strings.Contains(sql, "response_code >= 400") || !strings.Contains(sql, "(ts, id) < ({before_ts:DateTime64(3)}, {before_id:String})") {
		t.Fatal(sql)
	}
	if params["before_ts"] != "2026-10-24 06:00:00.500" || params["before_id"] != "abc" {
		t.Fatalf("params = %v", params)
	}
	sql, _ = BuildQuery("db", audit.Filter{From: time.Unix(0, 0), To: time.Unix(1, 0), Outcome: "success", Source: audit.SourcePlatform, Limit: 1})
	if !strings.Contains(sql, "response_code < 400") || !strings.Contains(sql, "source = {source:String}") {
		t.Fatal(sql)
	}
}

func TestSchemaHasTTL(t *testing.T) {
	stmts := Schema("vk8s_audit", 365)
	if len(stmts) != 3 || !strings.Contains(stmts[1], "ORDER BY (vcluster, ts, id)") || !strings.Contains(stmts[2], "INTERVAL 365 DAY") || !strings.Contains(stmts[0], "CREATE DATABASE IF NOT EXISTS vk8s_audit") {
		t.Fatalf("stmts = %v", stmts)
	}
}

func TestSchemaHasIDIndex(t *testing.T) {
	stmts := Schema("vk8s_audit", 365)
	if !strings.Contains(stmts[1], "INDEX idx_id id TYPE bloom_filter") {
		t.Fatalf("detail lookups by id need a skipping index:\n%s", stmts[1])
	}
}

func TestBuildQueryBindsTokenFilter(t *testing.T) {
	from := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	sql, params := BuildQuery("db", audit.Filter{From: from, To: from.Add(time.Hour), Token: "tok-abc", Limit: 10})
	if !strings.Contains(sql, "annotations['vk8s.io/token-id'] = {token:String}") || params["token"] != "tok-abc" {
		t.Fatalf("sql=%s params=%v", sql, params)
	}
	sql, _ = BuildQuery("db", audit.Filter{From: from, To: from.Add(time.Hour), Limit: 10})
	if strings.Contains(sql, "annotations[") {
		t.Fatalf("token condition must be absent when unset:\n%s", sql)
	}
}
