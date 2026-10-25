package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/audit/clickhouse"
)

const (
	auditDefaultWindow = 24 * time.Hour
	auditMaxWindow     = 90 * 24 * time.Hour
	auditDefaultLimit  = 100
	auditMaxLimit      = 500
)

// parseAuditFilter 解析查询参数（设计 2026-10-24 审计 §5）。
func parseAuditFilter(q url.Values, now time.Time) (audit.Filter, error) {
	f := audit.Filter{To: now, From: now.Add(-auditDefaultWindow), Limit: auditDefaultLimit}
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, fmt.Errorf("from 不是 RFC3339 时间")
		}
		f.From = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, fmt.Errorf("to 不是 RFC3339 时间")
		}
		f.To = t
	} else if q.Get("from") != "" {
		f.To = now
	}
	if !f.To.After(f.From) {
		return f, fmt.Errorf("to 必须晚于 from")
	}
	if f.To.Sub(f.From) > auditMaxWindow {
		return f, fmt.Errorf("查询窗口最长 90 天")
	}
	f.VCluster = q.Get("vcluster")
	f.User = q.Get("user")
	f.Verb = q.Get("verb")
	f.Resource = q.Get("resource")
	f.Namespace = q.Get("namespace")
	f.Name = q.Get("name")
	f.Token = q.Get("token")
	switch src := q.Get("source"); src {
	case "", "kube", "platform":
		f.Source = audit.Source(src)
	default:
		return f, fmt.Errorf("source 只能是 kube 或 platform")
	}
	switch out := q.Get("outcome"); out {
	case "", "success", "failure":
		f.Outcome = out
	default:
		return f, fmt.Errorf("outcome 只能是 success 或 failure")
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return f, fmt.Errorf("limit 必须是正整数")
		}
		if n > auditMaxLimit {
			n = auditMaxLimit
		}
		f.Limit = n
	}
	if v := q.Get("before"); v != "" {
		ms, id, ok := strings.Cut(v, ":")
		n, err := strconv.ParseInt(ms, 10, 64)
		if !ok || err != nil || id == "" {
			return f, fmt.Errorf("before 游标格式应为 <毫秒时间戳>:<id>")
		}
		f.BeforeTS = time.UnixMilli(n).UTC()
		f.BeforeID = id
	}
	return f, nil
}

// auditScope 非管理员必须带 vcluster（缺 → 400）且是其 owner / member（否则 404，与 getVC 一致）。
// 返回 (vcluster 过滤值, 是否继续)。
func (s *Server) auditScope(w http.ResponseWriter, r *http.Request, me *v1alpha1.User, vcluster string) bool {
	if me.IsAdmin() {
		return true
	}
	if vcluster == "" {
		writeError(w, http.StatusBadRequest, "vcluster 必填")
		return false
	}
	vc, err := s.lookupVC(r, vcluster)
	if err != nil {
		s.internalError(w, r, err)
		return false
	}
	if vc == nil || !canView(vc, me) {
		writeError(w, http.StatusNotFound, "virtual cluster not found")
		return false
	}
	return true
}

func (s *Server) auditQueryError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, clickhouse.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "审计存储暂不可用")
		return
	}
	s.internalError(w, r, err)
}

// listAuditEvents GET /api/v1/audit/events。
func (s *Server) listAuditEvents(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	if s.Audit == nil {
		writeError(w, http.StatusNotFound, "audit disabled")
		return
	}
	f, err := parseAuditFilter(r.URL.Query(), s.now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.auditScope(w, r, me, f.VCluster) {
		return
	}
	events, err := s.Audit.Query(r.Context(), f)
	if err != nil {
		s.auditQueryError(w, r, err)
		return
	}
	items := make([]AuditEventView, 0, len(events))
	for i := range events {
		items = append(items, auditEventView(events[i], false))
	}
	next := ""
	if len(events) >= f.Limit && len(events) > 0 {
		last := events[len(events)-1]
		next = fmt.Sprintf("%d:%s", last.TS.UnixMilli(), last.ID)
	}
	writeJSON(w, http.StatusOK, AuditListView{Items: items, NextBefore: next})
}

// getAuditEvent GET /api/v1/audit/events/{id}?vcluster=：单条详情，含请求体。
func (s *Server) getAuditEvent(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	if s.Audit == nil {
		writeError(w, http.StatusNotFound, "audit disabled")
		return
	}
	vcluster := r.URL.Query().Get("vcluster")
	if !s.auditScope(w, r, me, vcluster) {
		return
	}
	// 管理员不带 vcluster 时不限集群；带了就按它限定（与列表一致）
	e, err := s.Audit.Get(r.Context(), r.PathValue("id"), vcluster)
	if err != nil {
		s.auditQueryError(w, r, err)
		return
	}
	if e == nil {
		writeError(w, http.StatusNotFound, "audit event not found")
		return
	}
	writeJSON(w, http.StatusOK, auditEventView(*e, true))
}
