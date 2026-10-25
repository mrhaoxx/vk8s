package placement

import (
	"sort"

	corev1 "k8s.io/api/core/v1"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/names"
)

// Effective 一个虚拟集群当前生效的节点约束：管理员配置的静态放置，加上预约控制器写入的租约节点。
type Effective struct {
	// Static spec.nodePlacement。
	Static v1alpha1.NodePlacement
	// Leased status.leasedNodes 里的节点名，已排序去重。
	Leased []string
}

// FromVCluster 从 VirtualCluster 的 spec 与 status 组装 Effective。
func FromVCluster(vc *v1alpha1.VirtualCluster) Effective {
	e := Effective{Static: *vc.Spec.NodePlacement.DeepCopy()}
	seen := map[string]bool{}
	for _, l := range vc.Status.LeasedNodes {
		if l.Node == "" || seen[l.Node] {
			continue
		}
		seen[l.Node] = true
		e.Leased = append(e.Leased, l.Node)
	}
	sort.Strings(e.Leased)
	return e
}

// clone 深拷贝。
func (e Effective) clone() Effective {
	return Effective{Static: *e.Static.DeepCopy(), Leased: append([]string(nil), e.Leased...)}
}

// IsLeased 节点是否在租约内。
func (e Effective) IsLeased(name string) bool {
	i := sort.SearchStrings(e.Leased, name)
	return i < len(e.Leased) && e.Leased[i] == name
}

// Allows 节点是否对该集群可用：
//   - 带 vk8s.io/bookable=true 标签的节点只能通过租约使用，静态规则不参与；
//   - 其他节点按静态规则：nodeSelector 每项都匹配 label；nodeNames 非空时名字必须在列表内。
//
// tolerations 不参与过滤。
func Allows(e Effective, node *corev1.Node) bool {
	if node == nil {
		return false
	}
	if node.Labels[names.LabelBookable] == names.BookableTrue {
		return e.IsLeased(node.Name)
	}
	for k, v := range e.Static.NodeSelector {
		if node.Labels[k] != v {
			return false
		}
	}
	if len(e.Static.NodeNames) > 0 {
		found := false
		for _, n := range e.Static.NodeNames {
			if n == node.Name {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
