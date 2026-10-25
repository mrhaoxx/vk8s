// Package auth 实现 manager 的登录与会话：静态账号（bcrypt + 限速）、OIDC（授权码 + PKCE）、
// JWT 会话 cookie，以及请求鉴权中间件与 CSRF 头校验。
package auth

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// CookieName 会话 cookie 名。
	CookieName = "vk8s_session"
	// jwtIssuer 会话 JWT 的 iss。
	jwtIssuer = "vk8s-manager"
)

// ErrInvalidSession 会话缺失、签名不符或已过期。
var ErrInvalidSession = errors.New("invalid session")

// Session 用 HS256 签发/校验会话 JWT，并负责 cookie 读写。
type Session struct {
	key    []byte
	ttl    time.Duration
	secure bool
}

// NewSession key 为 Secret vk8s-session-key 中的 32 字节随机密钥；secure 控制 cookie 的 Secure 属性。
func NewSession(key []byte, ttl time.Duration, secure bool) *Session {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Session{key: key, ttl: ttl, secure: secure}
}

// TTL 会话有效期。
func (s *Session) TTL() time.Duration { return s.ttl }

// Issue 为用户签发会话并写 cookie。
func (s *Session) Issue(w http.ResponseWriter, username string) error {
	now := time.Now()
	token, err := s.Sign(jwt.RegisteredClaims{
		Issuer:    jwtIssuer,
		Subject:   username,
		IssuedAt:  jwt.NewNumericDate(now),
		NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
		ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
	})
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(s.ttl.Seconds()),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// Clear 清除会话 cookie。
func (s *Session) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// Subject 从请求 cookie 解析出用户名。
func (s *Session) Subject(r *http.Request) (string, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return "", ErrInvalidSession
	}
	claims := &jwt.RegisteredClaims{}
	if err := s.Parse(c.Value, claims); err != nil {
		return "", ErrInvalidSession
	}
	if claims.Subject == "" {
		return "", ErrInvalidSession
	}
	return claims.Subject, nil
}

// Sign 用会话密钥签任意 claims（OIDC 的 state cookie 也复用它）。
func (s *Session) Sign(claims jwt.Claims) (string, error) {
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.key)
	if err != nil {
		return "", fmt.Errorf("sign jwt: %w", err)
	}
	return token, nil
}

// Parse 校验签名与时间并填充 claims。
func (s *Session) Parse(token string, claims jwt.Claims) error {
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
		return s.key, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(jwtIssuer),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(30*time.Second),
	)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSession, err)
	}
	return nil
}
