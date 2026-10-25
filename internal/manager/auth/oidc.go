package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/config"
)

const (
	// OIDCCallbackPath 回调路径（相对 publicURL）。
	OIDCCallbackPath = "/auth/oidc/callback"
	// stateCookieName 保存 state 与 PKCE verifier 的短期签名 cookie。
	stateCookieName = "vk8s_oidc"
	stateTTL        = 10 * time.Minute
	// clientSecretKey OIDC client secret 在 Secret 中的 key。
	clientSecretKey = "clientSecret"
	// maxAutoCreateSuffix 自动建用户时名字冲突的最大后缀尝试次数。
	maxAutoCreateSuffix = 50
)

// OIDC 登录错误。
var (
	ErrOIDCStateMismatch = errors.New("oidc: state mismatch")
	ErrOIDCNoUser        = errors.New("oidc: no matching user and auto-creation is disabled")
	ErrOIDCEmailDomain   = errors.New("oidc: email domain not allowed")
)

// stateClaims state cookie 内容。
type stateClaims struct {
	jwt.RegisteredClaims
	State    string `json:"state"`
	Verifier string `json:"verifier"`
}

// OIDC 授权码 + PKCE 登录。provider 发现延迟到首次使用并带重试，避免 IdP 短暂不可达导致 manager 起不来。
type OIDC struct {
	cfg       *config.OIDC
	publicURL string
	client    client.Client
	sysNS     string
	session   *Session
	secure    bool

	mu           sync.Mutex
	provider     *gooidc.Provider
	verifier     *gooidc.IDTokenVerifier
	oauth        *oauth2.Config
	clientSecret string
}

// NewOIDC 构造 OIDC 登录器；cfg 为 nil 或未配置 issuer 时返回 nil。
func NewOIDC(cfg *config.Auth, c client.Client, sysNS string, session *Session, secure bool) *OIDC {
	if cfg == nil || !cfg.OIDC.Enabled() {
		return nil
	}
	return &OIDC{
		cfg:       cfg.OIDC,
		publicURL: strings.TrimRight(cfg.PublicURL, "/"),
		client:    c,
		sysNS:     sysNS,
		session:   session,
		secure:    secure,
	}
}

// Init 预热 provider 发现；失败只记录，首次登录时重试。
func (o *OIDC) Init(ctx context.Context) error {
	_, err := o.ensureProvider(ctx)
	return err
}

// ensureProvider 懒初始化 provider、verifier 与 oauth2 配置。
func (o *OIDC) ensureProvider(ctx context.Context) (*oauth2.Config, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.oauth != nil {
		return o.oauth, nil
	}
	if o.clientSecret == "" {
		ns, name := o.cfg.ClientSecretRef.Split()
		if ns == "" {
			ns = o.sysNS
		}
		if name != "" {
			secret := &corev1.Secret{}
			if err := o.client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, secret); err != nil {
				return nil, fmt.Errorf("oidc: read client secret %s/%s: %w", ns, name, err)
			}
			o.clientSecret = string(secret.Data[clientSecretKey])
			if o.clientSecret == "" {
				return nil, fmt.Errorf("oidc: secret %s/%s has no key %q", ns, name, clientSecretKey)
			}
		}
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	provider, err := gooidc.NewProvider(discoveryCtx, o.cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc: discover issuer %s: %w", o.cfg.Issuer, err)
	}
	o.provider = provider
	o.verifier = provider.Verifier(&gooidc.Config{ClientID: o.cfg.ClientID})
	o.oauth = &oauth2.Config{
		ClientID:     o.cfg.ClientID,
		ClientSecret: o.clientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  o.publicURL + OIDCCallbackPath,
		Scopes:       o.cfg.Scopes,
	}
	return o.oauth, nil
}

// Start 生成 state 与 PKCE verifier，写签名 cookie，重定向到 IdP。
func (o *OIDC) Start(w http.ResponseWriter, r *http.Request) error {
	oauth, err := o.ensureProvider(r.Context())
	if err != nil {
		return err
	}
	state := oauth2.GenerateVerifier() // 43 字节 base64url 随机串，作 state 足够
	verifier := oauth2.GenerateVerifier()
	now := time.Now()
	token, err := o.session.Sign(&stateClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer,
			Subject:   "oidc-state",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(stateTTL)),
		},
		State:    state,
		Verifier: verifier,
	})
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    token,
		Path:     "/auth/oidc/",
		MaxAge:   int(stateTTL.Seconds()),
		HttpOnly: true,
		Secure:   o.secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, oauth.AuthCodeURL(state, oauth2.AccessTypeOnline, oauth2.S256ChallengeOption(verifier)), http.StatusFound)
	return nil
}

// Callback 校验 state，换 token，验证 ID token，查找或自动创建 User。
func (o *OIDC) Callback(w http.ResponseWriter, r *http.Request) (*v1alpha1.User, error) {
	ctx := r.Context()
	// 不论成败都清掉 state cookie。
	http.SetCookie(w, &http.Cookie{Name: stateCookieName, Value: "", Path: "/auth/oidc/", MaxAge: -1, HttpOnly: true, Secure: o.secure, SameSite: http.SameSiteLaxMode})

	if errCode := r.URL.Query().Get("error"); errCode != "" {
		return nil, fmt.Errorf("oidc: identity provider returned %q: %s", errCode, r.URL.Query().Get("error_description"))
	}
	c, err := r.Cookie(stateCookieName)
	if err != nil || c.Value == "" {
		return nil, ErrOIDCStateMismatch
	}
	claims := &stateClaims{}
	if err := o.session.Parse(c.Value, claims); err != nil {
		return nil, ErrOIDCStateMismatch
	}
	if claims.State == "" || claims.State != r.URL.Query().Get("state") {
		return nil, ErrOIDCStateMismatch
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		return nil, errors.New("oidc: missing code")
	}

	oauth, err := o.ensureProvider(ctx)
	if err != nil {
		return nil, err
	}
	exchangeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tok, err := oauth.Exchange(exchangeCtx, code, oauth2.VerifierOption(claims.Verifier))
	if err != nil {
		return nil, fmt.Errorf("oidc: exchange code: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return nil, errors.New("oidc: token response has no id_token")
	}
	idToken, err := o.verifier.Verify(exchangeCtx, rawID)
	if err != nil {
		return nil, fmt.Errorf("oidc: verify id_token: %w", err)
	}
	var all map[string]interface{}
	if err := idToken.Claims(&all); err != nil {
		return nil, fmt.Errorf("oidc: parse claims: %w", err)
	}
	subject := idToken.Subject
	if subject == "" {
		return nil, errors.New("oidc: id_token has no subject")
	}

	// 按 subject 查 User（cache 索引）。
	list := &v1alpha1.UserList{}
	if err := o.client.List(ctx, list, client.MatchingFields{IndexUserOIDCSubject: subject}); err != nil {
		return nil, fmt.Errorf("oidc: lookup user: %w", err)
	}
	if len(list.Items) > 0 {
		u := &list.Items[0]
		if u.Spec.Disabled {
			return nil, ErrUserDisabled
		}
		return u, nil
	}
	if !o.cfg.AutoCreateUsers {
		return nil, ErrOIDCNoUser
	}
	email := stringClaim(all, o.cfg.EmailClaim)
	if !o.emailAllowed(email) {
		return nil, ErrOIDCEmailDomain
	}
	username := stringClaim(all, o.cfg.UsernameClaim)
	if username == "" {
		username = strings.SplitN(email, "@", 2)[0]
	}
	displayName := stringClaim(all, "name")
	if displayName == "" {
		displayName = username
	}
	return o.autoCreate(ctx, sanitizeUsername(username), displayName, email, subject)
}

// emailAllowed 白名单为空时放行所有域。
func (o *OIDC) emailAllowed(email string) bool {
	if len(o.cfg.AllowedEmailDomains) == 0 {
		return true
	}
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	domain := strings.ToLower(email[at+1:])
	for _, d := range o.cfg.AllowedEmailDomains {
		if strings.EqualFold(strings.TrimPrefix(d, "@"), domain) {
			return true
		}
	}
	return false
}

// autoCreate 创建 User，名字冲突时追加数字后缀。
func (o *OIDC) autoCreate(ctx context.Context, base, displayName, email, subject string) (*v1alpha1.User, error) {
	if base == "" {
		base = "user"
	}
	for i := 0; i <= maxAutoCreateSuffix; i++ {
		name := base
		if i > 0 {
			suffix := fmt.Sprintf("-%d", i+1)
			if len(base)+len(suffix) > validation.DNS1123LabelMaxLength {
				name = base[:validation.DNS1123LabelMaxLength-len(suffix)]
			}
			name = strings.TrimRight(name, "-") + suffix
		}
		u := &v1alpha1.User{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: v1alpha1.UserSpec{
				DisplayName: displayName,
				Email:       email,
				Role:        v1alpha1.UserRoleUser,
				Auth:        v1alpha1.UserAuth{OIDC: &v1alpha1.OIDCAuth{Subject: subject}},
			},
		}
		err := o.client.Create(ctx, u)
		if err == nil {
			return u, nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("oidc: create user: %w", err)
		}
	}
	return nil, fmt.Errorf("oidc: could not find a free username for %q", base)
}

var invalidLabelChars = regexp.MustCompile(`[^a-z0-9-]+`)

// sanitizeUsername 把 IdP 用户名整理成 DNS label：小写、非法字符转 "-"、去首尾 "-"、截断到 63。
func sanitizeUsername(s string) string {
	s = strings.ToLower(s)
	if at := strings.Index(s, "@"); at > 0 {
		s = s[:at]
	}
	s = invalidLabelChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > validation.DNS1123LabelMaxLength {
		s = strings.TrimRight(s[:validation.DNS1123LabelMaxLength], "-")
	}
	return s
}

func stringClaim(claims map[string]interface{}, key string) string {
	if key == "" {
		return ""
	}
	v, _ := claims[key].(string)
	return strings.TrimSpace(v)
}
