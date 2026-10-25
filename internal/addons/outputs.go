package addons

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"strconv"
	"text/template"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"vk8s.io/vk8s/api/v1alpha1"
)

// ClusterInfo url 模板里可用的集群信息。
type ClusterInfo struct {
	Name       string
	AppsDomain string
}

// ResolveOutputs 解析 service / url 类 outputs；secret 类不返回（值按需读取）；对象尚不存在的项跳过。
func ResolveOutputs(ctx context.Context, c client.Reader, t *v1alpha1.AddonTemplate, cluster ClusterInfo) ([]v1alpha1.AddonOutputValue, error) {
	var out []v1alpha1.AddonOutputValue
	resolved := map[string]string{}
	for _, o := range t.Spec.Outputs {
		var val string
		switch o.Kind {
		case "service":
			svc := &corev1.Service{}
			err := c.Get(ctx, client.ObjectKey{Namespace: o.Service.Namespace, Name: o.Service.Name}, svc)
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			host := svc.Spec.ClusterIP
			if ing := svc.Status.LoadBalancer.Ingress; len(ing) > 0 {
				if ing[0].IP != "" {
					host = ing[0].IP
				} else if ing[0].Hostname != "" {
					host = ing[0].Hostname
				}
			}
			if host == "" || host == corev1.ClusterIPNone {
				continue
			}
			val = net.JoinHostPort(host, strconv.Itoa(int(o.Service.Port)))
		case "url":
			tpl, err := template.New(o.Name).Option("missingkey=zero").Parse(o.URL)
			if err != nil {
				return nil, fmt.Errorf("outputs[%s] url: %w", o.Name, err)
			}
			var b bytes.Buffer
			if err := tpl.Execute(&b, map[string]any{"Cluster": cluster, "Outputs": resolved}); err != nil {
				return nil, fmt.Errorf("outputs[%s] url: %w", o.Name, err)
			}
			val = b.String()
		default:
			continue
		}
		resolved[o.Name] = val
		out = append(out, v1alpha1.AddonOutputValue{Name: o.Name, Label: o.Label, Value: val})
	}
	return out, nil
}

// ReadSecretOutput 读 secret 类 output 的值。
func ReadSecretOutput(ctx context.Context, c client.Reader, o v1alpha1.AddonOutput) (string, error) {
	if o.Secret == nil {
		return "", fmt.Errorf("output %s is not a secret", o.Name)
	}
	sec := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: o.Secret.Namespace, Name: o.Secret.Name}, sec); err != nil {
		return "", err
	}
	v, ok := sec.Data[o.Secret.Key]
	if !ok {
		return "", fmt.Errorf("secret %s/%s has no key %q", o.Secret.Namespace, o.Secret.Name, o.Secret.Key)
	}
	return string(v), nil
}
