package runner

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// NewCommand `vk8s addon`（子命令 run）。
func NewCommand() *cobra.Command {
	root := &cobra.Command{Use: "addon", Short: "应用目录安装器（在 vk8s-system 的 Job 里运行）"}
	var specPath, termLog string
	opts := Options{}
	run := &cobra.Command{
		Use:           "run",
		Short:         "按 spec.json 调用内置 helm 安装 / 升级 / 卸载，结果写进 termination message",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := os.ReadFile(specPath)
			if err != nil {
				return writeResult(termLog, Result{Message: "read spec: " + err.Error()})
			}
			var spec RunSpec
			if err := json.Unmarshal(raw, &spec); err != nil {
				return writeResult(termLog, Result{Message: "decode spec: " + err.Error()})
			}
			res := Run(cmd.Context(), spec, opts)
			fmt.Fprintln(cmd.OutOrStdout(), res.LogTail)
			return writeResult(termLog, res)
		},
	}
	f := run.Flags()
	f.StringVar(&specPath, "spec", "/run/vk8s/addon/spec.json", "RunSpec JSON")
	f.StringVar(&opts.Kubeconfig, "kubeconfig", "/run/vk8s/kubeconfig/kubeconfig", "目标虚拟集群的 kubeconfig")
	f.StringVar(&opts.Helm, "helm", "helm", "helm 可执行文件")
	f.StringVar(&opts.WorkDir, "workdir", "/tmp/vk8s-addon", "临时目录（values.yaml）")
	f.StringVar(&termLog, "termination-log", "/dev/termination-log", "结果写入位置")
	root.AddCommand(run)
	return root
}

// writeResult 写结果；失败时返回 error 使进程退出码非零。
func writeResult(path string, r Result) error {
	b, _ := json.Marshal(r)
	if len(b) > 4000 { // termination message 上限 4096 字节
		if excess := len(b) - 4000; excess < len(r.LogTail) {
			r.LogTail = r.LogTail[excess:]
		} else {
			r.LogTail = ""
		}
		b, _ = json.Marshal(r)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("write termination log: %w", err)
	}
	if !r.OK {
		return fmt.Errorf("%s", r.Message)
	}
	return nil
}
