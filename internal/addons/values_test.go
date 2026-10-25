package addons

import (
	"testing"

	"sigs.k8s.io/yaml"
)

func TestMergeValuesOverridesDefaultsByPath(t *testing.T) {
	out, err := MergeValues(devpodsTemplate(), map[string]string{"webui": "true", "banner": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := yaml.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v["webui"].(map[string]any)["enabled"] != true {
		t.Fatalf("webui.enabled must be bool true: %v", v["webui"])
	}
	gw := v["gateway"].(map[string]any)
	if gw["service"].(map[string]any)["type"] != "LoadBalancer" || gw["banner"] != "hi" {
		t.Fatalf("gateway = %v", gw)
	}
	if v["tier"] != "small" {
		t.Fatalf("default for unset input: %v", v["tier"])
	}
	if _, ok := v["admin"]; ok {
		t.Fatalf("input with no value and no default must not be written: %v", v["admin"])
	}
}
