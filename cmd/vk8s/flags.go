package main

import (
	"flag"

	"github.com/spf13/pflag"
)

// flagSet 把 klog 注册在标准 flag 集上的 flag 挂到 cobra 的 persistent flags 上。
// component-base 的 verflag 也在标准 flag 集上注册了一个非 bool 的 --version，与 cobra 为 root 自动加的
// --version bool flag 冲突（"version" flag declared as non-bool），这里跳过它。
func flagSet() *pflag.FlagSet {
	fs := pflag.NewFlagSet("global", pflag.ContinueOnError)
	flag.CommandLine.VisitAll(func(f *flag.Flag) {
		if f.Name == "version" {
			return
		}
		fs.AddGoFlag(f)
	})
	return fs
}
