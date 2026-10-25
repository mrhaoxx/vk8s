package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// UserRole 平台角色。
type UserRole string

const (
	UserRoleUser  UserRole = "user"
	UserRoleAdmin UserRole = "admin"
)

// StaticAuth 静态账号：密码 bcrypt 哈希存放在 vk8s-system 的 Secret 中，key 为 "bcrypt"。
type StaticAuth struct {
	PasswordSecretRef corev1.LocalObjectReference `json:"passwordSecretRef"`
}

// OIDCAuth OIDC 身份；issuer 来自平台配置。
type OIDCAuth struct {
	Subject string `json:"subject"`
}

// UserAuth 两种登录方式可同时存在。
type UserAuth struct {
	Static *StaticAuth `json:"static,omitempty"`
	OIDC   *OIDCAuth   `json:"oidc,omitempty"`
}

// UserSpec 用户定义。metadata.name 即登录名。
type UserSpec struct {
	DisplayName string   `json:"displayName,omitempty"`
	Email       string   `json:"email,omitempty"`
	Role        UserRole `json:"role"`
	Disabled    bool     `json:"disabled,omitempty"`
	Auth        UserAuth `json:"auth,omitempty"`
}

// UserStatus 用户状态。
type UserStatus struct {
	LastLoginTime *metav1.Time `json:"lastLoginTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.role`
// +kubebuilder:printcolumn:name="Disabled",type=boolean,JSONPath=`.spec.disabled`
// +kubebuilder:printcolumn:name="Email",type=string,JSONPath=`.spec.email`

// User 平台用户。
type User struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   UserSpec   `json:"spec,omitempty"`
	Status UserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// UserList User 列表。
type UserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []User `json:"items"`
}

// IsAdmin 是否平台管理员。
func (u *User) IsAdmin() bool { return u.Spec.Role == UserRoleAdmin }
