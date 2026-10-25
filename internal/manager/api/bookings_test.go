package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/config"
	"vk8s.io/vk8s/internal/names"
)

var apiNow = time.Date(2026, 10, 24, 9, 30, 0, 0, time.UTC)

func apiAt(h int) time.Time { return time.Date(2026, 10, 24, h, 0, 0, 0, time.UTC) }

func newAPITest(t *testing.T, objs ...client.Object) (*Server, client.Client) {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&v1alpha1.NodeBooking{}, &v1alpha1.VirtualCluster{}).
		WithObjects(objs...).Build()
	cfg := &config.Config{}
	return New(Deps{Client: c, Config: cfg, Now: func() time.Time { return apiNow }}), c
}

func apiUser(name string, admin bool) *v1alpha1.User {
	u := &v1alpha1.User{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if admin {
		u.Spec.Role = v1alpha1.UserRoleAdmin
	} else {
		u.Spec.Role = v1alpha1.UserRoleUser
	}
	return u
}

func apiNode(name string, bookable, ready bool, gpu string) *corev1.Node {
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"pool": "a"}}}
	if bookable {
		n.Labels[names.LabelBookable] = names.BookableTrue
	}
	cond := corev1.ConditionFalse
	if ready {
		cond = corev1.ConditionTrue
	}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: cond}}
	if gpu != "" {
		n.Status.Allocatable = corev1.ResourceList{"nvidia.com/gpu": resource.MustParse(gpu)}
	}
	return n
}

func apiVC(name, owner string, members ...string) *v1alpha1.VirtualCluster {
	vc := &v1alpha1.VirtualCluster{ObjectMeta: metav1.ObjectMeta{Name: name}}
	vc.Spec.Owner = owner
	vc.Spec.Members = members
	vc.Status.Phase = v1alpha1.PhaseRunning
	return vc
}

func apiBooking(name, node, vc, user string, start, end time.Time, phase v1alpha1.NodeBookingPhase) *v1alpha1.NodeBooking {
	b := &v1alpha1.NodeBooking{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: metav1.NewTime(apiAt(1))}}
	b.Spec = v1alpha1.NodeBookingSpec{Node: node, VCluster: vc, User: user, Start: metav1.NewTime(start), End: metav1.NewTime(end)}
	b.Status.Phase = phase
	return b
}

// do 直接调用 handler（绕过会话中间件），body 为 nil 时不带请求体。
func do(t *testing.T, h handlerFunc, me *v1alpha1.User, method, path string, body any, pathValues map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	rec := httptest.NewRecorder()
	h(rec, req, me)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestValidateBookingWindow(t *testing.T) {
	now := time.Date(2026, 10, 24, 9, 30, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 24, h, m, 0, 0, time.UTC) }
	if err := validateBookingWindow(at(10, 0), at(12, 0), now); err != nil {
		t.Fatal(err)
	}
	if err := validateBookingWindow(at(9, 0), at(12, 0), now); err != nil {
		t.Fatal("current hour must be allowed:", err)
	}
	for _, c := range [][2]time.Time{{at(10, 30), at(12, 0)}, {at(10, 0), at(10, 0)}, {at(12, 0), at(10, 0)}, {at(8, 0), at(12, 0)}} {
		if validateBookingWindow(c[0], c[1], now) == nil {
			t.Errorf("%v-%v should be rejected", c[0], c[1])
		}
	}
}

func bookingFixture() []client.Object {
	return []client.Object{
		apiNode("gpu-07", true, false, "8"),
		apiNode("w1", false, true, ""),
		apiVC("alice-dev", "alice", "bob", "dave"),
		apiVC("team-x", "bob"),
		apiUser("alice", false), apiUser("bob", false), apiUser("carol", false), apiUser("dave", false), apiUser("admin", true),
	}
}

func TestCreateBookingPermissionsAndValidation(t *testing.T) {
	s, _ := newAPITest(t, bookingFixture()...)
	post := func(me *v1alpha1.User, node string, start, end int) *httptest.ResponseRecorder {
		return do(t, s.createBooking, me, http.MethodPost, "/api/v1/bookings",
			createBookingRequest{Node: node, VCluster: "alice-dev", Start: metav1.NewTime(apiAt(start)), End: metav1.NewTime(apiAt(end)), Purpose: "ft"}, nil)
	}
	if rec := post(apiUser("carol", false), "gpu-07", 10, 12); rec.Code != http.StatusNotFound {
		t.Fatalf("non-member: %d %s", rec.Code, rec.Body)
	}
	rec := post(apiUser("bob", false), "gpu-07", 10, 12)
	if rec.Code != http.StatusCreated {
		t.Fatalf("member create: %d %s", rec.Code, rec.Body)
	}
	created := decodeBody[BookingView](t, rec)
	if !strings.HasPrefix(created.Name, names.BookingNamePrefix) || created.User != "bob" || created.Node != "gpu-07" || created.VCluster != "alice-dev" {
		t.Fatalf("created = %+v", created)
	}
	if rec := post(apiUser("alice", false), "w1", 10, 12); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-bookable node: %d %s", rec.Code, rec.Body)
	}
	if rec := post(apiUser("alice", false), "gpu-07", 11, 13); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), created.Name) {
		t.Fatalf("overlap: %d %s", rec.Code, rec.Body)
	}
	if rec := post(apiUser("alice", false), "gpu-07", 12, 14); rec.Code != http.StatusCreated {
		t.Fatalf("adjacent: %d %s", rec.Code, rec.Body)
	}
	if rec := post(apiUser("alice", false), "gpu-07", 8, 9); rec.Code != http.StatusBadRequest {
		t.Fatalf("past: %d %s", rec.Code, rec.Body)
	}
}

func TestDeleteBookingPermissions(t *testing.T) {
	bobs := apiBooking("nb-bob", "gpu-07", "alice-dev", "bob", apiAt(10), apiAt(12), v1alpha1.NodeBookingPending)
	s, c := newAPITest(t, append(bookingFixture(), bobs)...)
	del := func(me *v1alpha1.User) int {
		return do(t, s.deleteBooking, me, http.MethodDelete, "/api/v1/bookings/nb-bob", nil, map[string]string{"name": "nb-bob"}).Code
	}
	if code := del(apiUser("carol", false)); code != http.StatusNotFound {
		t.Fatalf("non-member delete: %d", code)
	}
	if code := del(apiUser("dave", false)); code != http.StatusForbidden {
		t.Fatalf("other member delete: %d", code)
	}
	if code := del(apiUser("alice", false)); code != http.StatusNoContent {
		t.Fatalf("owner delete: %d", code)
	}
	if err := c.Create(context.Background(), apiBooking("nb-bob", "gpu-07", "alice-dev", "bob", apiAt(10), apiAt(12), v1alpha1.NodeBookingPending)); err != nil {
		t.Fatal(err)
	}
	if code := del(apiUser("admin", true)); code != http.StatusNoContent {
		t.Fatalf("admin delete: %d", code)
	}
	if err := c.Create(context.Background(), apiBooking("nb-bob", "gpu-07", "alice-dev", "bob", apiAt(10), apiAt(12), v1alpha1.NodeBookingPending)); err != nil {
		t.Fatal(err)
	}
	if code := del(apiUser("bob", false)); code != http.StatusNoContent {
		t.Fatalf("creator delete: %d", code)
	}
}

func TestListBookingsWindowAndFilter(t *testing.T) {
	objs := append(bookingFixture(),
		apiBooking("nb-1", "gpu-07", "alice-dev", "bob", apiAt(10), apiAt(12), v1alpha1.NodeBookingPending),
		apiBooking("nb-2", "gpu-07", "team-x", "bob", apiAt(14), apiAt(16), v1alpha1.NodeBookingPending),
		apiBooking("nb-3", "gpu-07", "team-x", "bob", apiAt(9).Add(72*time.Hour), apiAt(10).Add(72*time.Hour), v1alpha1.NodeBookingPending),
	)
	s, _ := newAPITest(t, objs...)
	rec := do(t, s.listBookings, apiUser("alice", false), http.MethodGet, "/api/v1/bookings", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	resp := decodeBody[BookingsResponse](t, rec)
	if len(resp.Bookings) != 2 || len(resp.Nodes) != 1 || resp.Nodes[0].Name != "gpu-07" || resp.Nodes[0].Ready || resp.Nodes[0].GPU != "8" {
		t.Fatalf("resp = %+v", resp)
	}
	for _, b := range resp.Bookings {
		wantDel := b.VCluster == "alice-dev" // alice 是 alice-dev 的 owner，不是 team-x 的成员
		if b.CanDelete != wantDel {
			t.Errorf("%s canDelete = %v want %v", b.Name, b.CanDelete, wantDel)
		}
	}
	rec = do(t, s.listBookings, apiUser("alice", false), http.MethodGet, "/api/v1/bookings?vcluster=alice-dev", nil, nil)
	if resp := decodeBody[BookingsResponse](t, rec); len(resp.Bookings) != 1 || resp.Bookings[0].Name != "nb-1" {
		t.Fatalf("filtered = %+v", resp.Bookings)
	}
	rec = do(t, s.listBookings, apiUser("alice", false), http.MethodGet, "/api/v1/bookings?from=2026-10-24T00:00:00Z&to=2026-11-26T00:00:00Z", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("window > 31d: %d", rec.Code)
	}
}

func TestBookableImpactCountsManagedPods(t *testing.T) {
	pod := func(ns, name, node string, managed bool) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{}}}
		if managed {
			p.Labels[names.LabelManaged] = "true"
		}
		p.Spec.NodeName = node
		return p
	}
	nsFor := func(vc string) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "vc-" + vc, Labels: map[string]string{names.LabelVCluster: vc}}}
	}
	s, _ := newAPITest(t,
		apiNode("gpu-07", false, true, "8"), nsFor("a"), nsFor("b"),
		pod("vc-a", "p1", "gpu-07", true), pod("vc-a", "p2", "gpu-07", true), pod("vc-b", "p3", "gpu-07", true),
		pod("vc-a", "p4", "other", true), pod("vc-a", "p5", "gpu-07", false))
	s.Config.Defaults.NodePlacement.NodeSelector = map[string]string{"pool": "a"}
	rec := do(t, s.bookableImpact, apiUser("admin", true), http.MethodGet, "/api/v1/nodes/gpu-07/bookable-impact", nil, map[string]string{"name": "gpu-07"})
	if rec.Code != http.StatusOK {
		t.Fatalf("impact: %d %s", rec.Code, rec.Body)
	}
	v := decodeBody[BookableImpactView](t, rec)
	if v.Bookable || !v.MatchesDefaultPlacement || len(v.Leases) != 0 {
		t.Fatalf("impact = %+v", v)
	}
	if len(v.AffectedClusters) != 2 || v.AffectedClusters[0].Name != "a" || v.AffectedClusters[0].Pods != 2 || v.AffectedClusters[1].Name != "b" || v.AffectedClusters[1].Pods != 1 {
		t.Fatalf("affected = %+v", v.AffectedClusters)
	}
}

func TestSetAndUnsetBookable(t *testing.T) {
	s, c := newAPITest(t, apiNode("gpu-07", false, true, ""))
	admin := apiUser("admin", true)
	if rec := do(t, s.setBookable, admin, http.MethodPut, "/api/v1/nodes/gpu-07/bookable", nil, map[string]string{"name": "gpu-07"}); rec.Code != http.StatusNoContent {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	n := &corev1.Node{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "gpu-07"}, n); err != nil {
		t.Fatal(err)
	}
	if n.Labels[names.LabelBookable] != names.BookableTrue || n.Labels["pool"] != "a" {
		t.Fatalf("labels = %v", n.Labels)
	}
	if rec := do(t, s.unsetBookable, admin, http.MethodDelete, "/api/v1/nodes/gpu-07/bookable", nil, map[string]string{"name": "gpu-07"}); rec.Code != http.StatusNoContent {
		t.Fatalf("unset: %d %s", rec.Code, rec.Body)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: "gpu-07"}, n); err != nil {
		t.Fatal(err)
	}
	if _, ok := n.Labels[names.LabelBookable]; ok {
		t.Fatalf("label must be removed: %v", n.Labels)
	}
	if rec := do(t, s.setBookable, admin, http.MethodPut, "/api/v1/nodes/nope/bookable", nil, map[string]string{"name": "nope"}); rec.Code != http.StatusNotFound {
		t.Fatalf("missing node: %d", rec.Code)
	}
}
