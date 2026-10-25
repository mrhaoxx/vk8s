// Package config 定义平台配置文件（由 ConfigMap 挂进 manager）的结构与加载。
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/names"
)

// SecretRef 形如 "namespace/name" 的 Secret 引用。
type SecretRef string

// Split 拆成 namespace, name。
func (s SecretRef) Split() (string, string) {
	parts := strings.SplitN(string(s), "/", 2)
	if len(parts) != 2 {
		return "", string(s)
	}
	return parts[0], parts[1]
}

// OIDC OIDC 登录配置。
type OIDC struct {
	Issuer              string    `json:"issuer"`
	ClientID            string    `json:"clientID"`
	ClientSecretRef     SecretRef `json:"clientSecretRef"` // Secret key: clientSecret
	UsernameClaim       string    `json:"usernameClaim"`
	EmailClaim          string    `json:"emailClaim"`
	Scopes              []string  `json:"scopes"`
	AutoCreateUsers     bool      `json:"autoCreateUsers"`
	AllowedEmailDomains []string  `json:"allowedEmailDomains"`
}

// Enabled 是否配置了 OIDC。
func (o *OIDC) Enabled() bool { return o != nil && o.Issuer != "" }

// BootstrapAdmin 首次启动创建的静态管理员。
type BootstrapAdmin struct {
	Username          string    `json:"username"`
	PasswordSecretRef SecretRef `json:"passwordSecretRef"` // Secret key: password（明文，启动时转 bcrypt）
}

// Auth 认证配置。
type Auth struct {
	SessionTTL     Duration        `json:"sessionTTL"`
	BootstrapAdmin *BootstrapAdmin `json:"bootstrapAdmin,omitempty"`
	OIDC           *OIDC           `json:"oidc,omitempty"`
	// PublicURL 浏览器访问 UI 的地址，用于 OIDC 回调，例如 https://console.vk8s.corp.com
	PublicURL string `json:"publicURL"`
}

// Limits 平台上限。
type Limits struct {
	MaxVClustersPerUser int      `json:"maxVClustersPerUser"`
	TokenDefaultTTL     Duration `json:"tokenDefaultTTL"`
	TokenMaxTTL         Duration `json:"tokenMaxTTL"`
}

// Defaults 用户建集群时的默认值与上限。
type Defaults struct {
	Quota         v1alpha1.Quota            `json:"quota"`
	MaxQuota      v1alpha1.Quota            `json:"maxQuota"`
	PodSecurity   v1alpha1.PodSecurityLevel `json:"podSecurity"`
	NodePlacement v1alpha1.NodePlacement    `json:"nodePlacement"`
	ControlPlane  v1alpha1.ControlPlaneSpec `json:"controlPlane"`
}

// Placement 控制平面 pod 自身的调度。
type Placement struct {
	NodeSelector map[string]string   `json:"nodeSelector,omitempty"`
	Tolerations  []corev1.Toleration `json:"tolerations,omitempty"`
}

// NodePlacementPreset UI 下拉用的填表捷径。
type NodePlacementPreset struct {
	Name string `json:"name"`
	v1alpha1.NodePlacement
}

// LimitRange 写进每个租户 ns 的默认值。
type LimitRange struct {
	DefaultRequest corev1.ResourceList `json:"defaultRequest"`
	Default        corev1.ResourceList `json:"default"`
}

// Config 平台配置。
type Config struct {
	SystemNamespace string `json:"systemNamespace"`
	Image           string `json:"image"` // 控制平面 / syncer 使用的平台镜像
	APIDomain       string `json:"apiDomain"`
	// APIPort 用户从外部访问 <vc>.<apiDomain> 的端口，默认 443；网关走 NodePort 时填映射后的端口。
	// 写进 kubeconfig 的 server 与 status.apiEndpoint；网关自身监听端口不受影响。
	APIPort                  int               `json:"apiPort"`
	AppsDomain               string            `json:"appsDomain"`
	AppsTLSSecret            SecretRef         `json:"appsTLSSecret,omitempty"`
	HostServiceCIDR          string            `json:"hostServiceCIDR"`
	HostPodCIDR              string            `json:"hostPodCIDR"`
	HostDNSServiceIP         string            `json:"hostDNSServiceIP"`
	IngressNamespaceSelector map[string]string `json:"ingressNamespaceSelector"`
	ReservedNames            []string          `json:"reservedNames"`

	Auth                  Auth                  `json:"auth"`
	Limits                Limits                `json:"limits"`
	Defaults              Defaults              `json:"defaults"`
	ControlPlanePlacement Placement             `json:"controlPlanePlacement"`
	NodePlacementPresets  []NodePlacementPreset `json:"nodePlacementPresets"`
	LimitRange            LimitRange            `json:"limitRange"`
	Booking               Booking               `json:"booking"`
	Audit                 Audit                 `json:"audit"`
	Addons                Addons                `json:"addons"`
}

// Audit 审计（设计文档 2026-10-24 审计 §7.1）。
type Audit struct {
	Enabled       bool            `json:"enabled"`
	RetentionDays int             `json:"retentionDays"` // ClickHouse TTL，默认 365
	QueueSize     int             `json:"queueSize"`     // manager 内存队列，默认 50000
	BatchSize     int             `json:"batchSize"`     // 默认 2000
	BatchInterval Duration        `json:"batchInterval"` // 默认 2s
	ClickHouse    AuditClickHouse `json:"clickhouse"`
}

// AuditClickHouse ClickHouse 连接；密码只通过 Secret 引用提供（key: password）。
type AuditClickHouse struct {
	URL               string    `json:"url"`
	Database          string    `json:"database"` // 默认 vk8s_audit
	Username          string    `json:"username"`
	PasswordSecretRef SecretRef `json:"passwordSecretRef"`
}

// Booking 节点预约。
// Addons 应用目录的安装 Job（设计 2026-10-25 §7）。
type Addons struct {
	// JobTimeout helm --timeout，默认 10m；Job 的 activeDeadlineSeconds 再加 60s。
	JobTimeout Duration `json:"jobTimeout"`
	// JobResources 安装 Job 的资源，默认 requests 100m/128Mi、limits 1/512Mi。
	JobResources corev1.ResourceRequirements `json:"jobResources"`
}

type Booking struct {
	// Retention 终态（Completed / Rejected / Revoked）的预约保留多久后删除，默认 168h。
	Retention Duration `json:"retention"`
}

// Duration 支持 "24h" 这类写法的 time.Duration。
type Duration struct{ time.Duration }

// UnmarshalJSON 解析字符串或纳秒整数。
func (d *Duration) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		d.Duration = 0
		return nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// MarshalJSON 输出字符串。
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + d.Duration.String() + `"`), nil
}

// Load 读取 YAML 配置文件并填默认值。
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	c := &Config{}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) applyDefaults() {
	if c.SystemNamespace == "" {
		c.SystemNamespace = names.DefaultSystemNamespace
	}
	if c.APIPort == 0 {
		c.APIPort = 443
	}
	if c.Auth.SessionTTL.Duration == 0 {
		c.Auth.SessionTTL.Duration = 24 * time.Hour
	}
	if c.Auth.OIDC != nil {
		if c.Auth.OIDC.UsernameClaim == "" {
			c.Auth.OIDC.UsernameClaim = "preferred_username"
		}
		if c.Auth.OIDC.EmailClaim == "" {
			c.Auth.OIDC.EmailClaim = "email"
		}
		if len(c.Auth.OIDC.Scopes) == 0 {
			c.Auth.OIDC.Scopes = []string{"openid", "profile", "email"}
		}
	}
	if c.Limits.MaxVClustersPerUser == 0 {
		c.Limits.MaxVClustersPerUser = 3
	}
	if c.Limits.TokenDefaultTTL.Duration == 0 {
		c.Limits.TokenDefaultTTL.Duration = 30 * 24 * time.Hour
	}
	if c.Limits.TokenMaxTTL.Duration == 0 {
		c.Limits.TokenMaxTTL.Duration = 90 * 24 * time.Hour
	}
	if c.Defaults.PodSecurity == "" {
		c.Defaults.PodSecurity = v1alpha1.PodSecurityBaseline
	}
	if c.Addons.JobTimeout.Duration == 0 {
		c.Addons.JobTimeout.Duration = 10 * time.Minute
	}
	if c.Addons.JobResources.Requests == nil {
		c.Addons.JobResources.Requests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("128Mi")}
	}
	if c.Addons.JobResources.Limits == nil {
		c.Addons.JobResources.Limits = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("512Mi")}
	}
	if c.Booking.Retention.Duration == 0 {
		c.Booking.Retention.Duration = 168 * time.Hour
	}
	if c.Audit.RetentionDays == 0 {
		c.Audit.RetentionDays = 365
	}
	if c.Audit.QueueSize == 0 {
		c.Audit.QueueSize = 50000
	}
	if c.Audit.BatchSize == 0 {
		c.Audit.BatchSize = 2000
	}
	if c.Audit.BatchInterval.Duration == 0 {
		c.Audit.BatchInterval.Duration = 2 * time.Second
	}
	if c.Audit.ClickHouse.Database == "" {
		c.Audit.ClickHouse.Database = "vk8s_audit"
	}
	if c.Defaults.ControlPlane.StorageSize.IsZero() {
		c.Defaults.ControlPlane.StorageSize = resource.MustParse("5Gi")
	}
	if c.Defaults.ControlPlane.Resources.Requests == nil {
		c.Defaults.ControlPlane.Resources.Requests = corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("200m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		}
	}
	if c.Defaults.ControlPlane.Resources.Limits == nil {
		c.Defaults.ControlPlane.Resources.Limits = corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("2Gi"),
		}
	}
	if c.LimitRange.DefaultRequest == nil {
		c.LimitRange.DefaultRequest = corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		}
	}
	if c.LimitRange.Default == nil {
		c.LimitRange.Default = corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
		}
	}
	if len(c.ReservedNames) == 0 {
		c.ReservedNames = []string{"console", "api", "www"}
	}
}

// Validate 校验必填项。
func (c *Config) Validate() error {
	var missing []string
	if c.APIDomain == "" {
		missing = append(missing, "apiDomain")
	}
	if c.AppsDomain == "" {
		missing = append(missing, "appsDomain")
	}
	if c.Image == "" {
		missing = append(missing, "image")
	}
	if c.Auth.PublicURL == "" {
		missing = append(missing, "auth.publicURL")
	}
	if len(missing) > 0 {
		return fmt.Errorf("config: missing required fields: %s", strings.Join(missing, ", "))
	}
	if c.APIPort < 1 || c.APIPort > 65535 {
		return fmt.Errorf("config: apiPort %d out of range", c.APIPort)
	}
	switch c.Defaults.PodSecurity {
	case v1alpha1.PodSecurityPrivileged, v1alpha1.PodSecurityBaseline, v1alpha1.PodSecurityRestricted:
	default:
		return fmt.Errorf("config: defaults.podSecurity must be privileged|baseline|restricted")
	}
	if c.Audit.Enabled && c.Audit.ClickHouse.URL == "" {
		return fmt.Errorf("config: audit.enabled requires audit.clickhouse.url")
	}
	return nil
}

// APIEndpoint 虚拟集群对外的 API 地址：https://<vc>.<apiDomain>，非 443 时带端口。
func (c *Config) APIEndpoint(vc string) string {
	host := names.APIHost(vc, c.APIDomain)
	if c.APIPort == 443 {
		return "https://" + host
	}
	return fmt.Sprintf("https://%s:%d", host, c.APIPort)
}

// IsReservedName 名字是否保留。
func (c *Config) IsReservedName(name string) bool {
	for _, r := range c.ReservedNames {
		if r == name {
			return true
		}
	}
	return false
}
