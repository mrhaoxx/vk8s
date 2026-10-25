#!/usr/bin/env bash
# 用法: hack/update-k8s-replaces.sh v1.33.0
# 从上游 k8s.io/kubernetes 指定版本的 go.mod 读出全部 staging 模块，
# 重写本仓库 go.mod 的 replace 块，使 staging 版本与 k8s.io/kubernetes 精确对应。
set -euo pipefail
ver="${1:?usage: $0 vX.Y.Z}"
staging_ver="v0${ver#v1}"
tmp="$(mktemp)"
curl -fsSL "https://raw.githubusercontent.com/kubernetes/kubernetes/${ver}/go.mod" -o "$tmp"
mods="$(grep -E '^\s+k8s.io/[a-z-]+ => ./staging/' "$tmp" | awk '{print $1}' | sort -u)"
{
  echo "replace ("
  for m in $mods; do
    echo "	$m => $m $staging_ver"
  done
  echo ")"
} > "${tmp}.replace"
# 删除旧 replace 块并追加新块
awk 'BEGIN{skip=0} /^replace \(/{skip=1} skip&&/^\)/{skip=0;next} !skip' go.mod > "${tmp}.gomod"
cat "${tmp}.gomod" "${tmp}.replace" > go.mod
sed -i.bak -E "s#(k8s.io/kubernetes) v1\.[0-9.]+#\1 ${ver}#" go.mod && rm -f go.mod.bak
rm -f "$tmp" "${tmp}.replace" "${tmp}.gomod"
echo "go.mod replaces updated to ${ver} / ${staging_ver}; run: go mod tidy"
