package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/api/v1alpha1"
)

const (
	// CSRFHeader / CSRFValue 变更类请求必须携带的头（简单 CSRF 防护，浏览器跨站表单无法设置自定义头）。
	CSRFHeader = "X-Requested-With"
	CSRFValue  = "vk8s"
)

// 鉴权错误。
var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrUserDisabled    = errors.New("user is disabled")
	ErrCSRF            = errors.New("missing or invalid " + CSRFHeader + " header")
)

type ctxKey struct{}

// Authenticator 校验会话 JWT 后从 informer cache 取 User；被禁用或删除立即失效。
type Authenticator struct {
	Session *Session
	Reader  client.Reader
}

// Authenticate 返回当前请求的用户。
func (a *Authenticator) Authenticate(r *http.Request) (*v1alpha1.User, error) {
	name, err := a.Session.Subject(r)
	if err != nil {
		return nil, ErrUnauthenticated
	}
	user := &v1alpha1.User{}
	if err := a.Reader.Get(r.Context(), types.NamespacedName{Name: name}, user); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, ErrUnauthenticated
		}
		return nil, err
	}
	if user.Spec.Disabled {
		return nil, ErrUserDisabled
	}
	return user, nil
}

// WithUser 把用户放进 context。
func WithUser(ctx context.Context, u *v1alpha1.User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// UserFrom 取 context 中的用户；未登录返回 nil。
func UserFrom(ctx context.Context) *v1alpha1.User {
	u, _ := ctx.Value(ctxKey{}).(*v1alpha1.User)
	return u
}

// CheckCSRF 对非安全方法要求 X-Requested-With: vk8s。
func CheckCSRF(r *http.Request) error {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return nil
	}
	if r.Header.Get(CSRFHeader) != CSRFValue {
		return ErrCSRF
	}
	return nil
}

// RemoteIP 取来源 IP：优先 X-Forwarded-For 的第一跳（manager 在公司 Ingress 之后），否则 RemoteAddr。
func RemoteIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first := strings.TrimSpace(strings.Split(xff, ",")[0]); first != "" {
			return first
		}
	}
	if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
		return xri
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
