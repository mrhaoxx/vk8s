package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Quota 租户配额；写入宿主 ns 的 ResourceQuota（加上控制平面自身开销）。
type Quota struct {
	CPU      resource.Quantity            `json:"cpu,omitempty"`
	Memory   resource.Quantity            `json:"memory,omitempty"`
	Pods     int32                        `json:"pods,omitempty"`
	Storage  resource.Quantity            `json:"storage,omitempty"`
	Services int32                        `json:"services,omitempty"`
	Extended map[string]resource.Quantity `json:"extended,omitempty"` // 例如 nvidia.com/gpu
}

// NodePlacement 平台侧调度约束，仅管理员可改，用户在虚拟集群内无法放宽。
type NodePlacement struct {
	NodeSelector map[string]string   `json:"nodeSelector,omitempty"`
	NodeNames    []string            `json:"nodeNames,omitempty"`
	Tolerations  []corev1.Toleration `json:"tolerations,omitempty"`
}

// IsEmpty 三个字段都为空。
func (n NodePlacement) IsEmpty() bool {
	return len(n.NodeSelector) == 0 && len(n.NodeNames) == 0 && len(n.Tolerations) == 0
}

// PodSecurityLevel 写入宿主 ns 的 pod-security.kubernetes.io/enforce 标签。
type PodSecurityLevel string

const (
	PodSecurityPrivileged PodSecurityLevel = "privileged"
	PodSecurityBaseline   PodSecurityLevel = "baseline"
	PodSecurityRestricted PodSecurityLevel = "restricted"
)

// ControlPlaneSpec 控制平面 pod 的资源与存储。
type ControlPlaneSpec struct {
	Resources        corev1.ResourceRequirements `json:"resources,omitempty"`
	StorageSize      resource.Quantity           `json:"storageSize,omitempty"`
	StorageClassName string                      `json:"storageClassName,omitempty"`
}

// VirtualClusterSpec 虚拟集群定义。
type VirtualClusterSpec struct {
	// Owner 创建后不可改。
	Owner string `json:"owner"`
	// Members 纯用户名列表；owner、members、平台 admin 在虚拟集群内均为 cluster-admin。
	Members       []string         `json:"members,omitempty"`
	Quota         Quota            `json:"quota,omitempty"`
	NodePlacement NodePlacement    `json:"nodePlacement,omitempty"`
	PodSecurity   PodSecurityLevel `json:"podSecurity,omitempty"`
	ControlPlane  ControlPlaneSpec `json:"controlPlane,omitempty"`
}

// VirtualClusterPhase 生命周期阶段。
type VirtualClusterPhase string

const (
	PhasePending      VirtualClusterPhase = "Pending"
	PhaseProvisioning VirtualClusterPhase = "Provisioning"
	PhaseRunning      VirtualClusterPhase = "Running"
	PhaseFailed       VirtualClusterPhase = "Failed"
	PhaseDeleting     VirtualClusterPhase = "Deleting"
)

// Condition 类型。
const (
	ConditionCertificatesReady = "CertificatesReady"
	ConditionControlPlaneReady = "ControlPlaneReady"
	ConditionSyncerReady       = "SyncerReady"
)

// VirtualClusterStatus 虚拟集群状态。
type VirtualClusterStatus struct {
	Phase              VirtualClusterPhase `json:"phase,omitempty"`
	Reason             string              `json:"reason,omitempty"`
	HostNamespace      string              `json:"hostNamespace,omitempty"`
	APIEndpoint        string              `json:"apiEndpoint,omitempty"`
	AppsDomain         string              `json:"appsDomain,omitempty"`
	Conditions         []metav1.Condition  `json:"conditions,omitempty"`
	ObservedGeneration int64               `json:"observedGeneration,omitempty"`
	// LeasedNodes 通过 NodeBooking 租约当前归本集群使用的节点，按 node 排序；只由预约控制器写。
	LeasedNodes []LeasedNode `json:"leasedNodes,omitempty"`
}

// LeasedNode 一条生效中的节点租约。
type LeasedNode struct {
	Node    string      `json:"node"`
	Booking string      `json:"booking"` // NodeBooking 名
	Until   metav1.Time `json:"until"`   // 租约结束时刻（= 预约 end）
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=vc
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.apiEndpoint`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// VirtualCluster 一个虚拟 Kubernetes 集群。
type VirtualCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VirtualClusterSpec   `json:"spec,omitempty"`
	Status VirtualClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// VirtualClusterList VirtualCluster 列表。
type VirtualClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualCluster `json:"items"`
}

// HasAccess user 是否为 owner 或 member。
func (vc *VirtualCluster) HasAccess(user string) bool {
	if vc.Spec.Owner == user {
		return true
	}
	for _, m := range vc.Spec.Members {
		if m == user {
			return true
		}
	}
	return false
}
