package addons

import (
	"vk8s.io/vk8s/api/v1alpha1"
)

// devpodsTemplate spec §2.1 的示例。
func devpodsTemplate() *v1alpha1.AddonTemplate {
	t := &v1alpha1.AddonTemplate{}
	t.Name = "devpods"
	t.Spec = v1alpha1.AddonTemplateSpec{
		DisplayName: "DevPods",
		Description: "通过 ssh 使用的远程开发环境",
		Chart:       v1alpha1.AddonChart{Repository: "oci://harbor.example/library/charts", Name: "devpod", Version: "0.1.0"},
		Namespace:   "devpod-system",
		Release:     "devpod",
		Values:      "gateway:\n  service:\n    type: LoadBalancer\nwebui:\n  enabled: false\n",
		Inputs: []v1alpha1.AddonInput{
			{Name: "webui", Label: "启用 Web 界面", Type: "bool", Default: "false", Path: "webui.enabled"},
			{Name: "banner", Label: "横幅", Type: "string", Path: "gateway.banner"},
			{Name: "tier", Label: "规格", Type: "enum", Options: []string{"small", "large"}, Default: "small", Path: "tier"},
			{Name: "adminPassword", Label: "管理员密码", Type: "password", Path: "admin.password"},
		},
		Requires: v1alpha1.AddonRequires{PodSecurity: v1alpha1.PodSecurityPrivileged},
		Generate: []v1alpha1.AddonGenerate{
			{Secret: v1alpha1.NamespacedName{Namespace: "devpod-system", Name: "devpod-gateway-host-key"}, Kind: "sshKeyPair"},
			{Secret: v1alpha1.NamespacedName{Namespace: "devpod-system", Name: "devpod-gateway-internal-key"}, Kind: "sshKeyPair"},
		},
		Outputs: []v1alpha1.AddonOutput{
			{Name: "ssh", Label: "SSH 网关", Kind: "service", Service: &v1alpha1.AddonServiceRef{Namespace: "devpod-system", Name: "devpod-gateway", Port: 22}},
		},
	}
	return t
}
