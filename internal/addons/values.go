package addons

import (
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"

	"vk8s.io/vk8s/api/v1alpha1"
)

// MergeValues 模板默认 values + inputs（按点分路径覆盖；bool 写成布尔），返回 YAML。
func MergeValues(t *v1alpha1.AddonTemplate, inputs map[string]string) (string, error) {
	root := map[string]any{}
	if strings.TrimSpace(t.Spec.Values) != "" {
		if err := yaml.Unmarshal([]byte(t.Spec.Values), &root); err != nil {
			return "", fmt.Errorf("template values: %w", err)
		}
		if root == nil {
			root = map[string]any{}
		}
	}
	for _, in := range t.Spec.Inputs {
		v, ok := inputs[in.Name]
		if !ok || v == "" {
			v = in.Default
		}
		if v == "" {
			continue
		}
		var val any = v
		if in.Type == "bool" {
			val = v == "true"
		}
		if err := setPath(root, in.Path, val); err != nil {
			return "", err
		}
	}
	out, err := yaml.Marshal(root)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func setPath(m map[string]any, path string, val any) error {
	parts := strings.Split(path, ".")
	cur := m
	for _, p := range parts[:len(parts)-1] {
		next, ok := cur[p].(map[string]any)
		if !ok {
			if _, exists := cur[p]; exists {
				return fmt.Errorf("values path %q: %q is not a map", path, p)
			}
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = val
	return nil
}
