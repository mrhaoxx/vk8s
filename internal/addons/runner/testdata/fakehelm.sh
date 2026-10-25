#!/bin/sh
# 假 helm：记录参数与 HELM_REGISTRY_CONFIG，按约定返回成功或失败。
echo "ARGS $*" >> "$FAKEHELM_LOG"
echo "REGCFG ${HELM_REGISTRY_CONFIG:-}" >> "$FAKEHELM_LOG"
case "$*" in
  *fail-chart*) echo "Error: boom"; exit 1 ;;
  "uninstall missing"*) echo "Error: uninstall: Release not loaded: missing: release: not found"; exit 1 ;;
esac
echo ok
