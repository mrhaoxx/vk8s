package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNodeBookingOverlaps(t *testing.T) {
	at := func(h int) metav1.Time { return metav1.NewTime(time.Date(2026, 10, 24, h, 0, 0, 0, time.UTC)) }
	mk := func(node string, s, e int) *NodeBooking {
		return &NodeBooking{Spec: NodeBookingSpec{Node: node, Start: at(s), End: at(e)}}
	}
	cases := []struct {
		a, b *NodeBooking
		want bool
	}{
		{mk("n1", 8, 10), mk("n1", 9, 11), true},
		{mk("n1", 8, 10), mk("n1", 10, 12), false}, // 半开区间，首尾相接不算重叠
		{mk("n1", 8, 10), mk("n2", 8, 10), false},  // 不同节点
		{mk("n1", 8, 12), mk("n1", 9, 10), true},
	}
	for i, c := range cases {
		if got := c.a.Overlaps(c.b); got != c.want {
			t.Errorf("case %d: got %v want %v", i, got, c.want)
		}
		if got := c.b.Overlaps(c.a); got != c.want {
			t.Errorf("case %d reversed: got %v want %v", i, got, c.want)
		}
	}
}

func TestNodeBookingIsTerminal(t *testing.T) {
	for phase, want := range map[NodeBookingPhase]bool{"": false, NodeBookingPending: false, NodeBookingActive: false, NodeBookingCompleted: true, NodeBookingRejected: true, NodeBookingRevoked: true} {
		b := &NodeBooking{Status: NodeBookingStatus{Phase: phase}}
		if b.IsTerminal() != want {
			t.Errorf("%q: want %v", phase, want)
		}
	}
}
