package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeBookingSpec 一次节点预约：把宿主节点 Node 在 [Start, End) 期间租给 VCluster。
// 全部字段创建后不可变（见 NodeBooking.Spec 上的校验规则）。
type NodeBookingSpec struct {
	// Node 宿主节点名；必须带 vk8s.io/bookable=true 标签。
	Node string `json:"node"`
	// VCluster 目标 VirtualCluster 名。
	VCluster string `json:"vcluster"`
	// Start 起始时刻，UTC 整点。
	Start metav1.Time `json:"start"`
	// End 结束时刻（不含），UTC 整点，必须晚于 Start。
	End metav1.Time `json:"end"`
	// User 创建者用户名，由 manager 按会话填写。
	User string `json:"user"`
	// Purpose 用途说明。
	// +kubebuilder:validation:MaxLength=200
	Purpose string `json:"purpose,omitempty"`
}

// NodeBookingPhase 预约所处阶段。
type NodeBookingPhase string

const (
	NodeBookingPending   NodeBookingPhase = "Pending"   // 尚未到 Start
	NodeBookingActive    NodeBookingPhase = "Active"    // 租约生效中
	NodeBookingCompleted NodeBookingPhase = "Completed" // 自然到期
	NodeBookingRejected  NodeBookingPhase = "Rejected"  // 未生效即被拒绝（重叠、节点不可预约、目标不存在）
	NodeBookingRevoked   NodeBookingPhase = "Revoked"   // 生效后被撤销（标签被摘、节点消失）
)

// status.reason 的取值。
const (
	BookingReasonOverlap          = "Overlap"
	BookingReasonNodeNotBookable  = "NodeNotBookable"
	BookingReasonNodeNotFound     = "NodeNotFound"
	BookingReasonVClusterNotFound = "VClusterNotFound"
)

// NodeBookingStatus 由预约控制器写。
type NodeBookingStatus struct {
	Phase   NodeBookingPhase `json:"phase,omitempty"`
	Reason  string           `json:"reason,omitempty"`
	Message string           `json:"message,omitempty"`
	// ActivatedAt 首次进入 Active 的时刻。
	ActivatedAt *metav1.Time `json:"activatedAt,omitempty"`
	// ReleasedAt 进入任一终态的时刻；保留期（booking.retention）从这里起算。
	ReleasedAt *metav1.Time `json:"releasedAt,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=nb
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.spec.node`
// +kubebuilder:printcolumn:name="VCluster",type=string,JSONPath=`.spec.vcluster`
// +kubebuilder:printcolumn:name="Start",type=string,JSONPath=`.spec.start`
// +kubebuilder:printcolumn:name="End",type=string,JSONPath=`.spec.end`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`

// NodeBooking 节点预约（cluster-scoped）。ownerReference 指向目标 VirtualCluster。
type NodeBooking struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
	Spec   NodeBookingSpec   `json:"spec"`
	Status NodeBookingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NodeBookingList NodeBooking 列表。
type NodeBookingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NodeBooking `json:"items"`
}

// IsTerminal 是否已进入终态（Completed / Rejected / Revoked）。
func (b *NodeBooking) IsTerminal() bool {
	switch b.Status.Phase {
	case NodeBookingCompleted, NodeBookingRejected, NodeBookingRevoked:
		return true
	}
	return false
}

// Overlaps 同一节点且时间区间 [start, end) 相交。
func (b *NodeBooking) Overlaps(o *NodeBooking) bool {
	if b.Spec.Node != o.Spec.Node {
		return false
	}
	return b.Spec.Start.Time.Before(o.Spec.End.Time) && o.Spec.Start.Time.Before(b.Spec.End.Time)
}
