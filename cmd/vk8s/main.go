// vk8s 是整个平台的单一二进制：
//
//	vk8s manager        平台管理服务（UI、API、认证、TokenReview、operator）
//	vk8s gateway        kubectl 流量的 L4 SNI 网关
//	vk8s control-plane  每个虚拟集群的 kine + kube-apiserver + kube-controller-manager
//	vk8s syncer         每个虚拟集群的资源同步器 / 假 kubelet / metrics
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	addonrunner "vk8s.io/vk8s/internal/addons/runner"

	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"vk8s.io/vk8s/internal/controlplane"
	"vk8s.io/vk8s/internal/gateway"
	"vk8s.io/vk8s/internal/manager"
	"vk8s.io/vk8s/internal/syncer"
)

var (
	version = "dev"
	commit  = "none"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	root := &cobra.Command{
		Use:           "vk8s",
		Short:         "vk8s: 多用户隔离的虚拟 Kubernetes 开发环境",
		Version:       fmt.Sprintf("%s (%s)", version, commit),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	klog.InitFlags(nil)
	root.PersistentFlags().AddFlagSet(flagSet())

	root.AddCommand(
		manager.NewCommand(),
		gateway.NewCommand(),
		controlplane.NewCommand(),
		syncer.NewCommand(),
		addonrunner.NewCommand(),
	)

	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
