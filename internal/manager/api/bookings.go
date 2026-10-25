package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/names"
)

const (
	maxBookingPurpose    = 200
	defaultBookingWindow = 48 * time.Hour
	maxBookingWindow     = 31 * 24 * time.Hour
)

// createBookingRequest POST /api/v1/bookings。user 取会话用户，不接受请求体里的值。
type createBookingRequest struct {
	Node     string      `json:"node"`
	VCluster string      `json:"vcluster"`
	Start    metav1.Time `json:"start"`
	End      metav1.Time `json:"end"`
	Purpose  string      `json:"purpose"`
}

// validateBookingWindow 整点对齐（UTC）、end > start、start 不早于当前整点。
func validateBookingWindow(start, end, now time.Time) error {
	if start.IsZero() || end.IsZero() {
		return errors.New("start 与 end 必填")
	}
	if !start.Truncate(time.Hour).Equal(start) || !end.Truncate(time.Hour).Equal(end) {
		return errors.New("start 与 end 必须是整点（UTC）")
	}
	if !end.After(start) {
		return errors.New("end 必须晚于 start")
	}
	if start.Before(now.Truncate(time.Hour)) {
		return errors.New("start 不能早于当前整点")
	}
	return nil
}

// firstOverlap 与 [start, end) 相交的第一个非终态预约（existing 已是同一节点的）。
func firstOverlap(existing []v1alpha1.NodeBooking, start, end time.Time) *v1alpha1.NodeBooking {
	for i := range existing {
		b := &existing[i]
		if b.IsTerminal() || !b.DeletionTimestamp.IsZero() {
			continue
		}
		if start.Before(b.Spec.End.Time) && b.Spec.Start.Time.Before(end) {
			return b
		}
	}
	return nil
}

// canDeleteBooking 创建者 / 目标集群 owner / admin。
func canDeleteBooking(b *v1alpha1.NodeBooking, vc *v1alpha1.VirtualCluster, me *v1alpha1.User) bool {
	if me.IsAdmin() || b.Spec.User == me.Name {
		return true
	}
	return vc != nil && vc.Spec.Owner == me.Name
}

// newBookingName "nb-" + 10 位十六进制。
func newBookingName() string {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return names.BookingNamePrefix + fmt.Sprintf("%010x", time.Now().UnixNano()%(1<<40))
	}
	return names.BookingNamePrefix + hex.EncodeToString(buf)
}

// parseWindow 解析 from / to；缺省 from = 当前整点，to = from + 48h；上限 31 天。
func parseWindow(r *http.Request, now time.Time) (time.Time, time.Time, error) {
	from := now.Truncate(time.Hour)
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return from, from, fmt.Errorf("from 不是 RFC3339 时间: %v", err)
		}
		from = t
	}
	to := from.Add(defaultBookingWindow)
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return from, to, fmt.Errorf("to 不是 RFC3339 时间: %v", err)
		}
		to = t
	}
	if !to.After(from) {
		return from, to, errors.New("to 必须晚于 from")
	}
	if to.Sub(from) > maxBookingWindow {
		return from, to, errors.New("查询窗口最长 31 天")
	}
	return from, to, nil
}

// listBookings GET /api/v1/bookings?from=&to=&vcluster=：窗口内全部预约与可预约节点，所有登录用户可见。
func (s *Server) listBookings(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	now := s.now()
	from, to, err := parseWindow(r, now)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	onlyVC := r.URL.Query().Get("vcluster")

	nodes := &corev1.NodeList{}
	if err := s.Client.List(r.Context(), nodes); err != nil {
		s.internalError(w, r, err)
		return
	}
	nodeViews := make([]BookableNodeView, 0)
	for i := range nodes.Items {
		if isBookable(&nodes.Items[i]) {
			nodeViews = append(nodeViews, bookableNodeView(&nodes.Items[i]))
		}
	}
	sort.Slice(nodeViews, func(i, j int) bool { return nodeViews[i].Name < nodeViews[j].Name })

	vcs := &v1alpha1.VirtualClusterList{}
	if err := s.Client.List(r.Context(), vcs); err != nil {
		s.internalError(w, r, err)
		return
	}
	vcByName := make(map[string]*v1alpha1.VirtualCluster, len(vcs.Items))
	for i := range vcs.Items {
		vcByName[vcs.Items[i].Name] = &vcs.Items[i]
	}

	list := &v1alpha1.NodeBookingList{}
	if err := s.Client.List(r.Context(), list); err != nil {
		s.internalError(w, r, err)
		return
	}
	views := make([]BookingView, 0)
	for i := range list.Items {
		b := &list.Items[i]
		if onlyVC != "" && b.Spec.VCluster != onlyVC {
			continue
		}
		if !b.Spec.Start.Time.Before(to) || !b.Spec.End.Time.After(from) {
			continue
		}
		views = append(views, bookingView(b, canDeleteBooking(b, vcByName[b.Spec.VCluster], me)))
	}
	sort.Slice(views, func(i, j int) bool {
		if !views[i].Start.Equal(&views[j].Start) {
			return views[i].Start.Time.Before(views[j].Start.Time)
		}
		return views[i].Name < views[j].Name
	})
	writeJSON(w, http.StatusOK, BookingsResponse{Nodes: nodeViews, Bookings: views, Now: metav1.NewTime(now)})
}

// createBooking POST /api/v1/bookings。
func (s *Server) createBooking(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	var req createBookingRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Node = strings.TrimSpace(req.Node)
	req.VCluster = strings.TrimSpace(req.VCluster)
	req.Purpose = strings.TrimSpace(req.Purpose)
	if req.Node == "" || req.VCluster == "" {
		writeError(w, http.StatusBadRequest, "node 与 vcluster 必填")
		return
	}

	node := &corev1.Node{}
	if err := s.Client.Get(r.Context(), types.NamespacedName{Name: req.Node}, node); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "节点不存在")
			return
		}
		s.internalError(w, r, err)
		return
	}
	if !isBookable(node) {
		writeError(w, http.StatusBadRequest, "节点未标记为可预约")
		return
	}
	vc := &v1alpha1.VirtualCluster{}
	if err := s.Client.Get(r.Context(), types.NamespacedName{Name: req.VCluster}, vc); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "virtual cluster not found")
			return
		}
		s.internalError(w, r, err)
		return
	}
	if !canView(vc, me) {
		writeError(w, http.StatusNotFound, "virtual cluster not found")
		return
	}
	if !vc.DeletionTimestamp.IsZero() || vc.Status.Phase == v1alpha1.PhaseDeleting {
		writeError(w, http.StatusConflict, "virtual cluster is being deleted")
		return
	}
	if err := validateBookingWindow(req.Start.Time, req.End.Time, s.now()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if utf8.RuneCountInString(req.Purpose) > maxBookingPurpose {
		writeError(w, http.StatusBadRequest, "用途说明最长 200 字")
		return
	}
	list := &v1alpha1.NodeBookingList{}
	if err := s.Client.List(r.Context(), list); err != nil {
		s.internalError(w, r, err)
		return
	}
	onNode := make([]v1alpha1.NodeBooking, 0)
	for i := range list.Items {
		if list.Items[i].Spec.Node == req.Node {
			onNode = append(onNode, list.Items[i])
		}
	}
	if o := firstOverlap(onNode, req.Start.Time, req.End.Time); o != nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("与预约 %s（%s，%s → %s）时间重叠", o.Name, o.Spec.VCluster,
			o.Spec.Start.UTC().Format(time.RFC3339), o.Spec.End.UTC().Format(time.RFC3339)))
		return
	}

	b := &v1alpha1.NodeBooking{ObjectMeta: metav1.ObjectMeta{Name: newBookingName()}}
	b.Spec = v1alpha1.NodeBookingSpec{
		Node: req.Node, VCluster: vc.Name, Start: metav1.NewTime(req.Start.Time.UTC()), End: metav1.NewTime(req.End.Time.UTC()),
		User: me.Name, Purpose: req.Purpose,
	}
	if err := controllerutil.SetOwnerReference(vc, b, s.Client.Scheme()); err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.Client.Create(r.Context(), b); err != nil {
		s.internalError(w, r, err)
		return
	}
	if a := audit.Annotate(r.Context()); a != nil {
		a.VCluster, a.Target = vc.Name, b.Name
	}
	writeJSON(w, http.StatusCreated, bookingView(b, true))
}

// deleteBooking DELETE /api/v1/bookings/{name}：创建者 / 目标集群 owner / admin；看不到目标集群的人得到 404。
func (s *Server) deleteBooking(w http.ResponseWriter, r *http.Request, me *v1alpha1.User) {
	name := r.PathValue("name")
	b := &v1alpha1.NodeBooking{}
	if err := s.Client.Get(r.Context(), types.NamespacedName{Name: name}, b); err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "booking not found")
			return
		}
		s.internalError(w, r, err)
		return
	}
	audit.Annotate(r.Context()).VCluster = b.Spec.VCluster
	var vc *v1alpha1.VirtualCluster
	got := &v1alpha1.VirtualCluster{}
	switch err := s.Client.Get(r.Context(), types.NamespacedName{Name: b.Spec.VCluster}, got); {
	case err == nil:
		vc = got
	case apierrors.IsNotFound(err):
	default:
		s.internalError(w, r, err)
		return
	}
	if !me.IsAdmin() && b.Spec.User != me.Name && (vc == nil || !vc.HasAccess(me.Name)) {
		writeError(w, http.StatusNotFound, "booking not found")
		return
	}
	if !canDeleteBooking(b, vc, me) {
		writeError(w, http.StatusForbidden, "只有预约创建者、目标集群所有者或管理员可以删除")
		return
	}
	if err := s.Client.Delete(r.Context(), b); err != nil && !apierrors.IsNotFound(err) {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
