package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AddonChart 应用的 Helm chart。
type AddonChart struct {
	// Repository oci:// 或 https:// 仓库地址。
	Repository string `json:"repository"`
	Name       string `json:"name"`
	Version    string `json:"version"`
	// PullSecretRef vk8s-system 内 kubernetes.io/dockerconfigjson 类型的 Secret（私有仓库用）。
	PullSecretRef *corev1.LocalObjectReference `json:"pullSecretRef,omitempty"`
}

// AddonInput 允许用户填写的参数，映射到 values 的点分路径。
type AddonInput struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
	// Type string | bool | enum | password。
	Type     string   `json:"type"`
	Default  string   `json:"default,omitempty"`
	Options  []string `json:"options,omitempty"`
	Required bool     `json:"required,omitempty"`
	Path     string   `json:"path"`
}

// AddonRequires 安装前提。
type AddonRequires struct {
	// PodSecurity 集群档位至少要这么宽松（privileged < baseline < restricted）。
	PodSecurity PodSecurityLevel `json:"podSecurity,omitempty"`
}

// NamespacedName 虚拟集群里的对象位置。
type NamespacedName struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// AddonGenerate 安装前由平台在虚拟集群里生成的 Secret（已存在则跳过）。
type AddonGenerate struct {
	Secret NamespacedName `json:"secret"`
	// Kind sshKeyPair | password。
	Kind string `json:"kind"`
	// Key password 的键名，缺省 "password"。
	Key string `json:"key,omitempty"`
}

// AddonServiceRef 输出某个 Service 的地址。
type AddonServiceRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Port      int32  `json:"port"`
}

// AddonSecretRef 输出某个 Secret 的键（值只按需读取）。
type AddonSecretRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Key       string `json:"key"`
}

// AddonOutput 装好后展示的访问信息。
type AddonOutput struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
	// Kind service | secret | url。
	Kind    string           `json:"kind"`
	Service *AddonServiceRef `json:"service,omitempty"`
	Secret  *AddonSecretRef  `json:"secret,omitempty"`
	// URL Go text/template：.Cluster.Name、.Cluster.AppsDomain、.Outputs.<name>。
	URL string `json:"url,omitempty"`
}

// AddonTemplateSpec 目录项（管理员维护）。
type AddonTemplateSpec struct {
	DisplayName string `json:"displayName"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	// Deprecated 目录里隐藏，已装的不受影响。
	Deprecated bool       `json:"deprecated,omitempty"`
	Chart      AddonChart `json:"chart"`
	Namespace  string     `json:"namespace"`
	Release    string     `json:"release"`
	// Values 默认 values（YAML 文本）。
	Values   string          `json:"values,omitempty"`
	Inputs   []AddonInput    `json:"inputs,omitempty"`
	Requires AddonRequires   `json:"requires,omitempty"`
	Generate []AddonGenerate `json:"generate,omitempty"`
	Outputs  []AddonOutput   `json:"outputs,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=addontpl
// +kubebuilder:printcolumn:name="Display",type=string,JSONPath=`.spec.displayName`
// +kubebuilder:printcolumn:name="Chart",type=string,JSONPath=`.spec.chart.name`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.spec.chart.version`

// AddonTemplate 应用目录项。
type AddonTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AddonTemplateSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// AddonTemplateList 列表。
type AddonTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AddonTemplate `json:"items"`
}

// AddonPhase 安装状态。
type AddonPhase string

const (
	AddonPending      AddonPhase = "Pending"
	AddonInstalling   AddonPhase = "Installing"
	AddonReady        AddonPhase = "Ready"
	AddonFailed       AddonPhase = "Failed"
	AddonUninstalling AddonPhase = "Uninstalling"
)

// AddonInstallSpec 某集群的一次安装；名字固定为 <vcluster>-<template>。
type AddonInstallSpec struct {
	VCluster string `json:"vcluster"`
	Template string `json:"template"`
	// Version 安装时钉住的 chart 版本；升级 = 改成目录当前版本。
	Version string            `json:"version"`
	Inputs  map[string]string `json:"inputs,omitempty"`
}

// AddonOutputValue 解析后的访问信息（secret 类不在此）。
type AddonOutputValue struct {
	Name  string `json:"name"`
	Label string `json:"label,omitempty"`
	Value string `json:"value"`
}

// AddonInstallStatus 状态。
type AddonInstallStatus struct {
	Phase            AddonPhase `json:"phase,omitempty"`
	Reason           string     `json:"reason,omitempty"`
	Message          string     `json:"message,omitempty"`
	InstalledVersion string     `json:"installedVersion,omitempty"`
	AvailableVersion string     `json:"availableVersion,omitempty"`
	// Job 最近一次 Job 名。
	Job string `json:"job,omitempty"`
	// Release / Namespace 实际安装位置（模板被删后卸载用）。
	Release            string             `json:"release,omitempty"`
	Namespace          string             `json:"namespace,omitempty"`
	// InstalledSpecHash 已装内容（chart、release、namespace、合并后的 values）的摘要。
	InstalledSpecHash string `json:"installedSpecHash,omitempty"`
	// UpdateAvailable 目录项改了 values 等（版本号没变）：有更新可装，由所有者点升级。
	UpdateAvailable bool `json:"updateAvailable,omitempty"`
	Outputs            []AddonOutputValue `json:"outputs,omitempty"`
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=addon
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="VCluster",type=string,JSONPath=`.spec.vcluster`
// +kubebuilder:printcolumn:name="Template",type=string,JSONPath=`.spec.template`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.installedVersion`

// AddonInstall 某虚拟集群安装的一个应用。
type AddonInstall struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AddonInstallSpec   `json:"spec,omitempty"`
	Status            AddonInstallStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AddonInstallList 列表。
type AddonInstallList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AddonInstall `json:"items"`
}
