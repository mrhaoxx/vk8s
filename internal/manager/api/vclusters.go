package api

import (
	"fmt"
	"net/http"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/util/retry"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/controllers"
)

// createVClusterRequest POST /api/v1/vclusters。非 admin 提交的 quota/nodePlacement/podSecurity 被忽略。
type createVClusterRequest struct {
	Name          string                     `json:"name"`
	Quota         *v1alpha1.Quota            `json:"quota,omitempty"`
	NodePlacement *v1alpha1.NodePlacement    `json:"nodePlacement,omitempty"`
	PodSecurity   *v1alpha1.PodSecurityLevel `json:"podSecurity,omitempty"`
	// Addons 预装应用：建集群前全部校验，任一不合法整个请求 400。
	Addons []addonRequest `json:"addons,omitempty"`
}

// patchVClusterRequest PATCH /api/v1/vclusters/{name}。给出的字段整体替换对应 spec 字段。
type patchVClusterRequest struct {
	Quota         *v1alpha1.Quota            `json:"quota,omitempty"`
	NodePlacement *v1alpha1.NodePlacement    `json:"nodePlacement,omitempty"`
	PodSecurity   *v1alpha1.PodSecurityLevel `json:"podSecurity,omitempty"`
}

// canView owner / member / admin。
func canView(vc *v1alpha1.VirtualCluster, me *v1alpha1.User) bool {
	return me.IsAdmin() || vc.HasAccess(me.Name)
}

// canDelete owner / admin。
func canDelete(vc *v1alpha1.VirtualCluster, me *v1alpha1.User) bool {
	return me.IsAdmin() || vc.Spec.Owner == me.Name
}

// lookupVC 按名字读集群；不存在返回 (nil, nil)。
func (s *Server) lookupVC(r *http.Request, name string) (*v1alpha1.VirtualCluster, error) {
	vc := &v1alpha1.VirtualCluster{}
	if err := s.Client.Get(r.Context(), types.NamespacedName{Name: name}, vc); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return vc, nil
}

// getVC 读取并做可见性检查；不可见一律 404，避免泄露集群名。
func (s *Server) getVC(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) (*v1alpha1.VirtualCluster, bool) {
	name := r.PathValue("name")
	vc := &v1alpha1.VirtualCluster{}
	if err := s.Client.Get(r.Context(), types.NamespacedName{Name: name}, vc); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "virtual cluster not found")
		} else {
			s.internalError(w, r, err)
		}
		return nil, false
	}
	if !canView(vc, me) {
		writeError(w, http.StatusNotFound, "virtual cluster not found")
		return nil, false
	}
	return vc, true
}

// listVClusters 我 owned + member 的；admin 看全部。
func (s *Server) listVClusters(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	list := &v1alpha1.VirtualClusterList{}
	if err := s.Client.List(r.Context(), list); err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]VClusterView, 0, len(list.Items))
	for i := range list.Items {
		vc := &list.Items[i]
		if canView(vc, me) {
			items = append(items, vclusterView(vc, me, s.Config))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	writeJSON(w, http.StatusOK, listResponse[VClusterView]{Items: items})
}

// getVCluster GET /api/v1/vclusters/{name}。
func (s *Server) getVCluster(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	vc, ok := s.getVC(w, r, me)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, vclusterView(vc, me, s.Config))
}

// createVCluster POST /api/v1/vclusters：校验按设计 5.1，默认值来自平台配置。
func (s *Server) createVCluster(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	var req createVClusterRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if a := audit.Annotate(r.Context()); a != nil {
		a.VCluster, a.Target = name, name
	}
	if err := controllers.ValidateName(s.Config, name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// owned 数量上限（只算 owned，含正在删除的以外的全部）。
	list := &v1alpha1.VirtualClusterList{}
	if err := s.Client.List(r.Context(), list); err != nil {
		s.internalError(w, r, err)
		return
	}
	owned := 0
	for i := range list.Items {
		if list.Items[i].Spec.Owner == me.Name && list.Items[i].DeletionTimestamp.IsZero() {
			owned++
		}
	}
	if max := s.Config.Limits.MaxVClustersPerUser; max > 0 && owned >= max {
		writeError(w, http.StatusForbidden, fmt.Sprintf("you already own %d virtual clusters (limit %d)", owned, max))
		return
	}

	def := s.Config.Defaults
	spec := v1alpha1.VirtualClusterSpec{
		Owner:         me.Name,
		Members:       []string{},
		Quota:         *def.Quota.DeepCopy(),
		NodePlacement: *def.NodePlacement.DeepCopy(),
		PodSecurity:   def.PodSecurity,
		ControlPlane:  *def.ControlPlane.DeepCopy(),
	}
	if me.IsAdmin() {
		if req.Quota != nil {
			spec.Quota = *req.Quota.DeepCopy()
		}
		if req.NodePlacement != nil {
			spec.NodePlacement = *req.NodePlacement.DeepCopy()
		}
		if req.PodSecurity != nil && *req.PodSecurity != "" {
			spec.PodSecurity = *req.PodSecurity
		}
	}
	if err := controllers.ValidatePodSecurity(spec.PodSecurity); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := controllers.ValidateQuota(spec.Quota, def.MaxQuota); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	templates := make([]*v1alpha1.AddonTemplate, 0, len(req.Addons))
	seen := map[string]bool{}
	for _, ar := range req.Addons {
		// inputs 可能含密码：校验之前就不记请求体
		if a := audit.Annotate(r.Context()); a != nil && len(ar.Inputs) > 0 {
			a.SkipBody = true
		}
		if seen[ar.Template] {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("应用 %q 重复", ar.Template))
			return
		}
		seen[ar.Template] = true
		t, msg, err := s.validateAddonRequest(r.Context(), ar, spec.PodSecurity)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		templates = append(templates, t)
	}

	vc := &v1alpha1.VirtualCluster{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
	if err := s.Client.Create(r.Context(), vc); err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeError(w, http.StatusConflict, fmt.Sprintf("virtual cluster %q already exists", name))
			return
		}
		s.internalError(w, r, err)
		return
	}
	for i, t := range templates {
		// 集群已建好：单个应用建失败只记日志，用户可在「应用」Tab 重装
		if err := s.Client.Create(r.Context(), newAddonInstall(vc, t, req.Addons[i].Inputs)); err != nil && !apierrors.IsAlreadyExists(err) {
			logf.FromContext(r.Context()).Error(err, "create addon install after cluster creation", "vcluster", vc.Name, "template", t.Name)
		}
	}
	writeJSON(w, http.StatusCreated, vclusterView(vc, me, s.Config))
}

// patchVCluster PATCH /api/v1/vclusters/{name}，仅 admin。
func (s *Server) patchVCluster(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	var req patchVClusterRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Quota == nil && req.NodePlacement == nil && req.PodSecurity == nil {
		writeError(w, http.StatusBadRequest, "nothing to update")
		return
	}
	if req.PodSecurity != nil {
		if err := controllers.ValidatePodSecurity(*req.PodSecurity); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Quota != nil {
		if err := controllers.ValidateQuota(*req.Quota, s.Config.Defaults.MaxQuota); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	name := r.PathValue("name")
	vc := &v1alpha1.VirtualCluster{}
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := s.Client.Get(r.Context(), types.NamespacedName{Name: name}, vc); err != nil {
			return err
		}
		if req.Quota != nil {
			vc.Spec.Quota = *req.Quota.DeepCopy()
		}
		if req.NodePlacement != nil {
			vc.Spec.NodePlacement = *req.NodePlacement.DeepCopy()
		}
		if req.PodSecurity != nil {
			vc.Spec.PodSecurity = *req.PodSecurity
		}
		return s.Client.Update(r.Context(), vc)
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "virtual cluster not found")
			return
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, vclusterView(vc, me, s.Config))
}

// deleteVCluster DELETE /api/v1/vclusters/{name}，owner 或 admin。
func (s *Server) deleteVCluster(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	vc, ok := s.getVC(w, r, me)
	if !ok {
		return
	}
	if !canDelete(vc, me) {
		writeError(w, http.StatusForbidden, "only the owner or an admin can delete a virtual cluster")
		return
	}
	if err := s.Client.Delete(r.Context(), vc); err != nil && !apierrors.IsNotFound(err) {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// addMember PUT /api/v1/vclusters/{name}/members/{user}，owner/member/admin。幂等。
func (s *Server) addMember(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	vc, ok := s.getVC(w, r, me)
	if !ok {
		return
	}
	member := r.PathValue("user")
	if errs := validation.IsDNS1123Label(member); len(errs) > 0 {
		writeError(w, http.StatusBadRequest, "invalid user name")
		return
	}
	target := &v1alpha1.User{}
	if err := s.Client.Get(r.Context(), types.NamespacedName{Name: member}, target); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, fmt.Sprintf("user %q not found", member))
			return
		}
		s.internalError(w, r, err)
		return
	}
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := s.Client.Get(r.Context(), types.NamespacedName{Name: vc.Name}, vc); err != nil {
			return err
		}
		if vc.Spec.Owner == member || isMember(vc, member) {
			return nil
		}
		vc.Spec.Members = append(vc.Spec.Members, member)
		return s.Client.Update(r.Context(), vc)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// removeMember DELETE /api/v1/vclusters/{name}/members/{user}，owner/member/admin。幂等。
func (s *Server) removeMember(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	vc, ok := s.getVC(w, r, me)
	if !ok {
		return
	}
	member := r.PathValue("user")
	if vc.Spec.Owner == member {
		writeError(w, http.StatusBadRequest, "the owner cannot be removed from members")
		return
	}
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err := s.Client.Get(r.Context(), types.NamespacedName{Name: vc.Name}, vc); err != nil {
			return err
		}
		kept := make([]string, 0, len(vc.Spec.Members))
		for _, m := range vc.Spec.Members {
			if m != member {
				kept = append(kept, m)
			}
		}
		if len(kept) == len(vc.Spec.Members) {
			return nil
		}
		vc.Spec.Members = kept
		return s.Client.Update(r.Context(), vc)
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
