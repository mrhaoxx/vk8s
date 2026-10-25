package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/manager/metrics"
	"vk8s.io/vk8s/internal/manager/tokens"
	"vk8s.io/vk8s/internal/names"
)

// maxTokenReviewBody TokenReview 请求体上限。
const maxTokenReviewBody = 64 << 10

// TokenReviewHandler 处理虚拟 apiserver 的 authentication webhook 回调（设计 4.3）。
// 只挂在 8443 的 mTLS 监听上；客户端证书由平台 CA 签，CN 必须等于路径中的 "vc-<name>"。
type TokenReviewHandler struct {
	Client client.Reader
	Tokens *tokens.Service
}

// Register 挂到 mux：POST /webhook/tokenreview/{vc}。
func (h *TokenReviewHandler) Register(mux *http.ServeMux) {
	mux.Handle("POST "+names.TokenReviewPathPrefix+"{vc}", h)
}

// ServeHTTP 实现 http.Handler。
func (h *TokenReviewHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	log := logf.FromContext(r.Context()).WithName("tokenreview")
	vcName := r.PathValue("vc")

	result := metrics.ResultRejected
	defer func() {
		metrics.TokenReviewDuration.WithLabelValues(result).Observe(time.Since(start).Seconds())
	}()

	// mTLS 身份与路径一致性。
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		http.Error(w, "client certificate required", http.StatusUnauthorized)
		return
	}
	cn := r.TLS.PeerCertificates[0].Subject.CommonName
	if vcName == "" || cn != names.HostNamespace(vcName) {
		log.Info("tokenreview rejected: certificate CN does not match path", "cn", cn, "vcluster", vcName)
		http.Error(w, "certificate identity does not match path", http.StatusForbidden)
		return
	}

	var review authnv1.TokenReview
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTokenReviewBody)).Decode(&review); err != nil {
		http.Error(w, "invalid TokenReview body", http.StatusBadRequest)
		return
	}

	status, err := h.review(r.Context(), vcName, review.Spec.Token, review.Spec.Audiences)
	switch {
	case err != nil:
		result = metrics.ResultError
		log.Error(err, "tokenreview failed", "vcluster", vcName)
		status = authnv1.TokenReviewStatus{Authenticated: false, Error: "internal error"}
	case status.Authenticated:
		result = metrics.ResultAuthenticated
	default:
		result = metrics.ResultUnauthenticated
	}

	resp := authnv1.TokenReview{
		TypeMeta:   metav1.TypeMeta{APIVersion: authnv1.SchemeGroupVersion.String(), Kind: "TokenReview"},
		ObjectMeta: review.ObjectMeta,
		Status:     status,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(&resp)
}

// review 按 4.3 判定：sha256 查 AccessToken → 未过期、vcluster 匹配、User 存在且未禁用 →
// owner / member / 平台 admin。返回 err 仅表示内部错误（apiserver 不可达等）。
func (h *TokenReviewHandler) review(ctx context.Context, vcName, token string, audiences []string) (authnv1.TokenReviewStatus, error) {
	deny := func(reason string) authnv1.TokenReviewStatus {
		return authnv1.TokenReviewStatus{Authenticated: false, Error: reason}
	}
	token = strings.TrimSpace(token)
	if token == "" || !strings.HasPrefix(token, names.TokenPrefix) {
		return deny("unknown token"), nil
	}
	tok, err := h.Tokens.Lookup(ctx, token)
	if err != nil {
		if errors.Is(err, tokens.ErrNotFound) {
			return deny("unknown token"), nil
		}
		return authnv1.TokenReviewStatus{}, err
	}
	if !tok.DeletionTimestamp.IsZero() {
		return deny("token revoked"), nil
	}
	if tok.Expired(metav1.Now()) {
		return deny("token expired"), nil
	}
	if tok.Spec.VCluster != vcName {
		return deny("token not valid for this cluster"), nil
	}

	user := &v1alpha1.User{}
	if err := h.Client.Get(ctx, types.NamespacedName{Name: tok.Spec.User}, user); err != nil {
		if apierrors.IsNotFound(err) {
			return deny("user not found"), nil
		}
		return authnv1.TokenReviewStatus{}, fmt.Errorf("get user: %w", err)
	}
	if user.Spec.Disabled {
		return deny("user disabled"), nil
	}

	vc := &v1alpha1.VirtualCluster{}
	if err := h.Client.Get(ctx, types.NamespacedName{Name: vcName}, vc); err != nil {
		if apierrors.IsNotFound(err) {
			return deny("cluster not found"), nil
		}
		return authnv1.TokenReviewStatus{}, fmt.Errorf("get virtualcluster: %w", err)
	}
	if !vc.DeletionTimestamp.IsZero() {
		return deny("cluster is being deleted"), nil
	}
	if !vc.HasAccess(user.Name) && !user.IsAdmin() {
		return deny("user is not a member of this cluster"), nil
	}

	return authnv1.TokenReviewStatus{
		Authenticated: true,
		User: authnv1.UserInfo{
			Username: user.Name,
			UID:      string(user.UID),
			Groups:   []string{names.GroupAdmins},
			// 审计据此对应到具体令牌（一个用户可能有多份 kubeconfig）
			Extra: map[string]authnv1.ExtraValue{names.TokenIDKey: {tok.Name}},
		},
		// token 是为该虚拟 apiserver 签的，原样接受其请求的 audiences。
		Audiences: audiences,
	}, nil
}
