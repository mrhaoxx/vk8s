package api

import (
	"net/http"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/names"
	"vk8s.io/vk8s/internal/syncer/placement"
)

// listNodes GET /api/v1/nodes：宿主节点与标签，供填 nodePlacement 与标记可预约。仅 admin。
func (s *Server) listNodes(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	list := &corev1.NodeList{}
	if err := s.Client.List(r.Context(), list); err != nil {
		s.internalError(w, r, err)
		return
	}
	bookings := &v1alpha1.NodeBookingList{}
	if err := s.Client.List(r.Context(), bookings); err != nil {
		s.internalError(w, r, err)
		return
	}
	leases := map[string]int{}
	for i := range bookings.Items {
		if bookings.Items[i].Status.Phase == v1alpha1.NodeBookingActive {
			leases[bookings.Items[i].Spec.Node]++
		}
	}
	items := make([]NodeView, 0, len(list.Items))
	for i := range list.Items {
		v := nodeView(&list.Items[i])
		v.Leases = leases[v.Name]
		items = append(items, v)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	writeJSON(w, http.StatusOK, listResponse[NodeView]{Items: items})
}

// handleConfig GET /api/v1/config：前端建集群表单所需的平台配置。
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	v := configView(s.Config)
	v.AddonsEnabled = s.addonsEnabled(r.Context())
	writeJSON(w, http.StatusOK, v)
}

// getNode 读宿主节点；不存在 → 404。
func (s *Server) getNode(w http.ResponseWriter, r *http.Request) (*corev1.Node, bool) {
	node := &corev1.Node{}
	if err := s.Client.Get(r.Context(), types.NamespacedName{Name: r.PathValue("name")}, node); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "node not found")
		} else {
			s.internalError(w, r, err)
		}
		return nil, false
	}
	return node, true
}

// bookableImpact GET /api/v1/nodes/{name}/bookable-impact：切换可预约标签前的影响。
// 未标记：将被驱逐的租户 Pod（按集群计数）与是否匹配平台默认放置；已标记：将被撤销的租约。
func (s *Server) bookableImpact(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	node, ok := s.getNode(w, r)
	if !ok {
		return
	}
	view := BookableImpactView{Bookable: isBookable(node), AffectedClusters: []ImpactCluster{}, Leases: []ImpactLease{}}

	plain := node.DeepCopy()
	delete(plain.Labels, names.LabelBookable)
	view.MatchesDefaultPlacement = placement.Allows(placement.Effective{Static: s.Config.Defaults.NodePlacement}, plain)

	if !view.Bookable {
		pods := &corev1.PodList{}
		if err := s.Client.List(r.Context(), pods, client.MatchingLabels{names.LabelManaged: "true"}); err != nil {
			s.internalError(w, r, err)
			return
		}
		byNS := map[string]int{}
		for i := range pods.Items {
			if pods.Items[i].Spec.NodeName == node.Name {
				byNS[pods.Items[i].Namespace]++
			}
		}
		byVC := map[string]int{}
		for ns, n := range byNS {
			name := ns
			nsObj := &corev1.Namespace{}
			if err := s.Client.Get(r.Context(), types.NamespacedName{Name: ns}, nsObj); err == nil {
				if v := nsObj.Labels[names.LabelVCluster]; v != "" {
					name = v
				}
			}
			byVC[name] += n
		}
		for name, n := range byVC {
			view.AffectedClusters = append(view.AffectedClusters, ImpactCluster{Name: name, Pods: n})
		}
		sort.Slice(view.AffectedClusters, func(i, j int) bool { return view.AffectedClusters[i].Name < view.AffectedClusters[j].Name })
	} else {
		bookings := &v1alpha1.NodeBookingList{}
		if err := s.Client.List(r.Context(), bookings); err != nil {
			s.internalError(w, r, err)
			return
		}
		for i := range bookings.Items {
			b := &bookings.Items[i]
			if b.Spec.Node == node.Name && b.Status.Phase == v1alpha1.NodeBookingActive {
				view.Leases = append(view.Leases, ImpactLease{Booking: b.Name, VCluster: b.Spec.VCluster, Until: b.Spec.End})
			}
		}
		sort.Slice(view.Leases, func(i, j int) bool { return view.Leases[i].Booking < view.Leases[j].Booking })
	}
	writeJSON(w, http.StatusOK, view)
}

// setBookable PUT /api/v1/nodes/{name}/bookable：打 vk8s.io/bookable=true 标签（宿主侧唯一的写）。
func (s *Server) setBookable(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	s.patchBookable(w, r, true)
}

// unsetBookable DELETE /api/v1/nodes/{name}/bookable：摘标签。
func (s *Server) unsetBookable(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	s.patchBookable(w, r, false)
}

func (s *Server) patchBookable(w http.ResponseWriter, r *http.Request, on bool) {
	node, ok := s.getNode(w, r)
	if !ok {
		return
	}
	orig := node.DeepCopy()
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	if on {
		node.Labels[names.LabelBookable] = names.BookableTrue
	} else {
		delete(node.Labels, names.LabelBookable)
	}
	if err := s.Client.Patch(r.Context(), node, client.MergeFrom(orig)); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
