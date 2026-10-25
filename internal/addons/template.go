// Package addons 应用目录：模板校验、values 合并、Job 与 Secret 构造、outputs 解析（设计 2026-10-25）。
package addons

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"vk8s.io/vk8s/api/v1alpha1"
)

var (
	inputTypes    = map[string]bool{"string": true, "bool": true, "enum": true, "password": true}
	generateKinds = map[string]bool{"sshKeyPair": true, "password": true}
	outputKinds   = map[string]bool{"service": true, "secret": true, "url": true}
)

// ValidateTemplate 目录项的结构校验（operator 与 API 共用）。
func ValidateTemplate(t *v1alpha1.AddonTemplate) error {
	if errs := validation.IsDNS1123Label(t.Name); len(errs) > 0 {
		return fmt.Errorf("name %q: %s", t.Name, strings.Join(errs, "; "))
	}
	s := t.Spec
	if !strings.HasPrefix(s.Chart.Repository, "oci://") && !strings.HasPrefix(s.Chart.Repository, "https://") {
		return fmt.Errorf("chart.repository must start with oci:// or https://")
	}
	if s.Chart.Name == "" || s.Chart.Version == "" {
		return fmt.Errorf("chart.name and chart.version are required")
	}
	if len(validation.IsDNS1123Label(s.Namespace)) > 0 || s.Release == "" {
		return fmt.Errorf("namespace must be a DNS label and release is required")
	}
	seen := map[string]bool{}
	for _, in := range s.Inputs {
		if in.Name == "" || seen[in.Name] {
			return fmt.Errorf("inputs: name %q empty or duplicated", in.Name)
		}
		seen[in.Name] = true
		if !inputTypes[in.Type] {
			return fmt.Errorf("inputs[%s].type %q unknown", in.Name, in.Type)
		}
		if in.Path == "" {
			return fmt.Errorf("inputs[%s].path is required", in.Name)
		}
		if in.Type == "enum" && len(in.Options) == 0 {
			return fmt.Errorf("inputs[%s].options required for enum", in.Name)
		}
	}
	switch s.Requires.PodSecurity {
	case "", v1alpha1.PodSecurityPrivileged, v1alpha1.PodSecurityBaseline, v1alpha1.PodSecurityRestricted:
	default:
		return fmt.Errorf("requires.podSecurity %q unknown", s.Requires.PodSecurity)
	}
	for _, g := range s.Generate {
		if !generateKinds[g.Kind] {
			return fmt.Errorf("generate kind %q unknown", g.Kind)
		}
		if g.Secret.Namespace == "" || g.Secret.Name == "" {
			return fmt.Errorf("generate secret namespace and name are required")
		}
	}
	for _, o := range s.Outputs {
		if !outputKinds[o.Kind] {
			return fmt.Errorf("outputs[%s] kind %q unknown", o.Name, o.Kind)
		}
		switch {
		case o.Kind == "service" && o.Service == nil:
			return fmt.Errorf("outputs[%s] kind service requires service", o.Name)
		case o.Kind == "secret" && o.Secret == nil:
			return fmt.Errorf("outputs[%s] kind secret requires secret", o.Name)
		case o.Kind == "url" && o.URL == "":
			return fmt.Errorf("outputs[%s] kind url requires url", o.Name)
		}
	}
	return nil
}

// ValidateInputs 用户填写的 inputs 是否合法。
func ValidateInputs(t *v1alpha1.AddonTemplate, inputs map[string]string) error {
	byName := map[string]v1alpha1.AddonInput{}
	for _, in := range t.Spec.Inputs {
		byName[in.Name] = in
	}
	for k, v := range inputs {
		in, ok := byName[k]
		if !ok {
			return fmt.Errorf("未知参数 %q", k)
		}
		switch in.Type {
		case "bool":
			if v != "true" && v != "false" {
				return fmt.Errorf("参数 %q 只能是 true 或 false", k)
			}
		case "enum":
			found := false
			for _, o := range in.Options {
				found = found || o == v
			}
			if !found {
				return fmt.Errorf("参数 %q 只能是 %s 之一", k, strings.Join(in.Options, " / "))
			}
		}
	}
	for _, in := range t.Spec.Inputs {
		if in.Required && inputs[in.Name] == "" && in.Default == "" {
			return fmt.Errorf("参数 %q 必填", in.Name)
		}
	}
	return nil
}

// strictness privileged < baseline < restricted。
func strictness(l v1alpha1.PodSecurityLevel) int {
	switch l {
	case v1alpha1.PodSecurityPrivileged:
		return 0
	case v1alpha1.PodSecurityBaseline:
		return 1
	default:
		return 2
	}
}

// RequiresMet 集群档位是否满足模板要求；不满足时给出中文原因。
func RequiresMet(t *v1alpha1.AddonTemplate, level v1alpha1.PodSecurityLevel) (bool, string) {
	req := t.Spec.Requires.PodSecurity
	if req == "" || strictness(level) <= strictness(req) {
		return true, ""
	}
	return false, fmt.Sprintf("需要 %s 档位", req)
}

// MaskInputs password 类型的值换成 ***（返回新 map）。
func MaskInputs(t *v1alpha1.AddonTemplate, inputs map[string]string) map[string]string {
	pw := map[string]bool{}
	if t != nil {
		for _, in := range t.Spec.Inputs {
			pw[in.Name] = in.Type == "password"
		}
	}
	out := make(map[string]string, len(inputs))
	for k, v := range inputs {
		if pw[k] {
			v = "***"
		}
		out[k] = v
	}
	return out
}
