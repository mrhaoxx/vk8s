// Package policy 内嵌虚拟 apiserver 的审计策略；控制平面启动时把它写到 run-dir。
package policy

import _ "embed"

// YAML 审计策略原文（audit.k8s.io/v1 Policy）。
//
//go:embed policy.yaml
var YAML []byte
