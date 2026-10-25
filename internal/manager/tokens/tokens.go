// Package tokens 签发与查找 kubectl 用的 AccessToken，并生成对应 kubeconfig。
package tokens

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/config"
	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/pki"
)

// IndexTokenHash AccessToken 上按 hash 查找的 cache 索引名。
const IndexTokenHash = "spec.tokenHash"

// tokenBytes 随机部分长度（32 字节 → 43 字符 base64url）。
const tokenBytes = 32

// 错误。
var (
	ErrNotFound   = errors.New("token not found")
	ErrInvalidTTL = errors.New("invalid ttl")
)

// RegisterIndexes 在 cache 上建 tokenHash 索引。
func RegisterIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	return indexer.IndexField(ctx, &v1alpha1.AccessToken{}, IndexTokenHash, func(obj client.Object) []string {
		t, ok := obj.(*v1alpha1.AccessToken)
		if !ok || t.Spec.TokenHash == "" {
			return nil
		}
		return []string{t.Spec.TokenHash}
	})
}

// Service token 服务。
type Service struct {
	Client client.Client
	Config *config.Config
	// PlatformCAPEM 写进 kubeconfig 的 certificate-authority-data（所有虚拟 apiserver 都由平台 CA 签）。
	PlatformCAPEM []byte
}

// Issued 一次签发的结果；Plaintext 只在这里出现一次。
type Issued struct {
	Token      *v1alpha1.AccessToken
	Plaintext  string
	Kubeconfig []byte
}

// ResolveTTL 解析用户提交的 ttl（如 "72h"）：空取默认，上限 tokenMaxTTL。
func (s *Service) ResolveTTL(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	def := s.Config.Limits.TokenDefaultTTL.Duration
	max := s.Config.Limits.TokenMaxTTL.Duration
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalidTTL, err)
	}
	if d < time.Minute {
		return 0, fmt.Errorf("%w: must be at least 1m", ErrInvalidTTL)
	}
	if max > 0 && d > max {
		return 0, fmt.Errorf("%w: exceeds maximum %s", ErrInvalidTTL, max.String())
	}
	return d, nil
}

// Issue 生成明文 token、写 AccessToken（ownerReference → VirtualCluster）、构造 kubeconfig。
func (s *Service) Issue(ctx context.Context, user *v1alpha1.User, vc *v1alpha1.VirtualCluster, description string, ttl time.Duration) (*Issued, error) {
	if ttl <= 0 {
		ttl = s.Config.Limits.TokenDefaultTTL.Duration
	}
	plaintext, err := Generate()
	if err != nil {
		return nil, err
	}
	hash := names.TokenHash(plaintext)
	tok := &v1alpha1.AccessToken{
		ObjectMeta: metav1.ObjectMeta{
			Name:   names.AccessTokenName(hash),
			Labels: map[string]string{names.LabelVCluster: vc.Name, names.LabelOwner: user.Name},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion:         v1alpha1.GroupVersion.String(),
				Kind:               "VirtualCluster",
				Name:               vc.Name,
				UID:                vc.UID,
				BlockOwnerDeletion: ptr.To(false),
			}},
		},
		Spec: v1alpha1.AccessTokenSpec{
			User:        user.Name,
			VCluster:    vc.Name,
			Description: description,
			ExpiresAt:   metav1.NewTime(time.Now().Add(ttl).Truncate(time.Second)),
			TokenHash:   hash,
		},
	}
	if err := s.Client.Create(ctx, tok); err != nil {
		return nil, fmt.Errorf("create access token: %w", err)
	}
	kubeconfig, err := s.Kubeconfig(vc.Name, user.Name, plaintext)
	if err != nil {
		// 回滚，避免留下用户拿不到明文的 token。
		_ = s.Client.Delete(ctx, tok)
		return nil, err
	}
	return &Issued{Token: tok, Plaintext: plaintext, Kubeconfig: kubeconfig}, nil
}

// Kubeconfig server=https://<vc>.<apiDomain>[:apiPort]，CA=平台 CA，token=明文。
func (s *Service) Kubeconfig(vcName, userName, plaintext string) ([]byte, error) {
	return pki.Kubeconfig(pki.KubeconfigOptions{
		ClusterName: vcName,
		UserName:    userName + "@" + vcName,
		ContextName: vcName,
		Server:      s.Config.APIEndpoint(vcName),
		CAPEM:       s.PlatformCAPEM,
		Token:       plaintext,
		Namespace:   "default",
	})
}

// Lookup 按明文 token 查 AccessToken（cache 索引），不存在返回 ErrNotFound。
func (s *Service) Lookup(ctx context.Context, plaintext string) (*v1alpha1.AccessToken, error) {
	if !strings.HasPrefix(plaintext, names.TokenPrefix) {
		return nil, ErrNotFound
	}
	list := &v1alpha1.AccessTokenList{}
	if err := s.Client.List(ctx, list, client.MatchingFields{IndexTokenHash: names.TokenHash(plaintext)}); err != nil {
		return nil, fmt.Errorf("lookup token: %w", err)
	}
	if len(list.Items) == 0 {
		return nil, ErrNotFound
	}
	return &list.Items[0], nil
}

// Revoke 删除 AccessToken；不存在视为成功。
func (s *Service) Revoke(ctx context.Context, tok *v1alpha1.AccessToken) error {
	if err := s.Client.Delete(ctx, tok); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// Generate 生成 "vk8s_" + 32 字节 base64url（无填充）的明文 token。
func Generate() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return names.TokenPrefix + base64.RawURLEncoding.EncodeToString(buf), nil
}
