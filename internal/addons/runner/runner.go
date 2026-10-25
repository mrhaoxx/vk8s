// Package runner 实现 `vk8s addon run`：在 Job 里调用内置 helm 完成安装 / 升级 / 卸载，
// 结果以 JSON 写进 termination message（设计 2026-10-25 §3.2）。
package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"vk8s.io/vk8s/api/v1alpha1"
)

// LogTailBytes Result.LogTail 的上限。
const LogTailBytes = 4096

// RunSpec Job 的输入（ConfigMap 里的 spec.json）。
type RunSpec struct {
	// Action install | upgrade | uninstall。
	Action         string              `json:"action"`
	Chart          v1alpha1.AddonChart `json:"chart"`
	Release        string              `json:"release"`
	Namespace      string              `json:"namespace"`
	Values         string              `json:"values,omitempty"`
	Timeout        string              `json:"timeout"`
	RegistryConfig string              `json:"registryConfig,omitempty"`
}

// Result Job 的输出（termination message）。
type Result struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
	LogTail string `json:"logTail"`
}

// Options 运行环境。
type Options struct {
	Helm       string
	Kubeconfig string
	WorkDir    string
}

const httpsRepoAlias = "vk8s-addon"

// Run 执行一次动作；永不 panic，所有失败都体现在 Result 里。
func Run(ctx context.Context, spec RunSpec, opts Options) Result {
	var out bytes.Buffer
	helm := func(args ...string) error {
		cmd := exec.CommandContext(ctx, opts.Helm, args...)
		cmd.Stdout, cmd.Stderr = &out, &out
		cmd.Env = os.Environ()
		if spec.RegistryConfig != "" {
			cmd.Env = append(cmd.Env, "HELM_REGISTRY_CONFIG="+spec.RegistryConfig)
		}
		return cmd.Run()
	}
	fail := func(msg string) Result { return Result{OK: false, Message: msg, LogTail: tail(out.String())} }
	kc := []string{"--kubeconfig", opts.Kubeconfig}

	switch spec.Action {
	case "uninstall":
		err := helm(append([]string{"uninstall", spec.Release, "-n", spec.Namespace, "--wait", "--timeout", spec.Timeout}, kc...)...)
		if err != nil {
			if strings.Contains(out.String(), "not found") {
				return Result{OK: true, Message: fmt.Sprintf("release %s already absent", spec.Release), LogTail: tail(out.String())}
			}
			return fail("helm uninstall failed: " + err.Error())
		}
		return Result{OK: true, Message: "uninstalled " + spec.Release, LogTail: tail(out.String())}
	case "install", "upgrade":
	default:
		return fail(fmt.Sprintf("unknown action %q", spec.Action))
	}

	if err := os.MkdirAll(opts.WorkDir, 0o700); err != nil {
		return fail(err.Error())
	}
	valuesFile := filepath.Join(opts.WorkDir, "values.yaml")
	if err := os.WriteFile(valuesFile, []byte(spec.Values), 0o600); err != nil {
		return fail(err.Error())
	}
	ref := strings.TrimSuffix(spec.Chart.Repository, "/") + "/" + spec.Chart.Name
	if strings.HasPrefix(spec.Chart.Repository, "https://") {
		if err := helm("repo", "add", httpsRepoAlias, spec.Chart.Repository, "--force-update"); err != nil {
			return fail("helm repo add failed: " + err.Error())
		}
		ref = httpsRepoAlias + "/" + spec.Chart.Name
	}
	args := []string{"upgrade", "--install", spec.Release, ref, "--version", spec.Chart.Version, "-n", spec.Namespace,
		"--create-namespace", "-f", valuesFile, "--wait", "--timeout", spec.Timeout}
	if err := helm(append(args, kc...)...); err != nil {
		return fail("helm upgrade --install failed: " + err.Error())
	}
	return Result{OK: true, Message: fmt.Sprintf("%s %s %s", spec.Release, spec.Chart.Name, spec.Chart.Version), LogTail: tail(out.String())}
}

func tail(s string) string {
	if len(s) <= LogTailBytes {
		return s
	}
	return s[len(s)-LogTailBytes:]
}
