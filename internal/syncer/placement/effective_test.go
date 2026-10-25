package placement

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/names"
)

func node(name string, labels map[string]string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func TestAllowsBookableNodeOnlyWhenLeased(t *testing.T) {
	bookable := map[string]string{names.LabelBookable: names.BookableTrue, "pool": "a"}
	static := v1alpha1.NodePlacement{NodeSelector: map[string]string{"pool": "a"}}
	cases := []struct {
		name string
		e    Effective
		node *corev1.Node
		want bool
	}{
		{"plain node, static matches", Effective{Static: static}, node("n1", map[string]string{"pool": "a"}), true},
		{"plain node, static mismatch", Effective{Static: static}, node("n1", map[string]string{"pool": "b"}), false},
		{"bookable node, no lease, static would match", Effective{Static: static}, node("n2", bookable), false},
		{"bookable node, leased", Effective{Static: static, Leased: []string{"n2"}}, node("n2", bookable), true},
		{"bookable node, leased to another", Effective{Leased: []string{"n3"}}, node("n2", bookable), false},
		{"empty placement allows plain node", Effective{}, node("n1", nil), true},
		{"nil node", Effective{Leased: []string{"n1"}}, nil, false},
	}
	for _, c := range cases {
		if got := Allows(c.e, c.node); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestFromVClusterSortsAndDedupsLeases(t *testing.T) {
	vc := &v1alpha1.VirtualCluster{}
	vc.Spec.NodePlacement.NodeNames = []string{"x"}
	vc.Status.LeasedNodes = []v1alpha1.LeasedNode{{Node: "n9"}, {Node: "n1"}, {Node: "n9"}}
	e := FromVCluster(vc)
	if !reflect.DeepEqual(e.Leased, []string{"n1", "n9"}) {
		t.Fatalf("leased = %v", e.Leased)
	}
	if !reflect.DeepEqual(e.Static.NodeNames, []string{"x"}) {
		t.Fatalf("static = %v", e.Static)
	}
}
