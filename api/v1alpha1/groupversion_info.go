// Package v1alpha1 定义 vk8s.io/v1alpha1 的 User、VirtualCluster、AccessToken。
// +kubebuilder:object:generate=true
// +groupName=vk8s.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion 是本包对象的 group/version。
	GroupVersion = schema.GroupVersion{Group: "vk8s.io", Version: "v1alpha1"}

	// SchemeBuilder 用于把类型注册进 scheme。
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme 把本包全部类型加入 scheme。
	AddToScheme = SchemeBuilder.AddToScheme
)

func init() {
	SchemeBuilder.Register(
		&User{}, &UserList{},
		&VirtualCluster{}, &VirtualClusterList{},
		&AccessToken{}, &AccessTokenList{},
		&NodeBooking{}, &NodeBookingList{},
		&AddonTemplate{}, &AddonTemplateList{},
		&AddonInstall{}, &AddonInstallList{},
	)
}
