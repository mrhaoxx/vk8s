package kubeletmetrics

import (
	"strings"
	"testing"
)

const sample = `# HELP container_memory_working_set_bytes Current working set of the container in bytes
# TYPE container_memory_working_set_bytes gauge
container_memory_working_set_bytes{container="web",id="/kubepods/a",namespace="vc-alice",pod="web-x-demo"} 1000 1792944098000
container_memory_working_set_bytes{container="db",id="/kubepods/b",namespace="vc-bob",pod="db-x-default"} 2000 1792944098000
container_memory_working_set_bytes{container="",id="/",namespace="",pod=""} 9999 1792944098000
container_memory_working_set_bytes{id="/system.slice/kubelet.service"} 300 1792944098000
# HELP machine_cpu_cores Number of logical CPU cores.
# TYPE machine_cpu_cores gauge
machine_cpu_cores{boot_id="x",machine_id="y",system_uuid="z"} 64
# HELP container_cpu_usage_seconds_total Cumulative cpu time consumed in seconds.
# TYPE container_cpu_usage_seconds_total counter
container_cpu_usage_seconds_total{container="db",namespace="vc-bob",pod="db-x-default"} 5
`

func TestFilterKeepsOnlyOwnNamespaceAndMachineSeries(t *testing.T) {
	out, err := Filter(strings.NewReader(sample), "vc-alice")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`pod="web-x-demo"`, "machine_cpu_cores", "# TYPE container_memory_working_set_bytes gauge"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	for _, bad := range []string{"vc-bob", "9999", "kubelet.service", "container_cpu_usage_seconds_total"} {
		if strings.Contains(s, bad) {
			t.Errorf("must not contain %q:\n%s", bad, s)
		}
	}
}

func TestFilterRejectsGarbage(t *testing.T) {
	if _, err := Filter(strings.NewReader("this is not { prometheus text"), "vc-alice"); err == nil {
		t.Fatal("unparseable input must error (never forward unfiltered text)")
	}
}

func TestRenameToVirtualNamesAndDropUnknownPods(t *testing.T) {
	in := `# TYPE container_memory_working_set_bytes gauge
container_memory_working_set_bytes{container="web",namespace="vc-alice",pod="web-x-demo"} 1000
container_memory_working_set_bytes{container="old",namespace="vc-alice",pod="gone-x-demo"} 5
machine_cpu_cores 64
`
	lookup := func(hostPod string) (string, string, bool) {
		if hostPod == "web-x-demo" {
			return "demo", "web", true
		}
		return "", "", false
	}
	out, err := Rename(strings.NewReader(in), "vc-alice", lookup)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `namespace="demo"`) || !strings.Contains(s, `pod="web"`) || strings.Contains(s, "gone-x-demo") || !strings.Contains(s, "machine_cpu_cores 64") {
		t.Fatalf("rename result:\n%s", s)
	}
}

func TestFilterStripsHostIdentityFromMachineSeries(t *testing.T) {
	out, err := Filter(strings.NewReader(sample), "vc-alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"machine_id", "system_uuid", "boot_id"} {
		if strings.Contains(string(out), bad) {
			t.Fatalf("host identity label %q must be stripped:\n%s", bad, out)
		}
	}
}
