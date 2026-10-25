package controllers

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"vk8s.io/vk8s/api/v1alpha1"
)

func TestDecidePhase(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 10, 24, h, 0, 0, 0, time.UTC) }
	mk := func(name string, created, s, e int, phase v1alpha1.NodeBookingPhase) *v1alpha1.NodeBooking {
		b := &v1alpha1.NodeBooking{}
		b.Name = name
		b.CreationTimestamp = metav1.NewTime(at(created))
		b.Spec = v1alpha1.NodeBookingSpec{Node: "n1", Start: metav1.NewTime(at(s)), End: metav1.NewTime(at(e))}
		b.Status.Phase = phase
		return b
	}
	ok := bookingFacts{NodeExists: true, Bookable: true, VCExists: true}
	with := func(f bookingFacts, now int) bookingFacts { f.Now = at(now); return f }
	cases := []struct {
		name   string
		b      *v1alpha1.NodeBooking
		f      bookingFacts
		phase  v1alpha1.NodeBookingPhase
		reason string
	}{
		{"before start", mk("a", 1, 10, 12, ""), with(ok, 9), v1alpha1.NodeBookingPending, ""},
		{"within", mk("a", 1, 10, 12, "Pending"), with(ok, 10), v1alpha1.NodeBookingActive, ""},
		{"after end", mk("a", 1, 10, 12, "Active"), with(ok, 12), v1alpha1.NodeBookingCompleted, ""},
		{"manager was down past end: never activated, straight to Completed", mk("a", 1, 10, 12, "Pending"), with(ok, 13), v1alpha1.NodeBookingCompleted, ""},
		{"terminal stays", mk("a", 1, 10, 12, "Rejected"), with(ok, 11), v1alpha1.NodeBookingRejected, ""},
		{"vcluster gone", mk("a", 1, 10, 12, "Pending"), with(bookingFacts{NodeExists: true, Bookable: true}, 9), v1alpha1.NodeBookingRejected, v1alpha1.BookingReasonVClusterNotFound},
		{"label removed while pending", mk("a", 1, 10, 12, "Pending"), with(bookingFacts{NodeExists: true, VCExists: true}, 9), v1alpha1.NodeBookingRejected, v1alpha1.BookingReasonNodeNotBookable},
		{"label removed while active", mk("a", 1, 10, 12, "Active"), with(bookingFacts{NodeExists: true, VCExists: true}, 11), v1alpha1.NodeBookingRevoked, v1alpha1.BookingReasonNodeNotBookable},
		{"node gone while active", mk("a", 1, 10, 12, "Active"), with(bookingFacts{VCExists: true}, 11), v1alpha1.NodeBookingRevoked, v1alpha1.BookingReasonNodeNotFound},
		{"later-created overlapping loses even if reconciled first", mk("b", 2, 10, 12, ""), with(bookingFacts{NodeExists: true, Bookable: true, VCExists: true, Others: []v1alpha1.NodeBooking{*mk("a", 1, 11, 13, "")}}, 9), v1alpha1.NodeBookingRejected, v1alpha1.BookingReasonOverlap},
		{"earlier-created overlapping wins", mk("a", 1, 11, 13, ""), with(bookingFacts{NodeExists: true, Bookable: true, VCExists: true, Others: []v1alpha1.NodeBooking{*mk("b", 2, 10, 12, "")}}, 9), v1alpha1.NodeBookingPending, ""},
		{"adjacent is not overlap", mk("b", 2, 12, 14, ""), with(bookingFacts{NodeExists: true, Bookable: true, VCExists: true, Others: []v1alpha1.NodeBooking{*mk("a", 1, 10, 12, "")}}, 9), v1alpha1.NodeBookingPending, ""},
	}
	for _, c := range cases {
		got := decidePhase(c.b, c.f)
		if got.Phase != c.phase || got.Reason != c.reason {
			t.Errorf("%s: got %s/%s want %s/%s", c.name, got.Phase, got.Reason, c.phase, c.reason)
		}
	}
}

func TestWinsOverTieBreaksByName(t *testing.T) {
	ts := metav1.NewTime(time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC))
	a := &v1alpha1.NodeBooking{}
	a.Name = "nb-a"
	a.CreationTimestamp = ts
	b := &v1alpha1.NodeBooking{}
	b.Name = "nb-b"
	b.CreationTimestamp = ts
	if !winsOver(a, b) || winsOver(b, a) {
		t.Fatal("same timestamp: lexically smaller name must win")
	}
}

func TestNextBoundary(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 10, 24, h, 0, 0, 0, time.UTC) }
	b := &v1alpha1.NodeBooking{}
	b.Spec.Start, b.Spec.End = metav1.NewTime(at(10)), metav1.NewTime(at(12))
	if got := nextBoundary(b, v1alpha1.NodeBookingPending, at(9), time.Hour); !got.Equal(at(10)) {
		t.Errorf("pending → start, got %v", got)
	}
	if got := nextBoundary(b, v1alpha1.NodeBookingActive, at(11), time.Hour); !got.Equal(at(12)) {
		t.Errorf("active → end, got %v", got)
	}
	rel := metav1.NewTime(at(12))
	b.Status.ReleasedAt = &rel
	if got := nextBoundary(b, v1alpha1.NodeBookingCompleted, at(12), 168*time.Hour); !got.Equal(at(12).Add(168 * time.Hour)) {
		t.Errorf("terminal → releasedAt+retention, got %v", got)
	}
	b.Status.ReleasedAt = nil
	if got := nextBoundary(b, v1alpha1.NodeBookingRejected, at(12), time.Hour); !got.Equal(at(13)) {
		t.Errorf("terminal without releasedAt → now+retention, got %v", got)
	}
}
