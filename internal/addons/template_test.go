package addons

import (
	"strings"
	"testing"

	"vk8s.io/vk8s/api/v1alpha1"
)

func TestValidateTemplate(t *testing.T) {
	if err := ValidateTemplate(devpodsTemplate()); err != nil {
		t.Fatalf("valid template rejected: %v", err)
	}
	cases := []struct {
		field  string
		mutate func(*v1alpha1.AddonTemplate)
	}{
		{"name", func(x *v1alpha1.AddonTemplate) { x.Name = "Bad_Name" }},
		{"repository", func(x *v1alpha1.AddonTemplate) { x.Spec.Chart.Repository = "http://insecure/charts" }},
		{"version", func(x *v1alpha1.AddonTemplate) { x.Spec.Chart.Version = "" }},
		{"inputs", func(x *v1alpha1.AddonTemplate) { x.Spec.Inputs[1].Name = "webui" }},
		{"path", func(x *v1alpha1.AddonTemplate) { x.Spec.Inputs[0].Path = "" }},
		{"options", func(x *v1alpha1.AddonTemplate) { x.Spec.Inputs[2].Options = nil }},
		{"podSecurity", func(x *v1alpha1.AddonTemplate) { x.Spec.Requires.PodSecurity = "weird" }},
		{"generate", func(x *v1alpha1.AddonTemplate) { x.Spec.Generate[0].Kind = "rsa" }},
		{"outputs", func(x *v1alpha1.AddonTemplate) { x.Spec.Outputs[0].Kind = "foo" }},
		{"service", func(x *v1alpha1.AddonTemplate) { x.Spec.Outputs[0].Service = nil }},
	}
	for _, c := range cases {
		tpl := devpodsTemplate()
		c.mutate(tpl)
		err := ValidateTemplate(tpl)
		if err == nil || !strings.Contains(err.Error(), c.field) {
			t.Errorf("%s: err = %v, want mention of %q", c.field, err, c.field)
		}
	}
}

func TestValidateInputs(t *testing.T) {
	tpl := devpodsTemplate()
	if err := ValidateInputs(tpl, map[string]string{"webui": "true", "tier": "large"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateInputs(tpl, map[string]string{"nope": "x"}); err == nil {
		t.Error("unknown input must fail")
	}
	if err := ValidateInputs(tpl, map[string]string{"webui": "yes"}); err == nil {
		t.Error("bool accepts only true/false")
	}
	if err := ValidateInputs(tpl, map[string]string{"tier": "huge"}); err == nil {
		t.Error("enum accepts only options")
	}
	tpl.Spec.Inputs[1].Required = true
	if err := ValidateInputs(tpl, nil); err == nil {
		t.Error("missing required input must fail")
	}
	tpl.Spec.Inputs[1].Required = false
	tpl.Spec.Inputs[2].Required = true // 有 Default
	if err := ValidateInputs(tpl, nil); err != nil {
		t.Errorf("required with default must pass: %v", err)
	}
}

func TestRequiresMet(t *testing.T) {
	tpl := devpodsTemplate()
	if ok, _ := RequiresMet(tpl, v1alpha1.PodSecurityPrivileged); !ok {
		t.Error("privileged meets privileged")
	}
	if ok, why := RequiresMet(tpl, v1alpha1.PodSecurityBaseline); ok || why != "需要 privileged 档位" {
		t.Errorf("baseline: ok=%v why=%q", ok, why)
	}
	tpl.Spec.Requires.PodSecurity = ""
	if ok, _ := RequiresMet(tpl, v1alpha1.PodSecurityRestricted); !ok {
		t.Error("no requirement → always met")
	}
	tpl.Spec.Requires.PodSecurity = v1alpha1.PodSecurityBaseline
	if ok, _ := RequiresMet(tpl, v1alpha1.PodSecurityRestricted); ok {
		t.Error("restricted does not meet baseline")
	}
	if ok, _ := RequiresMet(tpl, v1alpha1.PodSecurityPrivileged); !ok {
		t.Error("privileged meets baseline")
	}
}

func TestMaskInputs(t *testing.T) {
	in := map[string]string{"webui": "true", "adminPassword": "s3cret"}
	out := MaskInputs(devpodsTemplate(), in)
	if out["adminPassword"] != "***" || out["webui"] != "true" || in["adminPassword"] != "s3cret" {
		t.Fatalf("out=%v in=%v", out, in)
	}
}
