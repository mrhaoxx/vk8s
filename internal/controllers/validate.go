package controllers

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"vk8s.io/vk8s/api/v1alpha1"
	"vk8s.io/vk8s/internal/config"
	"vk8s.io/vk8s/internal/names"
)

// ValidateName 校验虚拟集群名：DNS label、长度上限、不含 "--"、不在保留名单。
// reconciler 与管理 API 共用，保证两处规则一致。
func ValidateName(cfg *config.Config, name string) error {
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return fmt.Errorf("name %q: %s", name, strings.Join(errs, "; "))
	}
	if len(name) > names.MaxVClusterNameLength {
		return fmt.Errorf("name %q exceeds %d characters", name, names.MaxVClusterNameLength)
	}
	if strings.Contains(name, names.HostLabelSeparator) {
		return fmt.Errorf("name %q must not contain %q", name, names.HostLabelSeparator)
	}
	if cfg.IsReservedName(name) {
		return fmt.Errorf("name %q is reserved", name)
	}
	return nil
}

// ValidateQuota 校验 quota 不超过 max。max 中为零/缺失的项视为不限制。
func ValidateQuota(q, max v1alpha1.Quota) error {
	if !max.CPU.IsZero() && q.CPU.Cmp(max.CPU) > 0 {
		return fmt.Errorf("quota.cpu %s exceeds maximum %s", q.CPU.String(), max.CPU.String())
	}
	if !max.Memory.IsZero() && q.Memory.Cmp(max.Memory) > 0 {
		return fmt.Errorf("quota.memory %s exceeds maximum %s", q.Memory.String(), max.Memory.String())
	}
	if max.Pods > 0 && q.Pods > max.Pods {
		return fmt.Errorf("quota.pods %d exceeds maximum %d", q.Pods, max.Pods)
	}
	if !max.Storage.IsZero() && q.Storage.Cmp(max.Storage) > 0 {
		return fmt.Errorf("quota.storage %s exceeds maximum %s", q.Storage.String(), max.Storage.String())
	}
	if max.Services > 0 && q.Services > max.Services {
		return fmt.Errorf("quota.services %d exceeds maximum %d", q.Services, max.Services)
	}
	keys := make([]string, 0, len(q.Extended))
	for k := range q.Extended {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := q.Extended[k]
		if v.Sign() < 0 {
			return fmt.Errorf("quota.extended[%s] must not be negative", k)
		}
		m, ok := max.Extended[k]
		if !ok || m.IsZero() {
			continue
		}
		if v.Cmp(m) > 0 {
			return fmt.Errorf("quota.extended[%s] %s exceeds maximum %s", k, v.String(), m.String())
		}
	}
	return nil
}

// ValidatePodSecurity 校验安全档位取值。空值合法（由默认值补齐）。
func ValidatePodSecurity(level v1alpha1.PodSecurityLevel) error {
	switch level {
	case "", v1alpha1.PodSecurityPrivileged, v1alpha1.PodSecurityBaseline, v1alpha1.PodSecurityRestricted:
		return nil
	default:
		return fmt.Errorf("podSecurity %q must be privileged|baseline|restricted", level)
	}
}

// EffectivePodSecurity 返回生效的安全档位（空取平台默认）。
func EffectivePodSecurity(cfg *config.Config, vc *v1alpha1.VirtualCluster) v1alpha1.PodSecurityLevel {
	if vc.Spec.PodSecurity != "" {
		return vc.Spec.PodSecurity
	}
	return cfg.Defaults.PodSecurity
}
