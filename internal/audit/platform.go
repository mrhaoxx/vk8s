package audit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// maxPlatformBody 平台事件复制请求体的上限。
const maxPlatformBody = 1 << 20

// Annotation 处理器在处理过程中补充给记录器的信息。
type Annotation struct {
	Actor    string // 操作者；登录失败时为尝试的用户名
	VCluster string // 目标集群；路由里有 {name} 的集群接口可不填
	Target   string // 操作对象名
	Reason   string // 写进 response_reason，如登录的 success / failure / ratelimited
	SkipBody bool   // 整体不记请求体（登录、设密码）
}

type annotationKey struct{}

// WithAnnotation 在 ctx 里放一个可变的 Annotation，返回新 ctx 与指针。
func WithAnnotation(ctx context.Context) (context.Context, *Annotation) {
	a := &Annotation{}
	return context.WithValue(ctx, annotationKey{}, a), a
}

// Annotate 取出 ctx 里的 Annotation；没有（记录器未包装）时返回一个可丢弃的对象，调用方不用判空。
func Annotate(ctx context.Context) *Annotation {
	if a, ok := ctx.Value(annotationKey{}).(*Annotation); ok {
		return a
	}
	return &Annotation{}
}

// Recorder 把 manager API 的变更操作记成 platform 事件。nil Recorder 的方法都是空操作。
type Recorder struct {
	pipeline *Pipeline
	now      func() time.Time
}

// NewRecorder p 为 nil 时返回 nil（审计未启用）。
func NewRecorder(p *Pipeline) *Recorder {
	if p == nil {
		return nil
	}
	return &Recorder{pipeline: p, now: time.Now}
}

// VerbFor 平台事件的动词：登录 / 登出按动作，其余按 HTTP 方法。
func VerbFor(method, action string) string {
	switch action {
	case "auth.login", "auth.oidc.login":
		return "login"
	case "auth.logout":
		return "logout"
	}
	switch method {
	case http.MethodPost:
		return "create"
	case http.MethodPut, http.MethodPatch:
		return "update"
	case http.MethodDelete:
		return "delete"
	}
	return strings.ToLower(method)
}

// skipBodyActions 无论处理器是否来得及标注都不记请求体的动作（体里就是密码）。
var skipBodyActions = map[string]bool{"auth.login": true, "user.password": true}

// stripQueryActions request_uri 不带查询串的动作（OIDC 回调的查询串里是授权码与 state）。
var stripQueryActions = map[string]bool{"auth.oidc.login": true}

// sensitiveKeys 请求体里整体替换为 "***" 的字段。
var sensitiveKeys = map[string]bool{"password": true, "token": true, "kubeconfig": true, "tokenHash": true}

// Redact 把 JSON 请求体里的敏感字段递归替换为 "***"；非 JSON 或空返回空串。
func Redact(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return ""
	}
	out, err := json.Marshal(redactValue(v))
	if err != nil {
		return ""
	}
	return string(out)
}

func redactValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if sensitiveKeys[k] {
				x[k] = "***"
			} else {
				x[k] = redactValue(val)
			}
		}
		return x
	case []any:
		for i := range x {
			x[i] = redactValue(x[i])
		}
		return x
	}
	return v
}

// statusWriter 捕获状态码。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap 让 http.ResponseController 能拿到原始 writer。
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Wrap 包装一个处理器：请求结束后，非 GET 请求（以及 OIDC 回调）生成一条 platform 事件。
// 请求体通过 TeeReader 复制一份（上限 1 MiB）供脱敏记录，处理器照常读到完整体。
func (r *Recorder) Wrap(action string, next http.Handler) http.Handler {
	if r == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ctx, ann := WithAnnotation(req.Context())
		req = req.WithContext(ctx)
		record := req.Method != http.MethodGet || action == "auth.oidc.login"
		var captured []byte
		if record && !skipBodyActions[action] && req.Body != nil && req.Body != http.NoBody {
			// 先读一份（上限 1 MiB）再原样交还：处理器提前返回、没读体时也能记录
			head, _ := io.ReadAll(io.LimitReader(req.Body, maxPlatformBody))
			captured = head
			rest := req.Body
			req.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(head), rest), Closer: rest}
		}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, req)
		if !record {
			return
		}
		r.emit(req, action, ann, sw.status, captured)
	})
}

type readCloser struct {
	io.Reader
	io.Closer
}

func (r *Recorder) emit(req *http.Request, action string, ann *Annotation, status int, body []byte) {
	if status == 0 {
		status = http.StatusOK
	}
	vcluster := ann.VCluster
	if vcluster == "" && strings.Contains(req.Pattern, "/api/v1/vclusters/{name}") {
		vcluster = req.PathValue("name")
	}
	target := ann.Target
	if target == "" {
		for _, k := range []string{"user", "id", "name"} {
			if v := req.PathValue(k); v != "" {
				target = v
				break
			}
		}
	}
	if target == "" && (action == "auth.login" || action == "auth.logout" || action == "auth.oidc.login") {
		target = ann.Actor
	}
	requestObject := ""
	if !ann.SkipBody && !skipBodyActions[action] {
		requestObject = Redact(body)
	}
	requestURI := req.URL.RequestURI()
	if stripQueryActions[action] {
		requestURI = req.URL.Path
	}
	e := Event{
		TS: r.now(), ReceivedAt: r.now(), Source: SourcePlatform, VCluster: vcluster, ID: newID(),
		Stage: "ResponseComplete", Level: "Request", Verb: VerbFor(req.Method, action), User: ann.Actor,
		Groups: []string{}, SourceIPs: sourceIPs(req), UserAgent: req.UserAgent(),
		RequestURI: requestURI, ResponseCode: uint16(status), ResponseReason: ann.Reason,
		RequestObject: requestObject, Annotations: map[string]string{},
		PlatformAction: action, PlatformTarget: target,
	}
	if e.RequestObject != "" {
		e.RequestObject, e.RequestTruncated = Truncate(e.RequestObject)
	}
	if !r.pipeline.Enqueue(e) {
		// Enqueue 已计入 dropped{queue_full}
		return
	}
}

// sourceIPs RemoteAddr 的 IP 在前，X-Forwarded-For 各项在后（原样记录，不做信任判断）。
func sourceIPs(req *http.Request) []string {
	ips := []string{}
	if host, _, err := net.SplitHostPort(req.RemoteAddr); err == nil && host != "" {
		ips = append(ips, host)
	} else if req.RemoteAddr != "" {
		ips = append(ips, req.RemoteAddr)
	}
	for _, part := range strings.Split(req.Header.Get("X-Forwarded-For"), ",") {
		if p := strings.TrimSpace(part); p != "" {
			ips = append(ips, p)
		}
	}
	return ips
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b)
}
