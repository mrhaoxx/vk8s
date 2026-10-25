package translate

import (
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/validation"

	"vk8s.io/vk8s/internal/names"
)

// IngressEnv Ingress 翻译上下文。
type IngressEnv struct {
	HostNamespace string
	VCName        string
	// AppsDomain 平台 apps 域名（如 apps.vk8s.corp.com）。
	AppsDomain string
	// AppsTLSSecret 租户 ns 内的泛证书 Secret 名；空表示未配置，不自动补 tls。
	AppsTLSSecret string
}

// ValidateIngressHost 校验 host 是否属于该虚拟集群：`<vc>.<appsDomain>` 或 `<label>--<vc>.<appsDomain>`，
// label 必须是 DNS label 且不含 `--`。
func ValidateIngressHost(host, vc, appsDomain string) error {
	if host == "" {
		return Rejectf("ingress rule without host is not allowed; use <label>--%s.%s", vc, appsDomain)
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	suffix := "." + strings.ToLower(appsDomain)
	if !strings.HasSuffix(h, suffix) {
		return Rejectf("host %q is not under %s", host, appsDomain)
	}
	first := strings.TrimSuffix(h, suffix)
	if strings.Contains(first, ".") {
		return Rejectf("host %q must be exactly one label under %s", host, appsDomain)
	}
	if first == vc {
		return nil
	}
	sep := names.HostLabelSeparator + vc
	if !strings.HasSuffix(first, sep) {
		return Rejectf("host %q must be %s.%s or <label>%s%s.%s", host, vc, appsDomain, names.HostLabelSeparator, vc, appsDomain)
	}
	label := strings.TrimSuffix(first, sep)
	if label == "" || strings.Contains(label, names.HostLabelSeparator) || len(validation.IsDNS1123Label(label)) > 0 {
		return Rejectf("host %q: %q is not a valid DNS label (must not contain %q)", host, label, names.HostLabelSeparator)
	}
	return nil
}

// TranslateIngress 虚 → 宿 Ingress：host 白名单校验、backend Service 与 TLS secret 改名、自动补泛证书 tls。
func TranslateIngress(ving *networkingv1.Ingress, env IngressEnv) (*networkingv1.Ingress, error) {
	hing := &networkingv1.Ingress{
		ObjectMeta: HostObjectMeta(ving, env.HostNamespace),
		Spec:       *ving.Spec.DeepCopy(),
	}
	vns := ving.Namespace
	spec := &hing.Spec

	var hosts []string
	for i := range spec.Rules {
		r := &spec.Rules[i]
		if err := ValidateIngressHost(r.Host, env.VCName, env.AppsDomain); err != nil {
			return nil, err
		}
		if !containsString(hosts, r.Host) {
			hosts = append(hosts, r.Host)
		}
		if r.HTTP == nil {
			continue
		}
		for j := range r.HTTP.Paths {
			if err := rewriteIngressBackend(&r.HTTP.Paths[j].Backend, vns); err != nil {
				return nil, err
			}
		}
	}
	if spec.DefaultBackend != nil {
		if err := rewriteIngressBackend(spec.DefaultBackend, vns); err != nil {
			return nil, err
		}
	}
	for i := range spec.TLS {
		for _, h := range spec.TLS[i].Hosts {
			if err := ValidateIngressHost(h, env.VCName, env.AppsDomain); err != nil {
				return nil, err
			}
		}
		if spec.TLS[i].SecretName != "" {
			spec.TLS[i].SecretName = HostName(spec.TLS[i].SecretName, vns)
		}
	}
	if len(spec.TLS) == 0 && env.AppsTLSSecret != "" && len(hosts) > 0 {
		spec.TLS = []networkingv1.IngressTLS{{Hosts: hosts, SecretName: env.AppsTLSSecret}}
	}
	return hing, nil
}

func rewriteIngressBackend(b *networkingv1.IngressBackend, vns string) error {
	if b.Resource != nil {
		return Rejectf("ingress backend.resource is not supported; use backend.service")
	}
	if b.Service != nil {
		b.Service.Name = HostName(b.Service.Name, vns)
	}
	return nil
}

// ApplyIngressMutations Ingress spec 全部可变，整体覆盖。
func ApplyIngressMutations(desired, actual *networkingv1.Ingress) bool {
	changed := MergeMeta(&actual.ObjectMeta, desired.ObjectMeta)
	if !equality.Semantic.DeepEqual(actual.Spec, desired.Spec) {
		actual.Spec = *desired.Spec.DeepCopy()
		changed = true
	}
	return changed
}

// IngressStatusChanged 回写 status.loadBalancer；有变化则更新虚拟对象并返回 true。
func IngressStatusChanged(ving, hing *networkingv1.Ingress) bool {
	if equality.Semantic.DeepEqual(ving.Status, hing.Status) {
		return false
	}
	ving.Status = *hing.Status.DeepCopy()
	return true
}
