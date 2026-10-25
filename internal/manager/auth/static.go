package auth

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/api/v1alpha1"
)

// 静态登录错误。对调用方只区分「凭据错误」与「被限速」，不暴露用户是否存在。
var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrRateLimited        = errors.New("too many failed attempts, try again later")
)

// Static 静态账号认证：User.spec.auth.static.passwordSecretRef → vk8s-system 的 Secret → bcrypt。
type Static struct {
	// Client 读 User（缓存）与密码 Secret（manager 的 client 对 Secret 关闭缓存，直连 apiserver）。
	Client          client.Client
	SystemNamespace string
	Limiter         *RateLimiter
}

// NewStatic 构造静态认证器。
func NewStatic(c client.Client, systemNamespace string) *Static {
	return &Static{Client: c, SystemNamespace: systemNamespace, Limiter: NewRateLimiter(0, 0)}
}

// Authenticate 校验用户名密码。remoteIP 参与限速。
func (s *Static) Authenticate(ctx context.Context, username, password, remoteIP string) (*v1alpha1.User, error) {
	if username == "" || password == "" {
		return nil, ErrInvalidCredentials
	}
	userKey, ipKey := "user:"+username, "ip:"+remoteIP
	if !s.Limiter.Allowed(userKey, ipKey) {
		return nil, ErrRateLimited
	}

	user := &v1alpha1.User{}
	if err := s.Client.Get(ctx, types.NamespacedName{Name: username}, user); err != nil {
		if apierrors.IsNotFound(err) {
			// 恒定耗时：用户不存在也做一次 bcrypt 比对。
			CheckPassword(dummyHash, password)
			s.Limiter.Fail(userKey, ipKey)
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("lookup user: %w", err)
	}
	if user.Spec.Disabled || user.Spec.Auth.Static == nil || user.Spec.Auth.Static.PasswordSecretRef.Name == "" {
		CheckPassword(dummyHash, password)
		s.Limiter.Fail(userKey, ipKey)
		return nil, ErrInvalidCredentials
	}

	secret := &corev1.Secret{}
	secretKey := types.NamespacedName{Namespace: s.SystemNamespace, Name: user.Spec.Auth.Static.PasswordSecretRef.Name}
	if err := s.Client.Get(ctx, secretKey, secret); err != nil {
		if apierrors.IsNotFound(err) {
			CheckPassword(dummyHash, password)
			s.Limiter.Fail(userKey, ipKey)
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("read password secret: %w", err)
	}
	hash := secret.Data[PasswordSecretKey]
	if len(hash) == 0 || !CheckPassword(hash, password) {
		s.Limiter.Fail(userKey, ipKey)
		return nil, ErrInvalidCredentials
	}
	s.Limiter.Reset(userKey, ipKey)
	return user, nil
}
