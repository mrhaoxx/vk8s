package api

import (
	"sort"
	"vk8s.io/vk8s/internal/addons"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/audit"
	"vk8s.io/vk8s/internal/config"
	"vk8s.io/vk8s/internal/names"
)

// 下面的 View 类型是与前端的 JSON 契约；字段名与形状不要随意改。

// VClusterView 虚拟集群视图。
type VClusterView struct {
	Name          string            `json:"name"`
	Owner         string            `json:"owner"`
	Members       []string          `json:"members"`
	Quota         QuotaView         `json:"quota"`
	NodePlacement NodePlacementView `json:"nodePlacement"`
	PodSecurity   string            `json:"podSecurity"`
	ControlPlane  ControlPlaneView  `json:"controlPlane"`
	Status        StatusView        `json:"status"`
	CreatedAt     metav1.Time       `json:"createdAt"`
	// MyRole owner → "owner"；在 members 里 → "member"；两者都不是但为平台 admin → "admin"。
	MyRole string `json:"myRole"`
}

// QuotaView 配额，数量以字符串输出（如 "64Gi"）。
type QuotaView struct {
	CPU      string            `json:"cpu"`
	Memory   string            `json:"memory"`
	Pods     int32             `json:"pods"`
	Storage  string            `json:"storage"`
	Services int32             `json:"services"`
	Extended map[string]string `json:"extended"`
}

// NodePlacementView 节点约束。
type NodePlacementView struct {
	NodeSelector map[string]string   `json:"nodeSelector"`
	NodeNames    []string            `json:"nodeNames"`
	Tolerations  []corev1.Toleration `json:"tolerations"`
}

// ControlPlaneView 控制平面存储。
type ControlPlaneView struct {
	StorageSize      string `json:"storageSize"`
	StorageClassName string `json:"storageClassName"`
}

// StatusView 状态。
type StatusView struct {
	Phase         string           `json:"phase"`
	Reason        string           `json:"reason"`
	HostNamespace string           `json:"hostNamespace"`
	APIEndpoint   string           `json:"apiEndpoint"`
	AppsDomain    string           `json:"appsDomain"`
	Conditions    []ConditionView  `json:"conditions"`
	LeasedNodes   []LeasedNodeView `json:"leasedNodes"`
}

// LeasedNodeView 一条生效中的节点租约。
type LeasedNodeView struct {
	Node    string      `json:"node"`
	Booking string      `json:"booking"`
	Until   metav1.Time `json:"until"`
}

// BookingView 一条节点预约。
type BookingView struct {
	Name      string      `json:"name"`
	Node      string      `json:"node"`
	VCluster  string      `json:"vcluster"`
	User      string      `json:"user"`
	Purpose   string      `json:"purpose"`
	Start     metav1.Time `json:"start"`
	End       metav1.Time `json:"end"`
	Phase     string      `json:"phase"`
	Reason    string      `json:"reason"`
	CreatedAt metav1.Time `json:"createdAt"`
	// CanDelete 请求者是否可删除（创建者 / 目标集群 owner / admin）。
	CanDelete bool `json:"canDelete"`
}

// BookableNodeView 可预约节点。
type BookableNodeView struct {
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
	// GPU allocatable 的 nvidia.com/gpu，没有则为空。
	GPU string `json:"gpu"`
}

// BookingsResponse GET /api/v1/bookings。
type BookingsResponse struct {
	Nodes    []BookableNodeView `json:"nodes"`
	Bookings []BookingView      `json:"bookings"`
	Now      metav1.Time        `json:"now"`
}

// ImpactCluster 标记节点可预约时会被驱逐的集群与 Pod 数。
type ImpactCluster struct {
	Name string `json:"name"`
	Pods int    `json:"pods"`
}

// ImpactLease 摘除标签时会被撤销的租约。
type ImpactLease struct {
	Booking  string      `json:"booking"`
	VCluster string      `json:"vcluster"`
	Until    metav1.Time `json:"until"`
}

// BookableImpactView GET /api/v1/nodes/{name}/bookable-impact。
type BookableImpactView struct {
	Bookable                bool            `json:"bookable"`
	MatchesDefaultPlacement bool            `json:"matchesDefaultPlacement"`
	AffectedClusters        []ImpactCluster `json:"affectedClusters"`
	Leases                  []ImpactLease   `json:"leases"`
}

// ConditionView condition。
type ConditionView struct {
	Type               string      `json:"type"`
	Status             string      `json:"status"`
	Reason             string      `json:"reason"`
	Message            string      `json:"message"`
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`
}

// UserView 用户视图。
type UserView struct {
	Name          string       `json:"name"`
	DisplayName   string       `json:"displayName"`
	Email         string       `json:"email"`
	Role          string       `json:"role"`
	Disabled      bool         `json:"disabled"`
	HasStatic     bool         `json:"hasStatic"`
	HasOIDC       bool         `json:"hasOIDC"`
	LastLoginTime *metav1.Time `json:"lastLoginTime"`
}

// TokenView token 列表项（不含明文）。
type TokenView struct {
	ID          string      `json:"id"`
	User        string      `json:"user"`
	Description string      `json:"description"`
	ExpiresAt   metav1.Time `json:"expiresAt"`
	CreatedAt   metav1.Time `json:"createdAt"`
}

// IssuedTokenView 创建 token 的响应，kubeconfig 明文只此一次。
type IssuedTokenView struct {
	ID          string      `json:"id"`
	Description string      `json:"description"`
	ExpiresAt   metav1.Time `json:"expiresAt"`
	Kubeconfig  string      `json:"kubeconfig"`
}

// NodeView 宿主节点。
type NodeView struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	Taints      []corev1.Taint    `json:"taints"`
	Allocatable map[string]string `json:"allocatable"`
	Ready       bool              `json:"ready"`
	// Bookable 带 vk8s.io/bookable=true 标签；Leases 当前 Active 的预约数。
	Bookable bool `json:"bookable"`
	Leases   int  `json:"leases"`
}

// ConfigView GET /api/v1/config。
type ConfigView struct {
	AuditEnabled bool `json:"auditEnabled"`
	// AddonsEnabled 目录里有可装的应用时 true，界面据此显示「应用」入口。
	AddonsEnabled        bool                      `json:"addonsEnabled"`
	APIDomain            string                    `json:"apiDomain"`
	AppsDomain           string                    `json:"appsDomain"`
	NodePlacementPresets []NodePlacementPresetView `json:"nodePlacementPresets"`
	Defaults             ConfigDefaultsView        `json:"defaults"`
	Limits               ConfigLimitsView          `json:"limits"`
}

// NodePlacementPresetView 预设。
type NodePlacementPresetView struct {
	Name string `json:"name"`
	NodePlacementView
}

// ConfigDefaultsView 建集群默认值。
type ConfigDefaultsView struct {
	Quota       QuotaView `json:"quota"`
	PodSecurity string    `json:"podSecurity"`
}

// ConfigLimitsView 上限；时长用 time.Duration.String() 格式（如 "720h0m0s"）。
type ConfigLimitsView struct {
	TokenDefaultTTL     string `json:"tokenDefaultTTL"`
	TokenMaxTTL         string `json:"tokenMaxTTL"`
	MaxVClustersPerUser int    `json:"maxVClustersPerUser"`
}

// listResponse {items:[...]}。
type listResponse[T any] struct {
	Items []T `json:"items"`
}

func quotaView(q v1alpha1.Quota) QuotaView {
	ext := map[string]string{}
	for k, v := range q.Extended {
		ext[k] = v.String()
	}
	return QuotaView{
		CPU:      quantityString(q.CPU),
		Memory:   quantityString(q.Memory),
		Pods:     q.Pods,
		Storage:  quantityString(q.Storage),
		Services: q.Services,
		Extended: ext,
	}
}

func nodePlacementView(np v1alpha1.NodePlacement) NodePlacementView {
	v := NodePlacementView{
		NodeSelector: map[string]string{},
		NodeNames:    []string{},
		Tolerations:  []corev1.Toleration{},
	}
	for k, val := range np.NodeSelector {
		v.NodeSelector[k] = val
	}
	v.NodeNames = append(v.NodeNames, np.NodeNames...)
	v.Tolerations = append(v.Tolerations, np.Tolerations...)
	return v
}

func vclusterView(vc *v1alpha1.VirtualCluster, me *v1alpha1.User, cfg *config.Config) VClusterView {
	members := append([]string{}, vc.Spec.Members...)
	conds := make([]ConditionView, 0, len(vc.Status.Conditions))
	for _, c := range vc.Status.Conditions {
		conds = append(conds, ConditionView{
			Type:               c.Type,
			Status:             string(c.Status),
			Reason:             c.Reason,
			Message:            c.Message,
			LastTransitionTime: c.LastTransitionTime,
		})
	}
	podSecurity := string(vc.Spec.PodSecurity)
	if podSecurity == "" {
		podSecurity = string(cfg.Defaults.PodSecurity)
	}
	storageSize := vc.Spec.ControlPlane.StorageSize
	if storageSize.IsZero() {
		storageSize = cfg.Defaults.ControlPlane.StorageSize
	}
	storageClass := vc.Spec.ControlPlane.StorageClassName
	if storageClass == "" {
		storageClass = cfg.Defaults.ControlPlane.StorageClassName
	}
	return VClusterView{
		Name:          vc.Name,
		Owner:         vc.Spec.Owner,
		Members:       members,
		Quota:         quotaView(vc.Spec.Quota),
		NodePlacement: nodePlacementView(vc.Spec.NodePlacement),
		PodSecurity:   podSecurity,
		ControlPlane:  ControlPlaneView{StorageSize: quantityString(storageSize), StorageClassName: storageClass},
		Status: StatusView{
			Phase:         string(vc.Status.Phase),
			Reason:        vc.Status.Reason,
			HostNamespace: vc.Status.HostNamespace,
			APIEndpoint:   vc.Status.APIEndpoint,
			AppsDomain:    vc.Status.AppsDomain,
			Conditions:    conds,
			LeasedNodes:   leasedNodesView(vc.Status.LeasedNodes),
		},
		CreatedAt: vc.CreationTimestamp,
		MyRole:    myRole(vc, me),
	}
}

// myRole owner 优先于 member，member 优先于 admin。
func myRole(vc *v1alpha1.VirtualCluster, me *v1alpha1.User) string {
	switch {
	case vc.Spec.Owner == me.Name:
		return "owner"
	case isMember(vc, me.Name):
		return "member"
	case me.IsAdmin():
		return "admin"
	default:
		return ""
	}
}

func isMember(vc *v1alpha1.VirtualCluster, user string) bool {
	for _, m := range vc.Spec.Members {
		if m == user {
			return true
		}
	}
	return false
}

func userView(u *v1alpha1.User) UserView {
	return UserView{
		Name:          u.Name,
		DisplayName:   u.Spec.DisplayName,
		Email:         u.Spec.Email,
		Role:          string(u.Spec.Role),
		Disabled:      u.Spec.Disabled,
		HasStatic:     u.Spec.Auth.Static != nil && u.Spec.Auth.Static.PasswordSecretRef.Name != "",
		HasOIDC:       u.Spec.Auth.OIDC != nil && u.Spec.Auth.OIDC.Subject != "",
		LastLoginTime: u.Status.LastLoginTime,
	}
}

func tokenView(t *v1alpha1.AccessToken) TokenView {
	return TokenView{
		ID:          t.Name,
		User:        t.Spec.User,
		Description: t.Spec.Description,
		ExpiresAt:   t.Spec.ExpiresAt,
		CreatedAt:   t.CreationTimestamp,
	}
}

func nodeView(n *corev1.Node) NodeView {
	alloc := map[string]string{}
	for k, v := range n.Status.Allocatable {
		alloc[string(k)] = v.String()
	}
	ready := false
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			ready = c.Status == corev1.ConditionTrue
		}
	}
	labels := map[string]string{}
	for k, v := range n.Labels {
		labels[k] = v
	}
	taints := append([]corev1.Taint{}, n.Spec.Taints...)
	return NodeView{Name: n.Name, Labels: labels, Taints: taints, Allocatable: alloc, Ready: ready, Bookable: isBookable(n)}
}

func configView(cfg *config.Config) ConfigView {
	presets := make([]NodePlacementPresetView, 0, len(cfg.NodePlacementPresets))
	for _, p := range cfg.NodePlacementPresets {
		presets = append(presets, NodePlacementPresetView{Name: p.Name, NodePlacementView: nodePlacementView(p.NodePlacement)})
	}
	sort.Slice(presets, func(i, j int) bool { return presets[i].Name < presets[j].Name })
	return ConfigView{
		AuditEnabled:         cfg.Audit.Enabled,
		APIDomain:            cfg.APIDomain,
		AppsDomain:           cfg.AppsDomain,
		NodePlacementPresets: presets,
		Defaults: ConfigDefaultsView{
			Quota:       quotaView(cfg.Defaults.Quota),
			PodSecurity: string(cfg.Defaults.PodSecurity),
		},
		Limits: ConfigLimitsView{
			TokenDefaultTTL:     cfg.Limits.TokenDefaultTTL.Duration.String(),
			TokenMaxTTL:         cfg.Limits.TokenMaxTTL.Duration.String(),
			MaxVClustersPerUser: cfg.Limits.MaxVClustersPerUser,
		},
	}
}

// quantityString 零值输出 "0" 而不是空串，便于前端统一处理。
func quantityString(q resource.Quantity) string {
	if q.IsZero() {
		return "0"
	}
	return q.String()
}

// isBookable 节点是否带 vk8s.io/bookable=true。
func isBookable(n *corev1.Node) bool {
	return n.Labels[names.LabelBookable] == names.BookableTrue
}

func leasedNodesView(ls []v1alpha1.LeasedNode) []LeasedNodeView {
	out := make([]LeasedNodeView, 0, len(ls))
	for _, l := range ls {
		out = append(out, LeasedNodeView{Node: l.Node, Booking: l.Booking, Until: l.Until})
	}
	return out
}

func bookingView(b *v1alpha1.NodeBooking, canDelete bool) BookingView {
	return BookingView{
		Name: b.Name, Node: b.Spec.Node, VCluster: b.Spec.VCluster, User: b.Spec.User, Purpose: b.Spec.Purpose,
		Start: b.Spec.Start, End: b.Spec.End, Phase: string(b.Status.Phase), Reason: b.Status.Reason,
		CreatedAt: b.CreationTimestamp, CanDelete: canDelete,
	}
}

func bookableNodeView(n *corev1.Node) BookableNodeView {
	v := nodeView(n)
	return BookableNodeView{Name: n.Name, Ready: v.Ready, GPU: v.Allocatable["nvidia.com/gpu"]}
}

// AuditEventView 一条审计事件；列表不带 RequestObject，详情带。
type AuditEventView struct {
	ID               string            `json:"id"`
	TS               string            `json:"ts"` // RFC3339 毫秒 UTC
	Source           string            `json:"source"`
	VCluster         string            `json:"vcluster"`
	Stage            string            `json:"stage"`
	Level            string            `json:"level"`
	Verb             string            `json:"verb"`
	User             string            `json:"user"`
	Groups           []string          `json:"groups"`
	ImpersonatedUser string            `json:"impersonatedUser"`
	SourceIPs        []string          `json:"sourceIPs"`
	UserAgent        string            `json:"userAgent"`
	Namespace        string            `json:"namespace"`
	APIGroup         string            `json:"apiGroup"`
	APIVersion       string            `json:"apiVersion"`
	Resource         string            `json:"resource"`
	Subresource      string            `json:"subresource"`
	Name             string            `json:"name"`
	RequestURI       string            `json:"requestURI"`
	ResponseCode     int               `json:"responseCode"`
	ResponseReason   string            `json:"responseReason"`
	ResponseMessage  string            `json:"responseMessage"`
	PlatformAction   string            `json:"platformAction"`
	PlatformTarget   string            `json:"platformTarget"`
	RequestTruncated bool              `json:"requestTruncated"`
	Annotations      map[string]string `json:"annotations"`
	RequestObject    string            `json:"requestObject,omitempty"`
}

// AuditListView GET /api/v1/audit/events。
type AuditListView struct {
	Items      []AuditEventView `json:"items"`
	NextBefore string           `json:"nextBefore"`
}

func auditEventView(e audit.Event, withBody bool) AuditEventView {
	v := AuditEventView{
		ID: e.ID, TS: e.TS.UTC().Format("2006-01-02T15:04:05.000Z07:00"), Source: string(e.Source), VCluster: e.VCluster, Stage: e.Stage, Level: e.Level, Verb: e.Verb,
		User: e.User, Groups: e.Groups, ImpersonatedUser: e.ImpersonatedUser, SourceIPs: e.SourceIPs, UserAgent: e.UserAgent,
		Namespace: e.Namespace, APIGroup: e.APIGroup, APIVersion: e.APIVersion, Resource: e.Resource, Subresource: e.Subresource, Name: e.Name,
		RequestURI: e.RequestURI, ResponseCode: int(e.ResponseCode), ResponseReason: e.ResponseReason, ResponseMessage: e.ResponseMessage,
		PlatformAction: e.PlatformAction, PlatformTarget: e.PlatformTarget, RequestTruncated: e.RequestTruncated == 1, Annotations: e.Annotations,
	}
	if v.Groups == nil {
		v.Groups = []string{}
	}
	if v.SourceIPs == nil {
		v.SourceIPs = []string{}
	}
	if v.Annotations == nil {
		v.Annotations = map[string]string{}
	}
	if withBody {
		v.RequestObject = e.RequestObject
	}
	return v
}

// AddonInputView 目录项的参数。
type AddonInputView struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Default  string   `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"`
	Required bool     `json:"required,omitempty"`
}

// AddonTemplateView GET /api/v1/addons 的一项。
type AddonTemplateView struct {
	Name        string           `json:"name"`
	DisplayName string           `json:"displayName"`
	Description string           `json:"description"`
	Category    string           `json:"category,omitempty"`
	Version     string           `json:"version"`
	Inputs      []AddonInputView `json:"inputs"`
	Requires    struct {
		PodSecurity string `json:"podSecurity,omitempty"`
	} `json:"requires"`
}

// AddonInstallView 集群里已装的一项。
type AddonInstallView struct {
	Template         string                      `json:"template"`
	DisplayName      string                      `json:"displayName"`
	Phase            string                      `json:"phase"`
	Reason           string                      `json:"reason,omitempty"`
	Message          string                      `json:"message,omitempty"`
	InstalledVersion string                      `json:"installedVersion,omitempty"`
	AvailableVersion string                      `json:"availableVersion,omitempty"`
	UpdateAvailable  bool                        `json:"updateAvailable"`
	Inputs           map[string]string           `json:"inputs"`
	Outputs          []v1alpha1.AddonOutputValue `json:"outputs"`
	HasSecretOutputs bool                        `json:"hasSecretOutputs"`
	TemplateRemoved  bool                        `json:"templateRemoved"`
	Deleting         bool                        `json:"deleting"`
	CreatedAt        metav1.Time                 `json:"createdAt"`
}

func addonTemplateView(t *v1alpha1.AddonTemplate) AddonTemplateView {
	v := AddonTemplateView{Name: t.Name, DisplayName: t.Spec.DisplayName, Description: t.Spec.Description, Category: t.Spec.Category,
		Version: t.Spec.Chart.Version, Inputs: []AddonInputView{}}
	for _, in := range t.Spec.Inputs {
		label := in.Label
		if label == "" {
			label = in.Name
		}
		v.Inputs = append(v.Inputs, AddonInputView{Name: in.Name, Label: label, Type: in.Type, Default: in.Default, Options: in.Options, Required: in.Required})
	}
	v.Requires.PodSecurity = string(t.Spec.Requires.PodSecurity)
	return v
}

// addonInstallView t 可为 nil（目录中已移除）；password inputs 一律 ***。
func addonInstallView(in *v1alpha1.AddonInstall, t *v1alpha1.AddonTemplate) AddonInstallView {
	v := AddonInstallView{Template: in.Spec.Template, DisplayName: in.Spec.Template, Phase: string(in.Status.Phase),
		Reason: in.Status.Reason, Message: in.Status.Message, InstalledVersion: in.Status.InstalledVersion,
		AvailableVersion: in.Status.AvailableVersion, UpdateAvailable: in.Status.UpdateAvailable, Inputs: addons.MaskInputs(t, in.Spec.Inputs),
		Outputs: append([]v1alpha1.AddonOutputValue{}, in.Status.Outputs...), TemplateRemoved: t == nil,
		Deleting: !in.DeletionTimestamp.IsZero(), CreatedAt: in.CreationTimestamp}
	if v.Phase == "" {
		v.Phase = string(v1alpha1.AddonPending)
	}
	if t != nil {
		v.DisplayName = t.Spec.DisplayName
		for _, o := range t.Spec.Outputs {
			v.HasSecretOutputs = v.HasSecretOutputs || o.Kind == "secret"
		}
	} else {
		// 模板没了无法判断哪些是密码：全部打码
		for k := range v.Inputs {
			v.Inputs[k] = "***"
		}
	}
	return v
}
