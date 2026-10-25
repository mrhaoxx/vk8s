package api

import (
	"net/http"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/manager/metrics"
)

// maxTokenDescription 描述长度上限。
const maxTokenDescription = 200

// createTokenRequest POST /api/v1/vclusters/{name}/tokens。
type createTokenRequest struct {
	Description string `json:"description"`
	// TTL 形如 "720h"，空取平台默认，上限 tokenMaxTTL。
	TTL string `json:"ttl"`
}

// createToken 给自己签 kubeconfig；owner/member/admin 均可。
func (s *Server) createToken(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	vc, ok := s.getVC(w, r, me)
	if !ok {
		return
	}
	if !vc.DeletionTimestamp.IsZero() {
		writeError(w, http.StatusConflict, "virtual cluster is being deleted")
		return
	}
	var req createTokenRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Description = strings.TrimSpace(req.Description)
	if len(req.Description) > maxTokenDescription {
		writeError(w, http.StatusBadRequest, "description too long")
		return
	}
	ttl, err := s.Tokens.ResolveTTL(req.TTL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	issued, err := s.Tokens.Issue(r.Context(), me, vc, req.Description, ttl)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	metrics.TokensIssuedTotal.Inc()
	audit.Annotate(r.Context()).Target = issued.Token.Name
	writeJSON(w, http.StatusCreated, IssuedTokenView{
		ID:          issued.Token.Name,
		Description: issued.Token.Spec.Description,
		ExpiresAt:   issued.Token.Spec.ExpiresAt,
		Kubeconfig:  string(issued.Kubeconfig),
	})
}

// listTokens 该集群下我的 token；admin 看全部。
func (s *Server) listTokens(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	vc, ok := s.getVC(w, r, me)
	if !ok {
		return
	}
	list := &v1alpha1.AccessTokenList{}
	if err := s.Client.List(r.Context(), list); err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]TokenView, 0)
	for i := range list.Items {
		t := &list.Items[i]
		if t.Spec.VCluster != vc.Name || !t.DeletionTimestamp.IsZero() {
			continue
		}
		if !me.IsAdmin() && t.Spec.User != me.Name {
			continue
		}
		items = append(items, tokenView(t))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt.Time) })
	writeJSON(w, http.StatusOK, listResponse[TokenView]{Items: items})
}

// deleteToken DELETE /api/v1/tokens/{id}：撤销自己的；admin 可撤任何。
func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	id := r.PathValue("id")
	tok := &v1alpha1.AccessToken{}
	if err := s.Client.Get(r.Context(), types.NamespacedName{Name: id}, tok); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "token not found")
			return
		}
		s.internalError(w, r, err)
		return
	}
	if a := audit.Annotate(r.Context()); a != nil {
		a.VCluster, a.Target = tok.Spec.VCluster, id
	}
	if !me.IsAdmin() && tok.Spec.User != me.Name {
		writeError(w, http.StatusForbidden, "you can only revoke your own tokens")
		return
	}
	if err := s.Tokens.Revoke(r.Context(), tok); err != nil {
		s.internalError(w, r, err)
		return
	}
	metrics.TokensRevokedTotal.Inc()
	w.WriteHeader(http.StatusNoContent)
}
