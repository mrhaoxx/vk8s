// Package controlplane 在同一进程内依次启动 kine(sqlite) → kube-apiserver → kube-controller-manager，
// 作为每个虚拟集群 StatefulSet pod 的 control-plane 容器（docs 第 6 节）。
//
// 上游组件以 Go 库形式复用：直接构造 k8s.io/kubernetes/cmd/{kube-apiserver,kube-controller-manager}/app
// 的 cobra 命令并在 goroutine 中执行，做法与 k3s 的 embedded executor 等价。
package controlplane

import (
	"flag"
	"os"

	"github.com/spf13/cobra"
	logsapi "k8s.io/component-base/logs/api/v1"

	// 注册 json 日志格式，使 --logging-format=json 可用（上游由 cmd/kube-apiserver/apiserver.go 的 main 包引入）。
	_ "k8s.io/component-base/logs/json/register"

	"vk8s.io/vk8s/internal/names"
)

func init() {
	// apiserver 与 kcm 的 RunE 都会调用 logsapi.ValidateAndApply。component-base 默认只允许在进程内
	// 应用一次日志配置，第二次返回错误并导致 kcm 启动失败。改为「配置未变则忽略」，
	// 并由 loggingArgs 保证两者参数完全一致。k3s 采用同样做法。
	logsapi.ReapplyHandling = logsapi.ReapplyHandlingIgnoreUnchanged
}

// NewCommand 返回 `vk8s control-plane` 子命令。flag 集合由 operator 固定，不可改名。
func NewCommand() *cobra.Command {
	o := Options{
		CertsDir:      names.CertsDir,
		DataDir:       names.DataDir,
		RunDir:        names.RunDir,
		LoggingFormat: "json",
		BindAddress:   "0.0.0.0",
	}
	cmd := &cobra.Command{
		Use:   "control-plane",
		Short: "在同一进程内运行虚拟集群的 kine + kube-apiserver + kube-controller-manager",
		Long: `control-plane 是每个虚拟集群 StatefulSet pod 的第一个容器。
它按顺序启动 kine（sqlite，数据在 PVC）、kube-apiserver（以 kine 为 etcd）、
kube-controller-manager（轮询 /readyz 通过后），任一组件退出则整个进程以非 0 退出，由 kubelet 重启。`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.PodIP = os.Getenv("POD_IP")
			if o.Verbosity == "" {
				o.Verbosity = inheritedVerbosity()
			}
			if err := o.Validate(); err != nil {
				return err
			}
			return Run(cmd.Context(), o)
		},
	}

	fs := cmd.Flags()
	fs.StringVar(&o.VClusterName, "vcluster-name", "", "所属虚拟集群名（必填）")
	fs.StringVar(&o.CertsDir, "certs-dir", o.CertsDir, "证书 Secret 挂载目录")
	fs.StringVar(&o.DataDir, "data-dir", o.DataDir, "sqlite 数据目录（PVC）")
	fs.StringVar(&o.RunDir, "run-dir", o.RunDir, "运行时目录：unix socket 与 egress selector 配置")
	fs.StringVar(&o.ServiceClusterIPRange, "service-cluster-ip-range", "", "Service CIDR，必须与宿主集群一致（必填）")
	fs.StringVar(&o.Verbosity, "component-verbosity", "", "透传给 apiserver/kcm 的 --v；空 = 继承本进程的 -v，未设置时为 2")
	fs.StringVar(&o.LoggingFormat, "logging-format", o.LoggingFormat, "apiserver/kcm 的日志格式：json | text")
	fs.StringVar(&o.BindAddress, "bind-address", o.BindAddress, "apiserver 监听地址；本机冒烟测试用 127.0.0.1")
	fs.StringVar(&o.AuditWebhookKubeconfig, "audit-webhook-kubeconfig", "", "apiserver 审计 webhook 的 kubeconfig；非空即启用审计")
	_ = cmd.MarkFlagRequired("vcluster-name")
	_ = cmd.MarkFlagRequired("service-cluster-ip-range")
	return cmd
}

// inheritedVerbosity 取 klog 在标准 flag 集上注册的 -v；用户未显式设置时用 2（上游组件的常用级别）。
func inheritedVerbosity() string {
	if f := flag.CommandLine.Lookup("v"); f != nil {
		if v := f.Value.String(); v != "" && v != f.DefValue {
			return v
		}
	}
	return "2"
}
