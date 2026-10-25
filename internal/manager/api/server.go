// Package api 实现 manager 的管理 REST API 与登录端点（设计第 9 节）。
// 它是 CRD 之上的薄层：鉴权 + 校验 + 写 CR；状态直接读 CR，其余交给 reconciler。
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
	"vk8s.io/vk8s/internal/controllers"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/config"
	"vk8s.io/vk8s/internal/manager/auth"
	"vk8s.io/vk8s/internal/manager/tokens"
)

// maxBodyBytes 请求体上限。
const maxBodyBytes = 1 << 20

// Deps API 依赖。
type Deps struct {
	// Client controller-runtime client：User/VirtualCluster/AccessToken/Node 走缓存，Secret 直连。
	Client client.Client
	Config *config.Config
	// Auth 会话鉴权。
	Auth *auth.Authenticator
	// Static 静态账号登录。
	Static *auth.Static
	// OIDC 为 nil 表示未启用。
	OIDC   *auth.OIDC
	Tokens *tokens.Service
	// Now 可注入以便测试；为空取 time.Now。
	Now func() time.Time
	// Audit 审计查询端；nil 表示审计未启用。
	Audit audit.Querier
	// Recorder 平台操作记录器；nil 表示审计未启用（不记录）。
	Recorder *audit.Recorder
	// VirtualClients 读虚拟集群（应用 outputs 的 secret 值）。
	VirtualClients controllers.VirtualClients
	// PodLogs 读 vk8s-system 里安装 Job 的 Pod 日志（tail 行）。
	PodLogs func(ctx context.Context, namespace, pod string, tail int64) (string, error)
}

// Server REST API。
type Server struct {
	Deps
}

// New 构造 API server。
func New(d Deps) *Server {
	return &Server{Deps: d}
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// rec 把变更类路由包进审计记录器；审计未启用时原样返回。
func (s *Server) rec(action string, next http.HandlerFunc) http.HandlerFunc {
	if s.Recorder == nil {
		return next
	}
	return s.Recorder.Wrap(action, next).ServeHTTP
}

// Register 把全部路由挂到 mux（Go 1.22 方法 + 路径模式）。
func (s *Server) Register(mux *http.ServeMux) {
	// 登录相关。
	mux.HandleFunc("GET /auth/me", s.handleMe)
	mux.HandleFunc("POST /auth/login", s.rec("auth.login", s.csrf(s.handleLogin)))
	mux.HandleFunc("POST /auth/logout", s.rec("auth.logout", s.csrf(s.handleLogout)))
	mux.HandleFunc("GET /auth/oidc/start", s.handleOIDCStart)
	mux.HandleFunc("GET /auth/oidc/callback", s.rec("auth.oidc.login", s.handleOIDCCallback))
	mux.HandleFunc("/auth/", s.notFound)

	// 需登录。
	mux.HandleFunc("GET /api/v1/config", s.user(s.handleConfig))
	mux.HandleFunc("GET /api/v1/vclusters", s.user(s.listVClusters))
	mux.HandleFunc("POST /api/v1/vclusters", s.rec("vcluster.create", s.user(s.createVCluster)))
	mux.HandleFunc("GET /api/v1/vclusters/{name}", s.user(s.getVCluster))
	mux.HandleFunc("PATCH /api/v1/vclusters/{name}", s.rec("vcluster.patch", s.admin(s.patchVCluster)))
	mux.HandleFunc("DELETE /api/v1/vclusters/{name}", s.rec("vcluster.delete", s.user(s.deleteVCluster)))
	mux.HandleFunc("PUT /api/v1/vclusters/{name}/members/{user}", s.rec("member.add", s.user(s.addMember)))
	mux.HandleFunc("DELETE /api/v1/vclusters/{name}/members/{user}", s.rec("member.remove", s.user(s.removeMember)))
	mux.HandleFunc("POST /api/v1/vclusters/{name}/tokens", s.rec("token.create", s.user(s.createToken)))
	mux.HandleFunc("GET /api/v1/vclusters/{name}/tokens", s.user(s.listTokens))
	mux.HandleFunc("GET /api/v1/addons", s.user(s.listAddonCatalog))
	mux.HandleFunc("GET /api/v1/vclusters/{name}/addons", s.user(s.listClusterAddons))
	mux.HandleFunc("POST /api/v1/vclusters/{name}/addons", s.rec("addon.install", s.user(s.installAddon)))
	mux.HandleFunc("DELETE /api/v1/vclusters/{name}/addons/{template}", s.rec("addon.uninstall", s.user(s.uninstallAddon)))
	mux.HandleFunc("POST /api/v1/vclusters/{name}/addons/{template}/upgrade", s.rec("addon.upgrade", s.user(s.upgradeAddon)))
	mux.HandleFunc("POST /api/v1/vclusters/{name}/addons/{template}/retry", s.rec("addon.retry", s.user(s.retryAddon)))
	mux.HandleFunc("GET /api/v1/vclusters/{name}/addons/{template}/outputs", s.user(s.getAddonOutputs))
	mux.HandleFunc("GET /api/v1/vclusters/{name}/addons/{template}/logs", s.user(s.getAddonLogs))
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.rec("token.delete", s.user(s.deleteToken)))

	// 仅 admin。
	mux.HandleFunc("GET /api/v1/users", s.admin(s.listUsers))
	mux.HandleFunc("POST /api/v1/users", s.rec("user.create", s.admin(s.createUser)))
	mux.HandleFunc("PATCH /api/v1/users/{name}", s.rec("user.patch", s.admin(s.patchUser)))
	mux.HandleFunc("POST /api/v1/users/{name}/password", s.rec("user.password", s.admin(s.setPassword)))
	mux.HandleFunc("GET /api/v1/nodes", s.admin(s.listNodes))
	mux.HandleFunc("GET /api/v1/nodes/{name}/bookable-impact", s.admin(s.bookableImpact))
	mux.HandleFunc("PUT /api/v1/nodes/{name}/bookable", s.rec("node.bookable.set", s.admin(s.setBookable)))
	mux.HandleFunc("DELETE /api/v1/nodes/{name}/bookable", s.rec("node.bookable.unset", s.admin(s.unsetBookable)))

	// 节点预约：所有登录用户可看全部预约，为自己有权限的集群创建。
	mux.HandleFunc("GET /api/v1/bookings", s.user(s.listBookings))
	mux.HandleFunc("POST /api/v1/bookings", s.rec("booking.create", s.user(s.createBooking)))
	mux.HandleFunc("DELETE /api/v1/bookings/{name}", s.rec("booking.delete", s.user(s.deleteBooking)))

	// 审计查询：管理员全部，租户限自己的集群（handler 内校验）。
	mux.HandleFunc("GET /api/v1/audit/events", s.user(s.listAuditEvents))
	mux.HandleFunc("GET /api/v1/audit/events/{id}", s.user(s.getAuditEvent))

	mux.HandleFunc("/api/", s.notFound)
}

// handlerFunc 已登录处理函数。
type handlerFunc func(w http.ResponseWriter, r *http.Request, me *v1alpha1.User)

// csrf 变更类请求要求 X-Requested-With: vk8s。
func (s *Server) csrf(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := auth.CheckCSRF(r); err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		next(w, r)
	}
}

// user 要求有效会话 + CSRF。
func (s *Server) user(next handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := auth.CheckCSRF(r); err != nil {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		me, err := s.Auth.Authenticate(r)
		if err != nil {
			switch {
			case errors.Is(err, auth.ErrUnauthenticated), errors.Is(err, auth.ErrUserDisabled):
				writeError(w, http.StatusUnauthorized, "unauthenticated")
			default:
				s.internalError(w, r, err)
			}
			return
		}
		audit.Annotate(r.Context()).Actor = me.Name
		next(w, r.WithContext(auth.WithUser(r.Context(), me)), me)
	}
}

// admin 要求平台管理员。
func (s *Server) admin(next handlerFunc) http.HandlerFunc {
	return s.user(func(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
		if !me.IsAdmin() {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		next(w, r, me)
	})
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not found")
}

// internalError 记录日志并返回 500；k8s API 的常见错误映射为对应状态码。
func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case apierrors.IsNotFound(err):
		writeError(w, http.StatusNotFound, "not found")
	case apierrors.IsConflict(err):
		writeError(w, http.StatusConflict, "conflict, please retry")
	case apierrors.IsAlreadyExists(err):
		writeError(w, http.StatusConflict, "already exists")
	case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
		writeError(w, http.StatusBadRequest, err.Error())
	case apierrors.IsForbidden(err):
		writeError(w, http.StatusForbidden, "forbidden")
	default:
		logf.FromContext(r.Context()).Error(err, "api request failed", "method", r.Method, "path", r.URL.Path)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// errorResponse 错误响应体 {error:"..."}。
type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorResponse{Error: msg})
}

// decodeJSON 解析请求体；禁止未知字段之外的宽松处理，body 为空视为错误。
func decodeJSON(w http.ResponseWriter, r *http.Request, v interface{}) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if len(body) == 0 {
		return errors.New("empty request body")
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	return nil
}
