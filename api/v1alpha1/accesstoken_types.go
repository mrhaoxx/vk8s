package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AccessTokenSpec kubectl 用的长期 token。权限在 TokenReview 时按当时的 VirtualCluster 实时解析。
type AccessTokenSpec struct {
	User        string      `json:"user"`
	VCluster    string      `json:"vcluster"`
	Description string      `json:"description,omitempty"`
	ExpiresAt   metav1.Time `json:"expiresAt"`
	// TokenHash 形如 "sha256:<hex>"；明文只在创建响应中出现一次。
	TokenHash string `json:"tokenHash"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="User",type=string,JSONPath=`.spec.user`
// +kubebuilder:printcolumn:name="VCluster",type=string,JSONPath=`.spec.vcluster`
// +kubebuilder:printcolumn:name="Expires",type=string,JSONPath=`.spec.expiresAt`

// AccessToken kubectl 访问 token。
type AccessToken struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec AccessTokenSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// AccessTokenList AccessToken 列表。
type AccessTokenList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AccessToken `json:"items"`
}

// Expired 是否已过期。
func (t *AccessToken) Expired(now metav1.Time) bool {
	return !t.Spec.ExpiresAt.IsZero() && t.Spec.ExpiresAt.Before(&now)
}
