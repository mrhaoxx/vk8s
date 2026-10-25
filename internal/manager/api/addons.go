package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/addons"
	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/names"
)

// addonRequest 安装请求（POST /api/v1/vclusters/{name}/addons，以及建集群请求里的 addons 项）。
type addonRequest struct {
	Template string            `json:"template"`
	Inputs   map[string]string `json:"inputs,omitempty"`
}

// listAddonCatalog GET /api/v1/addons：非 deprecated 的目录项。
func (s *Server) listAddonCatalog(w http.ResponseWriter, r *http.Request, _ *v1alpha1.User) {
	list := &v1alpha1.AddonTemplateList{}
	if err := s.Client.List(r.Context(), list); err != nil {
		s.internalError(w, r, err)
		return
	}
	out := []AddonTemplateView{}
	for i := range list.Items {
		if !list.Items[i].Spec.Deprecated && addons.ValidateTemplate(&list.Items[i]) == nil {
			out = append(out, addonTemplateView(&list.Items[i]))
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) addonsEnabled(ctx context.Context) bool {
	list := &v1alpha1.AddonTemplateList{}
	if err := s.Client.List(ctx, list); err != nil {
		return false
	}
	for i := range list.Items {
		if !list.Items[i].Spec.Deprecated && addons.ValidateTemplate(&list.Items[i]) == nil {
			return true
		}
	}
	return false
}

func (s *Server) getTemplate(ctx context.Context, name string) (*v1alpha1.AddonTemplate, error) {
	t := &v1alpha1.AddonTemplate{}
	if err := s.Client.Get(ctx, client.ObjectKey{Name: name}, t); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return t, nil
}

// validateAddonRequest 模板存在、未 deprecated、档位满足、inputs 合法；返回 (模板, 给用户的错误)。
func (s *Server) validateAddonRequest(ctx context.Context, req addonRequest, level v1alpha1.PodSecurityLevel) (*v1alpha1.AddonTemplate, string, error) {
	t, err := s.getTemplate(ctx, req.Template)
	if err != nil {
		return nil, "", err
	}
	if t == nil || t.Spec.Deprecated || addons.ValidateTemplate(t) != nil {
		return nil, fmt.Sprintf("应用 %q 不在目录中", req.Template), nil
	}
	if ok, why := addons.RequiresMet(t, level); !ok {
		return nil, fmt.Sprintf("应用 %s：%s", t.Spec.DisplayName, why), nil
	}
	if err := addons.ValidateInputs(t, req.Inputs); err != nil {
		return nil, fmt.Sprintf("应用 %s：%s", t.Spec.DisplayName, err.Error()), nil
	}
	return t, "", nil
}

func newAddonInstall(vc *v1alpha1.VirtualCluster, t *v1alpha1.AddonTemplate, inputs map[string]string) *v1alpha1.AddonInstall {
	return &v1alpha1.AddonInstall{
		ObjectMeta: metav1.ObjectMeta{
			Name: names.AddonInstallName(vc.Name, t.Name),
			OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "VirtualCluster",
				Name: vc.Name, UID: vc.UID, BlockOwnerDeletion: ptr.To(true)}},
		},
		Spec: v1alpha1.AddonInstallSpec{VCluster: vc.Name, Template: t.Name, Version: t.Spec.Chart.Version, Inputs: inputs},
	}
}

func effectiveLevel(vc *v1alpha1.VirtualCluster, def v1alpha1.PodSecurityLevel) v1alpha1.PodSecurityLevel {
	if vc.Spec.PodSecurity != "" {
		return vc.Spec.PodSecurity
	}
	return def
}

// installAddon POST /api/v1/vclusters/{name}/addons
func (s *Server) installAddon(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	vc, ok := s.getVC(w, r, me)
	if !ok {
		return
	}
	if !canDelete(vc, me) {
		writeError(w, http.StatusForbidden, "只有集群所有者与平台管理员可以管理应用")
		return
	}
	var req addonRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a := audit.Annotate(r.Context())
	if a != nil {
		a.VCluster, a.Target = vc.Name, vc.Name+"/"+req.Template
		// inputs 可能含密码：校验之前就不记请求体（400 也一样）
		a.SkipBody = a.SkipBody || len(req.Inputs) > 0
	}
	t, msg, err := s.validateAddonRequest(r.Context(), req, effectiveLevel(vc, s.Config.Defaults.PodSecurity))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	in := newAddonInstall(vc, t, req.Inputs)
	if err := s.Client.Create(r.Context(), in); err != nil {
		if apierrors.IsAlreadyExists(err) {
			writeError(w, http.StatusConflict, fmt.Sprintf("应用 %s 已安装", t.Spec.DisplayName))
			return
		}
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, addonInstallView(in, t))
}

// listClusterAddons GET /api/v1/vclusters/{name}/addons（成员可看）
func (s *Server) listClusterAddons(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	vc, ok := s.getVC(w, r, me)
	if !ok {
		return
	}
	list := &v1alpha1.AddonInstallList{}
	if err := s.Client.List(r.Context(), list); err != nil {
		s.internalError(w, r, err)
		return
	}
	out := []AddonInstallView{}
	for i := range list.Items {
		in := &list.Items[i]
		if in.Spec.VCluster != vc.Name {
			continue
		}
		t, err := s.getTemplate(r.Context(), in.Spec.Template)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		out = append(out, addonInstallView(in, t))
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Template < out[b].Template })
	writeJSON(w, http.StatusOK, out)
}

// addonTarget 读集群与 AddonInstall；manage=true 时要求所有者 / 管理员。
func (s *Server) addonTarget(w http.ResponseWriter, r *http.Request, me *v1alpha1.User, manage bool) (*v1alpha1.VirtualCluster, *v1alpha1.AddonInstall, bool) {
	vc, ok := s.getVC(w, r, me)
	if !ok {
		return nil, nil, false
	}
	tpl := r.PathValue("template")
	if a := audit.Annotate(r.Context()); a != nil {
		a.VCluster, a.Target = vc.Name, vc.Name+"/"+tpl
	}
	if manage && !canDelete(vc, me) {
		writeError(w, http.StatusForbidden, "只有集群所有者与平台管理员可以管理应用")
		return nil, nil, false
	}
	in := &v1alpha1.AddonInstall{}
	if err := s.Client.Get(r.Context(), client.ObjectKey{Name: names.AddonInstallName(vc.Name, tpl)}, in); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "应用未安装")
		} else {
			s.internalError(w, r, err)
		}
		return nil, nil, false
	}
	return vc, in, true
}

// uninstallAddon DELETE /api/v1/vclusters/{name}/addons/{template}
func (s *Server) uninstallAddon(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	_, in, ok := s.addonTarget(w, r, me, true)
	if !ok {
		return
	}
	if err := s.Client.Delete(r.Context(), in); client.IgnoreNotFound(err) != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// upgradeAddon POST …/{template}/upgrade：spec.version = 目录当前版本。
func (s *Server) upgradeAddon(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	_, in, ok := s.addonTarget(w, r, me, true)
	if !ok {
		return
	}
	t, err := s.getTemplate(r.Context(), in.Spec.Template)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if t == nil {
		writeError(w, http.StatusConflict, "应用已从目录中移除，无法升级")
		return
	}
	switch {
	case in.Spec.Version != t.Spec.Chart.Version:
		in.Spec.Version = t.Spec.Chart.Version
	case in.Status.UpdateAvailable:
		// 版本没变、目录改了 values：用重试注解让控制器按新内容重装
		if in.Annotations == nil {
			in.Annotations = map[string]string{}
		}
		in.Annotations[names.AnnotationAddonRetry] = s.now().UTC().Format(time.RFC3339)
	default:
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.Client.Update(r.Context(), in); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// retryAddon POST …/{template}/retry：仅 Failed。
func (s *Server) retryAddon(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	_, in, ok := s.addonTarget(w, r, me, true)
	if !ok {
		return
	}
	if in.Status.Phase != v1alpha1.AddonFailed {
		writeError(w, http.StatusConflict, "只有失败的应用可以重试")
		return
	}
	if in.Annotations == nil {
		in.Annotations = map[string]string{}
	}
	in.Annotations[names.AnnotationAddonRetry] = s.now().UTC().Format(time.RFC3339)
	if err := s.Client.Update(r.Context(), in); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// getAddonOutputs GET …/{template}/outputs：含 secret 类的值（所有者 / 管理员）。
func (s *Server) getAddonOutputs(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	vc, in, ok := s.addonTarget(w, r, me, true)
	if !ok {
		return
	}
	out := append([]v1alpha1.AddonOutputValue{}, in.Status.Outputs...)
	t, err := s.getTemplate(r.Context(), in.Spec.Template)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if t != nil && s.VirtualClients != nil {
		vcl, err := s.VirtualClients.For(r.Context(), vc.Name)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		for _, o := range t.Spec.Outputs {
			if o.Kind != "secret" {
				continue
			}
			v, err := addons.ReadSecretOutput(r.Context(), vcl, o)
			if err != nil {
				v = ""
			}
			out = append(out, v1alpha1.AddonOutputValue{Name: o.Name, Label: o.Label, Value: v})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// getAddonLogs GET …/{template}/logs：最近一次 Job 的 Pod 日志末尾 200 行（成员可看）。
func (s *Server) getAddonLogs(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	_, in, ok := s.addonTarget(w, r, me, false)
	if !ok {
		return
	}
	if in.Status.Job == "" || s.PodLogs == nil {
		writeJSON(w, http.StatusOK, map[string]string{"log": ""})
		return
	}
	pods := &corev1.PodList{}
	if err := s.Client.List(r.Context(), pods, client.InNamespace(s.Config.SystemNamespace), client.MatchingLabels{"job-name": in.Status.Job}); err != nil {
		s.internalError(w, r, err)
		return
	}
	if len(pods.Items) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"log": ""})
		return
	}
	sort.Slice(pods.Items, func(a, b int) bool {
		return pods.Items[a].CreationTimestamp.After(pods.Items[b].CreationTimestamp.Time)
	})
	log, err := s.PodLogs(r.Context(), s.Config.SystemNamespace, pods.Items[0].Name, 200)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "读取日志失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"log": log})
}
