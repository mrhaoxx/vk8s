package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/manager/auth"
	"vk8s.io/vk8s/internal/manager/metrics"
)

// meResponse GET /auth/me 成功响应。
type meResponse struct {
	User        meUser `json:"user"`
	OIDCEnabled bool   `json:"oidcEnabled"`
}

type meUser struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
	Role        string `json:"role"`
}

// unauthenticatedResponse 未登录时的 401 响应体：前端据 oidcEnabled 决定是否显示 SSO 按钮。
type unauthenticatedResponse struct {
	Error       string `json:"error"`
	OIDCEnabled bool   `json:"oidcEnabled"`
}

func (s *Server) oidcEnabled() bool { return s.OIDC != nil }

// handleMe GET /auth/me。
func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	me, err := s.Auth.Authenticate(r)
	if err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, auth.ErrUserDisabled) {
			writeJSON(w, http.StatusUnauthorized, unauthenticatedResponse{Error: "unauthenticated", OIDCEnabled: s.oidcEnabled()})
			return
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, meResponse{
		User: meUser{
			Name:        me.Name,
			DisplayName: me.Spec.DisplayName,
			Email:       me.Spec.Email,
			Role:        string(me.Spec.Role),
		},
		OIDCEnabled: s.oidcEnabled(),
	})
}

// loginRequest POST /auth/login 请求体。
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleLogin 静态账号登录：成功 204 + cookie。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ann := audit.Annotate(r.Context())
	ann.Actor, ann.SkipBody = strings.TrimSpace(req.Username), true // 登录体含密码，整体不记
	user, err := s.Static.Authenticate(r.Context(), strings.TrimSpace(req.Username), req.Password, auth.RemoteIP(r))
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrRateLimited):
			ann.Reason = "ratelimited"
			metrics.LoginTotal.WithLabelValues("static", "ratelimited").Inc()
			writeError(w, http.StatusTooManyRequests, err.Error())
		case errors.Is(err, auth.ErrInvalidCredentials):
			ann.Reason = "failure"
			metrics.LoginTotal.WithLabelValues("static", "failure").Inc()
			writeError(w, http.StatusUnauthorized, err.Error())
		default:
			ann.Reason = "failure"
			metrics.LoginTotal.WithLabelValues("static", "failure").Inc()
			s.internalError(w, r, err)
		}
		return
	}
	ann.Reason = "success"
	if err := s.Auth.Session.Issue(w, user.Name); err != nil {
		s.internalError(w, r, err)
		return
	}
	metrics.LoginTotal.WithLabelValues("static", "success").Inc()
	s.touchLastLogin(r, user)
	w.WriteHeader(http.StatusNoContent)
}

// handleLogout 清 cookie。会话是无状态 JWT，服务端无需记录。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if u, err := s.Auth.Authenticate(r); err == nil {
		audit.Annotate(r.Context()).Actor = u.Name
	}
	s.Auth.Session.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleOIDCStart 302 到 IdP。
func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil {
		writeError(w, http.StatusNotFound, "oidc is not enabled")
		return
	}
	if err := s.OIDC.Start(w, r); err != nil {
		logf.FromContext(r.Context()).Error(err, "oidc start failed")
		s.redirectLoginError(w, r, "sso_unavailable")
	}
}

// handleOIDCCallback 完成登录后 302 到 publicURL；失败 302 到 publicURL/login?error=<code>。
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil {
		writeError(w, http.StatusNotFound, "oidc is not enabled")
		return
	}
	user, err := s.OIDC.Callback(w, r)
	if err != nil {
		audit.Annotate(r.Context()).Reason = "failure"
		metrics.LoginTotal.WithLabelValues("oidc", "failure").Inc()
		logf.FromContext(r.Context()).Info("oidc login failed", "error", err.Error())
		code := "sso_failed"
		switch {
		case errors.Is(err, auth.ErrUserDisabled):
			code = "user_disabled"
		case errors.Is(err, auth.ErrOIDCNoUser):
			code = "no_user"
		case errors.Is(err, auth.ErrOIDCEmailDomain):
			code = "email_domain"
		case errors.Is(err, auth.ErrOIDCStateMismatch):
			code = "state_mismatch"
		}
		s.redirectLoginError(w, r, code)
		return
	}
	if err := s.Auth.Session.Issue(w, user.Name); err != nil {
		s.internalError(w, r, err)
		return
	}
	metrics.LoginTotal.WithLabelValues("oidc", "success").Inc()
	if a := audit.Annotate(r.Context()); a != nil {
		a.Actor, a.Reason = user.Name, "success"
	}
	s.touchLastLogin(r, user)
	http.Redirect(w, r, s.publicURL()+"/", http.StatusFound)
}

func (s *Server) publicURL() string {
	return strings.TrimRight(s.Config.Auth.PublicURL, "/")
}

func (s *Server) redirectLoginError(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, s.publicURL()+"/login?error="+url.QueryEscape(code), http.StatusFound)
}

// touchLastLogin 尽力更新 status.lastLoginTime，失败只记日志。
func (s *Server) touchLastLogin(r *http.Request, user *v1alpha1.User) {
	orig := user.DeepCopy()
	now := metav1.Now()
	user.Status.LastLoginTime = &now
	if err := s.Client.Status().Patch(r.Context(), user, client.MergeFrom(orig)); err != nil {
		logf.FromContext(r.Context()).V(1).Info("update lastLoginTime failed", "user", user.Name, "error", err.Error())
	}
}
