// Package clickhouse 用 ClickHouse 的 HTTP 接口实现审计存储：JSONEachRow 批量插入、类型化参数查询。
package clickhouse

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"vk8s.io/vk8s/internal/audit"
)

// ErrUnavailable 存储暂时不可达：连接失败、5xx、408/429，以及 ClickHouse 异常码里的超时 / 缺库缺表（迁移未完成）/ 过载。
// 与 audit.ErrUnavailable 是同一个值，管线与 API 都按它判断。
var ErrUnavailable = audit.ErrUnavailable

// unavailableExceptionCodes X-ClickHouse-Exception-Code 里视为「暂不可用」的异常码。
var unavailableExceptionCodes = map[string]bool{
	"159": true, // TIMEOUT_EXCEEDED
	"60":  true, // UNKNOWN_TABLE
	"81":  true, // UNKNOWN_DATABASE
	"202": true, // TOO_MANY_SIMULTANEOUS_QUERIES
	"209": true, // SOCKET_TIMEOUT
	"210": true, // NETWORK_ERROR
	"241": true, // MEMORY_LIMIT_EXCEEDED
}

// Config 连接参数。
type Config struct {
	URL      string // 如 http://vk8s-clickhouse.vk8s-system.svc:8123
	Database string // 默认 vk8s_audit
	Username string
	Password string
	// HTTPClient 为 nil 时用 30s 超时的默认客户端。
	HTTPClient *http.Client
}

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Sink 实现 audit.Sink 与 audit.Querier。
type Sink struct {
	base *url.URL
	db   string
	user string
	pass string
	http *http.Client
}

// New 校验 URL 与库名。
func New(cfg Config) (*Sink, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("clickhouse: invalid url %q", cfg.URL)
	}
	db := cfg.Database
	if db == "" {
		db = "vk8s_audit"
	}
	if !identRe.MatchString(db) {
		return nil, fmt.Errorf("clickhouse: invalid database name %q", db)
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Sink{base: u, db: db, user: cfg.Username, pass: cfg.Password, http: hc}, nil
}

// do 发一个请求：settings 进 URL 查询串，body 为请求体（SQL 或数据行）。非 2xx：5xx / 网络错误包装 ErrUnavailable，4xx 原样报错。
func (s *Sink) do(ctx context.Context, settings url.Values, body io.Reader, gz bool) ([]byte, error) {
	u := *s.base
	u.Path = strings.TrimSuffix(u.Path, "/") + "/"
	u.RawQuery = settings.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), body)
	if err != nil {
		return nil, err
	}
	if s.user != "" {
		req.Header.Set("X-ClickHouse-User", s.user)
		req.Header.Set("X-ClickHouse-Key", s.pass)
	}
	if gz {
		req.Header.Set("Content-Encoding", "gzip")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read response: %v", ErrUnavailable, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		code := resp.Header.Get("X-ClickHouse-Exception-Code")
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || unavailableExceptionCodes[code] {
			return nil, fmt.Errorf("%w: status %d (code %s): %s", ErrUnavailable, resp.StatusCode, code, snippet(out))
		}
		return nil, fmt.Errorf("clickhouse: status %d (code %s): %s", resp.StatusCode, code, snippet(out))
	}
	return out, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// exec 执行一条不返回数据的 SQL（DDL）；settings 进 URL 查询串。
func (s *Sink) exec(ctx context.Context, sql string, settings url.Values) error {
	if settings == nil {
		settings = url.Values{}
	}
	_, err := s.do(ctx, settings, strings.NewReader(sql), false)
	return err
}

// Write 批量插入：INSERT … FORMAT JSONEachRow，SQL 在 URL 里，gzip 的数据行在体里。
func (s *Sink) Write(ctx context.Context, events []audit.Event) error {
	if len(events) == 0 {
		return nil
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	enc := json.NewEncoder(gz)
	for i := range events {
		if err := enc.Encode(events[i]); err != nil {
			return fmt.Errorf("clickhouse: encode event: %w", err)
		}
	}
	if err := gz.Close(); err != nil {
		return err
	}
	settings := url.Values{
		"query":                  {"INSERT INTO " + s.db + ".events FORMAT JSONEachRow"},
		"date_time_input_format": {"best_effort"},
	}
	_, err := s.do(ctx, settings, &buf, true)
	return err
}

// Ping SELECT 1。
func (s *Sink) Ping(ctx context.Context) error {
	return s.exec(ctx, "SELECT 1", nil)
}

// Migrate 建库、建表、设 TTL，幂等。
func (s *Sink) Migrate(ctx context.Context, retentionDays int) error {
	if retentionDays <= 0 {
		retentionDays = 365
	}
	for i, stmt := range Schema(s.db, retentionDays) {
		settings := url.Values{}
		if i == 2 {
			// MODIFY TTL 只改元数据，不排全表 MATERIALIZE TTL：新 part 与后续 merge 自然按新 TTL 生效
			settings.Set("materialize_ttl_after_modify", "0")
		}
		if err := s.exec(ctx, stmt, settings); err != nil {
			return err
		}
	}
	return nil
}

// query 执行带参数的 SELECT，返回解码后的行。
func (s *Sink) query(ctx context.Context, sql string, params map[string]string) ([]audit.Event, error) {
	settings := url.Values{
		"default_format":     {"JSONEachRow"},
		"max_execution_time": {"10"},
	}
	for k, v := range params {
		settings.Set("param_"+k, v)
	}
	out, err := s.do(ctx, settings, strings.NewReader(sql), false)
	if err != nil {
		return nil, err
	}
	var events []audit.Event
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r row
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("clickhouse: decode row: %w", err)
		}
		e, err := r.toEvent()
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, sc.Err()
}

// Query 列表查询。
func (s *Sink) Query(ctx context.Context, f audit.Filter) ([]audit.Event, error) {
	sql, params := BuildQuery(s.db, f)
	return s.query(ctx, sql, params)
}

// Get 单条详情；找不到返回 (nil, nil)。
func (s *Sink) Get(ctx context.Context, id, vcluster string) (*audit.Event, error) {
	sql, params := BuildGet(s.db, id, vcluster)
	rows, err := s.query(ctx, sql, params)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// row JSONEachRow 输出的一行；时间是字符串。
type row struct {
	TS               string            `json:"ts"`
	ReceivedAt       string            `json:"received_at"`
	Source           string            `json:"source"`
	VCluster         string            `json:"vcluster"`
	ID               string            `json:"id"`
	Stage            string            `json:"stage"`
	Level            string            `json:"level"`
	Verb             string            `json:"verb"`
	User             string            `json:"user"`
	Groups           []string          `json:"groups"`
	ImpersonatedUser string            `json:"impersonated_user"`
	SourceIPs        []string          `json:"source_ips"`
	UserAgent        string            `json:"user_agent"`
	Namespace        string            `json:"namespace"`
	APIGroup         string            `json:"api_group"`
	APIVersion       string            `json:"api_version"`
	Resource         string            `json:"resource"`
	Subresource      string            `json:"subresource"`
	Name             string            `json:"name"`
	RequestURI       string            `json:"request_uri"`
	ResponseCode     uint16            `json:"response_code"`
	ResponseReason   string            `json:"response_reason"`
	ResponseMessage  string            `json:"response_message"`
	RequestObject    string            `json:"request_object"`
	RequestTruncated uint8             `json:"request_truncated"`
	Annotations      map[string]string `json:"annotations"`
	PlatformAction   string            `json:"platform_action"`
	PlatformTarget   string            `json:"platform_target"`
}

func (r row) toEvent() (audit.Event, error) {
	ts, err := parseTS(r.TS)
	if err != nil {
		return audit.Event{}, fmt.Errorf("clickhouse: parse ts %q: %w", r.TS, err)
	}
	var recv time.Time
	if r.ReceivedAt != "" {
		if recv, err = parseTS(r.ReceivedAt); err != nil {
			return audit.Event{}, fmt.Errorf("clickhouse: parse received_at %q: %w", r.ReceivedAt, err)
		}
	}
	e := audit.Event{
		TS: ts, ReceivedAt: recv, Source: audit.Source(r.Source), VCluster: r.VCluster, ID: r.ID,
		Stage: r.Stage, Level: r.Level, Verb: r.Verb, User: r.User, Groups: r.Groups, ImpersonatedUser: r.ImpersonatedUser, SourceIPs: r.SourceIPs, UserAgent: r.UserAgent,
		Namespace: r.Namespace, APIGroup: r.APIGroup, APIVersion: r.APIVersion, Resource: r.Resource, Subresource: r.Subresource, Name: r.Name, RequestURI: r.RequestURI,
		ResponseCode: r.ResponseCode, ResponseReason: r.ResponseReason, ResponseMessage: r.ResponseMessage,
		RequestObject: r.RequestObject, RequestTruncated: r.RequestTruncated, Annotations: r.Annotations,
		PlatformAction: r.PlatformAction, PlatformTarget: r.PlatformTarget,
	}
	if e.Groups == nil {
		e.Groups = []string{}
	}
	if e.SourceIPs == nil {
		e.SourceIPs = []string{}
	}
	if e.Annotations == nil {
		e.Annotations = map[string]string{}
	}
	return e, nil
}
