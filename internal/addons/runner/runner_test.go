package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"vk8s.io/vk8s/api/v1alpha1"
)

func fake(t *testing.T) (Options, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "helm.log")
	t.Setenv("FAKEHELM_LOG", log)
	abs, _ := filepath.Abs("testdata/fakehelm.sh")
	return Options{Helm: abs, Kubeconfig: "/kc", WorkDir: dir}, log
}

func calls(t *testing.T, log string) []string {
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(l, "ARGS ") {
			out = append(out, strings.TrimPrefix(l, "ARGS "))
		}
	}
	return out
}

func TestRunInstallOCI(t *testing.T) {
	opts, log := fake(t)
	res := Run(context.Background(), RunSpec{Action: "install", Chart: v1alpha1.AddonChart{Repository: "oci://reg/charts", Name: "podinfo", Version: "6.15.0"},
		Release: "podinfo", Namespace: "podinfo", Values: "a: 1\n", Timeout: "5m"}, opts)
	if !res.OK {
		t.Fatalf("result = %+v", res)
	}
	c := calls(t, log)
	want := "upgrade --install podinfo oci://reg/charts/podinfo --version 6.15.0 -n podinfo --create-namespace -f " + filepath.Join(opts.WorkDir, "values.yaml") + " --wait --timeout 5m --kubeconfig /kc"
	if len(c) != 1 || c[0] != want {
		t.Fatalf("calls = %q\nwant %q", c, want)
	}
	if b, _ := os.ReadFile(filepath.Join(opts.WorkDir, "values.yaml")); string(b) != "a: 1\n" {
		t.Fatalf("values.yaml = %q", b)
	}
}

func TestRunInstallHTTPSRepo(t *testing.T) {
	opts, log := fake(t)
	res := Run(context.Background(), RunSpec{Action: "upgrade", Chart: v1alpha1.AddonChart{Repository: "https://charts.example/x", Name: "grafana", Version: "1.0.0"},
		Release: "grafana", Namespace: "grafana", Timeout: "5m"}, opts)
	c := calls(t, log)
	if !res.OK || len(c) != 2 || !strings.HasPrefix(c[0], "repo add vk8s-addon https://charts.example/x") ||
		!strings.Contains(c[1], "upgrade --install grafana vk8s-addon/grafana --version 1.0.0") {
		t.Fatalf("res=%+v calls=%q", res, c)
	}
}

func TestRunUninstallTreatsMissingReleaseAsSuccess(t *testing.T) {
	opts, log := fake(t)
	res := Run(context.Background(), RunSpec{Action: "uninstall", Release: "missing", Namespace: "x", Timeout: "1m"}, opts)
	if !res.OK || !strings.Contains(res.Message, "already absent") {
		t.Fatalf("res = %+v", res)
	}
	if c := calls(t, log); len(c) != 1 || !strings.HasPrefix(c[0], "uninstall missing -n x --wait --timeout 1m --kubeconfig /kc") {
		t.Fatalf("calls = %q", c)
	}
}

func TestRunFailureCarriesLogTail(t *testing.T) {
	opts, _ := fake(t)
	res := Run(context.Background(), RunSpec{Action: "install", Chart: v1alpha1.AddonChart{Repository: "oci://r", Name: "fail-chart", Version: "1"}, Release: "r", Namespace: "n", Timeout: "1m"}, opts)
	if res.OK || !strings.Contains(res.LogTail, "Error: boom") || len(res.LogTail) > LogTailBytes {
		t.Fatalf("res = %+v", res)
	}
}

func TestRunSetsRegistryConfig(t *testing.T) {
	opts, log := fake(t)
	Run(context.Background(), RunSpec{Action: "install", Chart: v1alpha1.AddonChart{Repository: "oci://r", Name: "c", Version: "1"}, Release: "r", Namespace: "n", Timeout: "1m",
		RegistryConfig: "/run/vk8s/registry/.dockerconfigjson"}, opts)
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "REGCFG /run/vk8s/registry/.dockerconfigjson") {
		t.Fatalf("log = %s", b)
	}
}

func TestCommandWritesTerminationLog(t *testing.T) {
	opts, _ := fake(t)
	dir := t.TempDir()
	write := func(spec RunSpec) string {
		p := filepath.Join(dir, spec.Release+".json")
		b, _ := json.Marshal(spec)
		_ = os.WriteFile(p, b, 0o600)
		return p
	}
	term := filepath.Join(dir, "term")
	ok := write(RunSpec{Action: "install", Chart: v1alpha1.AddonChart{Repository: "oci://r", Name: "c", Version: "1"}, Release: "good", Namespace: "n", Timeout: "1m"})
	cmd := NewCommand()
	cmd.SetArgs([]string{"run", "--spec", ok, "--kubeconfig", "/kc", "--helm", opts.Helm, "--termination-log", term, "--workdir", t.TempDir()})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var r Result
	b, _ := os.ReadFile(term)
	if err := json.Unmarshal(b, &r); err != nil || !r.OK {
		t.Fatalf("termination log %s err=%v", b, err)
	}
	bad := write(RunSpec{Action: "install", Chart: v1alpha1.AddonChart{Repository: "oci://r", Name: "fail-chart", Version: "1"}, Release: "bad", Namespace: "n", Timeout: "1m"})
	cmd = NewCommand()
	cmd.SetArgs([]string{"run", "--spec", bad, "--kubeconfig", "/kc", "--helm", opts.Helm, "--termination-log", term, "--workdir", t.TempDir()})
	if err := cmd.Execute(); err == nil {
		t.Fatal("failed run must return an error")
	}
}
