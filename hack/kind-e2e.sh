#!/usr/bin/env bash
# vk8s 端到端测试：在 kind 上部署平台 → 建用户与集群 → 拿 kubeconfig →
# kubectl run/expose/exec/logs/top → nodePlacement 收缩驱逐 → 节点预约 → 审计 → Ingress host 校验 → 清理。
#
# 用法:
#   hack/kind-e2e.sh                 # 完整流程，结束后删除 kind 集群
#   KEEP=1 hack/kind-e2e.sh          # 保留 kind 集群以便排查
#   SKIP_BUILD=1 hack/kind-e2e.sh    # 不重新构建镜像（复用 $IMAGE:$TAG）
#   BUILD_TARGET=image-local …       # 用 make image-local（宿主 module cache 离线编译）代替 make image
#   SKIP_CREATE=1 hack/kind-e2e.sh   # 复用已存在的 kind 集群与基础组件
#   SKIP_AUDIT=1 hack/kind-e2e.sh    # 不开审计（省掉 ClickHouse 镜像约 1 GB 与审计段落）
#
# 依赖: docker kind kubectl helm jq curl make
#
# 网络布局（kind 节点端口 → 本机端口）:
#   ingress-nginx 80/443      → 本机 80/443      （console 与用户 Ingress）
#   gateway NodePort 30443    → 本机 7443        （kubectl；chart 的 apiPort=7443，kubeconfig 直接可用）
# 域名使用 nip.io（*.127.0.0.1.nip.io 解析到 127.0.0.1），需要外网 DNS。
set -euo pipefail

# ---------- 参数 ----------
KIND_CLUSTER="${KIND_CLUSTER:-vk8s-e2e}"
IMAGE="${IMAGE:-vk8s}"
TAG="${TAG:-e2e}"
SYSTEM_NS="${SYSTEM_NS:-vk8s-system}"
API_DOMAIN="${API_DOMAIN:-vk8s.127.0.0.1.nip.io}"
APPS_DOMAIN="${APPS_DOMAIN:-apps.vk8s.127.0.0.1.nip.io}"
GATEWAY_HOST_PORT="${GATEWAY_HOST_PORT:-7443}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-e2e-admin-password}"
E2E_USER="${E2E_USER:-alice}"
E2E_USER2="${E2E_USER2:-bob}"
E2E_USER_PASSWORD="${E2E_USER_PASSWORD:-e2e-alice-password}"
VC="${VC:-e2e}"
TIMEOUT="${TIMEOUT:-600}"   # 等待集群 Running 的秒数
KEEP="${KEEP:-0}"
SKIP_BUILD="${SKIP_BUILD:-0}"
BUILD_TARGET="${BUILD_TARGET:-image}"
SKIP_CREATE="${SKIP_CREATE:-0}"

INGRESS_NGINX_MANIFEST="${INGRESS_NGINX_MANIFEST:-https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v1.12.1/deploy/static/provider/kind/deploy.yaml}"
METRICS_SERVER_MANIFEST="${METRICS_SERVER_MANIFEST:-https://github.com/kubernetes-sigs/metrics-server/releases/download/v0.7.2/components.yaml}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
HOST_KUBECONFIG="${WORK}/host.kubeconfig"
VC_KUBECONFIG="${WORK}/${VC}.kubeconfig"
COOKIE_JAR="${WORK}/cookies.txt"
MANAGER_URL="http://127.0.0.1:18080"
PF_PID=""

# ---------- 工具 ----------
log()  { printf '\n\033[1;34m[e2e]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[ ok ]\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31m[fail]\033[0m %s\n' "$*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || fail "缺少命令: $1"; }

hk() { kubectl --kubeconfig "$HOST_KUBECONFIG" "$@"; }   # 宿主集群
vk() { kubectl --kubeconfig "$VC_KUBECONFIG" "$@"; }     # 虚拟集群
# 让 retry … bash -c "…" 里也能用 hk / vk
export HOST_KUBECONFIG VC_KUBECONFIG
export -f hk vk

# retry <次数> <间隔秒> <命令...>
retry() {
  local n="$1" sleep_s="$2"; shift 2
  local i
  for ((i = 1; i <= n; i++)); do
    if "$@"; then return 0; fi
    sleep "$sleep_s"
  done
  return 1
}

# api <method> <path> [json body]  —— 带会话 cookie 调 manager API；非 GET 必须带 X-Requested-With: vk8s（CSRF）
api() {
  local method="$1" path="$2" body="${3:-}"
  if [[ -n "$body" ]]; then
    curl -sS -f -X "$method" -b "$COOKIE_JAR" -c "$COOKIE_JAR" -H 'X-Requested-With: vk8s' \
      -H 'Content-Type: application/json' -d "$body" "${MANAGER_URL}${path}"
  else
    curl -sS -f -X "$method" -b "$COOKIE_JAR" -c "$COOKIE_JAR" -H 'X-Requested-With: vk8s' "${MANAGER_URL}${path}"
  fi
}

# admin_login 以管理员身份重新登录（改 quota / nodePlacement / podSecurity 只有 admin 能做）
admin_login() {
  rm -f "$COOKIE_JAR"
  api POST /auth/login "$(jq -nc --arg u admin --arg p "$ADMIN_PASSWORD" '{username:$u,password:$p}')" >/dev/null
}
# 让 retry … bash -c "…" 里也能用 api / admin_login（子 shell 看不到未导出的函数与变量）
export -f api admin_login
export COOKIE_JAR MANAGER_URL ADMIN_PASSWORD

cleanup() {
  local rc=$?
  set +e
  if [[ -n "$PF_PID" ]]; then kill "$PF_PID" 2>/dev/null; fi
  if [[ "$rc" -ne 0 ]]; then
    log "失败，收集诊断信息"
    hk -n "$SYSTEM_NS" get pods -o wide 2>/dev/null
    hk get vc 2>/dev/null
    hk get vc "$VC" -o yaml 2>/dev/null | sed -n '/^status:/,$p'
    hk -n "vc-${VC}" get pods -o wide 2>/dev/null
    hk -n "$SYSTEM_NS" logs deploy/vk8s-manager --tail=100 2>/dev/null
    hk -n "vc-${VC}" logs "sts/vc-${VC}" -c control-plane --tail=100 2>/dev/null
    hk -n "vc-${VC}" logs "sts/vc-${VC}" -c syncer --tail=100 2>/dev/null
  fi
  if [[ "$KEEP" != "1" ]]; then
    log "删除 kind 集群 ${KIND_CLUSTER}"
    kind delete cluster --name "$KIND_CLUSTER" >/dev/null 2>&1 || true
    rm -rf "$WORK"
  else
    log "KEEP=1：保留 kind 集群 ${KIND_CLUSTER}，工作目录 ${WORK}"
    log "  宿主: export KUBECONFIG=${HOST_KUBECONFIG}"
    log "  虚拟: export KUBECONFIG=${VC_KUBECONFIG}"
  fi
  exit "$rc"
}
trap cleanup EXIT

# ---------- 0. 前置检查 ----------
for c in docker kind kubectl helm jq curl make; do need "$c"; done

# ---------- 1. kind 集群 ----------
if [[ "$SKIP_CREATE" != "1" ]]; then
  log "创建 kind 集群 ${KIND_CLUSTER}"
  cat > "${WORK}/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    kubeadmConfigPatches:
      - |
        kind: InitConfiguration
        nodeRegistration:
          kubeletExtraArgs:
            node-labels: "ingress-ready=true"
    extraPortMappings:
      - containerPort: 80
        hostPort: 80
        protocol: TCP
      - containerPort: 443
        hostPort: 443
        protocol: TCP
      - containerPort: 30443
        hostPort: ${GATEWAY_HOST_PORT}
        protocol: TCP
  - role: worker
  - role: worker
EOF
  kind create cluster --name "$KIND_CLUSTER" --config "${WORK}/kind.yaml" --kubeconfig "$HOST_KUBECONFIG" --wait 120s
else
  kind get kubeconfig --name "$KIND_CLUSTER" > "$HOST_KUBECONFIG"
fi
hk cluster-info >/dev/null
ok "kind 集群就绪"

# kind 默认 CIDR；与 helm values 保持一致
HOST_SERVICE_CIDR="${HOST_SERVICE_CIDR:-10.96.0.0/16}"
HOST_POD_CIDR="${HOST_POD_CIDR:-10.244.0.0/16}"

# ---------- 2. ingress-nginx ----------
if [[ "$SKIP_CREATE" != "1" ]]; then
  log "安装 ingress-nginx"
  hk apply -f "$INGRESS_NGINX_MANIFEST"
  hk -n ingress-nginx wait --for=condition=Ready pod -l app.kubernetes.io/component=controller --timeout=180s
  ok "ingress-nginx 就绪"

  # ---------- 3. metrics-server ----------
  log "安装 metrics-server（kind 需要 --kubelet-insecure-tls）"
  hk apply -f "$METRICS_SERVER_MANIFEST"
  hk -n kube-system patch deployment metrics-server --type=json -p \
    '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--kubelet-insecure-tls"}]'
  hk -n kube-system rollout status deployment/metrics-server --timeout=180s
  ok "metrics-server 就绪"
fi

# ---------- 4. 镜像 ----------
if [[ "$SKIP_BUILD" != "1" ]]; then
  log "构建镜像 ${IMAGE}:${TAG}"
  (cd "$ROOT" && make "$BUILD_TARGET" IMAGE="$IMAGE" TAG="$TAG")
fi
log "加载镜像到 kind"
kind load docker-image "${IMAGE}:${TAG}" --name "$KIND_CLUSTER"
ok "镜像已加载"

# ---------- 5. helm install ----------
log "安装 vk8s chart"
# 审计：默认开启并由 chart 部署单副本 ClickHouse；SKIP_AUDIT=1 跳过
AUDIT_SET=()
if [[ "${SKIP_AUDIT:-0}" != "1" ]]; then
  AUDIT_SET=(--set audit.enabled=true --set audit.clickhouse.deploy=true --set audit.clickhouse.storageSize=2Gi)
fi
helm --kubeconfig "$HOST_KUBECONFIG" upgrade --install vk8s "${ROOT}/deploy/helm/vk8s" \
  ${AUDIT_SET[@]+"${AUDIT_SET[@]}"} \
  --namespace "$SYSTEM_NS" --create-namespace \
  --set image.repository="$IMAGE" --set image.tag="$TAG" --set image.pullPolicy=IfNotPresent \
  --set apiDomain="$API_DOMAIN" --set apiPort="$GATEWAY_HOST_PORT" \
  --set appsDomain="$APPS_DOMAIN" \
  --set hostServiceCIDR="$HOST_SERVICE_CIDR" \
  --set hostPodCIDR="$HOST_POD_CIDR" \
  --set auth.bootstrapAdmin.password="$ADMIN_PASSWORD" \
  --set gateway.service.type=NodePort --set gateway.service.nodePort=30443 \
  --set manager.ingress.enabled=true --set manager.ingress.tls.enabled=false \
  --set defaults.podSecurity=privileged \
  --set defaults.controlPlane.storageSize=1Gi \
  --wait --timeout 10m
if [[ "$SKIP_CREATE" == "1" ]]; then
  # 复用集群：镜像 tag 不变时 helm upgrade 不会换 pod，显式滚动重启拿到刚 kind load 的镜像；并清掉上次残留
  hk -n "$SYSTEM_NS" rollout restart deployment/vk8s-manager deployment/vk8s-gateway
  hk delete vc "$VC" --ignore-not-found --wait=true --timeout=300s
  hk delete user "$E2E_USER" "$E2E_USER2" --ignore-not-found
  hk delete runtimeclass e2e-runc --ignore-not-found
fi
hk -n "$SYSTEM_NS" rollout status deployment/vk8s-manager --timeout=300s
hk -n "$SYSTEM_NS" rollout status deployment/vk8s-gateway --timeout=300s
if [[ "${SKIP_AUDIT:-0}" != "1" ]]; then
  hk -n "$SYSTEM_NS" rollout status statefulset/vk8s-clickhouse --timeout=600s
fi
hk get crd users.vk8s.io virtualclusters.vk8s.io accesstokens.vk8s.io >/dev/null
ok "平台组件就绪"

# ---------- 6. 连接 manager API ----------
log "port-forward 到 vk8s-manager:8080"
hk -n "$SYSTEM_NS" port-forward svc/vk8s-manager 18080:8080 >/dev/null 2>&1 &
PF_PID=$!
retry 30 1 curl -sSf "${MANAGER_URL}/healthz" >/dev/null || fail "manager /healthz 不可达"

log "管理员登录"
api POST /auth/login "$(jq -nc --arg u admin --arg p "$ADMIN_PASSWORD" '{username:$u,password:$p}')" >/dev/null
api GET /auth/me | jq -e '.user.role == "admin"' >/dev/null || fail "/auth/me 不是 admin"
ok "管理员登录成功"

# ---------- 7. 建用户与集群 ----------
log "创建用户 ${E2E_USER}"
api POST /api/v1/users "$(jq -nc --arg n "$E2E_USER" \
  '{name:$n, displayName:"Alice", email:"alice@example.com", role:"user"}')" >/dev/null
api POST "/api/v1/users/${E2E_USER}/password" "$(jq -nc --arg p "$E2E_USER_PASSWORD" '{password:$p}')" >/dev/null
hk get user "$E2E_USER" >/dev/null
ok "User ${E2E_USER} 已创建"

log "以 ${E2E_USER} 登录并创建集群 ${VC}"
rm -f "$COOKIE_JAR"
api POST /auth/login "$(jq -nc --arg u "$E2E_USER" --arg p "$E2E_USER_PASSWORD" '{username:$u,password:$p}')" >/dev/null
api POST /api/v1/vclusters "$(jq -nc --arg n "$VC" '{name:$n}')" >/dev/null
hk get vc "$VC" -o jsonpath='{.spec.owner}' | grep -qx "$E2E_USER" || fail "owner 不是 ${E2E_USER}"

log "等待集群 Running（最多 ${TIMEOUT}s）"
deadline=$(( $(date +%s) + TIMEOUT ))
while :; do
  phase="$(hk get vc "$VC" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  if [[ "$phase" == "Running" ]]; then break; fi
  if [[ "$phase" == "Failed" ]]; then
    hk get vc "$VC" -o yaml | sed -n '/^status:/,$p'
    fail "集群进入 Failed"
  fi
  if (( $(date +%s) > deadline )); then
    hk get vc "$VC" -o yaml | sed -n '/^status:/,$p'
    hk -n "vc-${VC}" get pods -o wide || true
    fail "等待 Running 超时（当前 ${phase:-<空>}）"
  fi
  sleep 5
done
hk get vc "$VC" -o jsonpath='{.status.hostNamespace}' | grep -qx "vc-${VC}"
hk get vc "$VC" -o jsonpath='{.status.apiEndpoint}' | grep -qx "https://${VC}.${API_DOMAIN}:${GATEWAY_HOST_PORT}"
hk -n "vc-${VC}" get resourcequota/vk8s-quota limitrange/vk8s-limits "sts/vc-${VC}" "svc/vc-${VC}" >/dev/null
ok "集群 ${VC} Running"

# ---------- 8. kubeconfig ----------
log "签发 token 并写出 kubeconfig"
token_resp="$(api POST "/api/v1/vclusters/${VC}/tokens" '{"description":"e2e","ttl":"1h"}')"
echo "$token_resp" | jq -r '.kubeconfig' > "$VC_KUBECONFIG"
grep -q 'certificate-authority-data' "$VC_KUBECONFIG" || fail "kubeconfig 缺少 CA"
api GET "/api/v1/vclusters/${VC}/tokens" | jq -e 'length >= 1' >/dev/null
hk get accesstokens -o jsonpath='{.items[*].spec.vcluster}' | grep -qw "$VC"
grep -q "server: https://${VC}.${API_DOMAIN}:${GATEWAY_HOST_PORT}" "$VC_KUBECONFIG" || fail "kubeconfig 的 server 没带 apiPort"
ok "kubeconfig 写入 ${VC_KUBECONFIG}（server 直接可用，不需改写）"

# ---------- 9. kubectl 冒烟 ----------
log "kubectl 基本命令"
retry 30 2 vk get --raw /readyz >/dev/null || fail "经网关访问虚拟 apiserver 失败"
vk get nodes -o wide
vk get ns
vk auth can-i '*' '*' --all-namespaces | grep -qx yes || fail "用户不是虚拟集群 cluster-admin"
vk -n kube-system get deploy coredns >/dev/null || fail "CoreDNS 未投放"
vk get clusterrolebinding vk8s:admins >/dev/null || fail "vk8s:admins 绑定缺失"
ok "认证与基础对象正常"

log "kubectl run / expose / exec / logs"
vk create ns demo
vk -n demo run web --image=nginx:1.27-alpine --port=80 --labels=app=web
vk -n demo wait --for=condition=Ready pod/web --timeout=300s
hk -n "vc-${VC}" get pods -l vk8s.io/vname=web -o name | grep -q pod/ || fail "宿主侧未出现同步的 Pod"
vk -n demo expose pod web --port=80 --name=web
host_ip="$(hk -n "vc-${VC}" get svc -l vk8s.io/vname=web -o jsonpath='{.items[0].spec.clusterIP}')"
virt_ip="$(vk -n demo get svc web -o jsonpath='{.spec.clusterIP}')"
[[ "$host_ip" == "$virt_ip" ]] || fail "Service ClusterIP 两侧不一致：host=${host_ip} virtual=${virt_ip}"
# apiserver 主动连用户 Pod 的路径：egress 隧道 → syncer 直连转发。services/proxy 与 service 形式的 webhook、聚合 API 同路
retry 10 2 bash -c "vk get --raw /api/v1/namespaces/demo/services/web:80/proxy/ | grep -qi html" \
  || fail "services/proxy 经 egress 隧道访问失败"
vk -n demo exec web -- sh -c 'wget -qO- http://127.0.0.1/ | head -1' | grep -qi html || fail "exec 失败"
vk -n demo logs web --tail=5 >/dev/null || fail "logs 失败"
# CoreDNS 随集群 Running 后才投放并需等 k8s 插件同步，先等它就绪再做集群内 DNS 测试
vk -n kube-system rollout status deployment/coredns --timeout=180s >/dev/null || fail "CoreDNS 未就绪"
# 不用 `run --rm -i`：短命容器在 kubectl attach 前就退出，会回退到 logs 且偶发吞掉输出。改为跑完再读日志；
# 容器内重试覆盖 CoreDNS 刚就绪、kube-proxy 规则尚未下发的窗口。
vk -n demo run curl --image=curlimages/curl:8.11.1 --restart=Never --quiet -- \
  sh -c 'for i in $(seq 1 15); do curl -sS --max-time 5 http://web.demo.svc.cluster.local/ && exit 0; sleep 2; done; exit 1'
vk -n demo wait --for=jsonpath='{.status.phase}'=Succeeded pod/curl --timeout=120s >/dev/null \
  || { vk -n demo logs curl; fail "集群内 DNS / Service 访问失败（curl Pod 未成功结束）"; }
# 容器刚结束的几十毫秒内读日志偶发为空，重试几次
retry 10 1 bash -c "vk -n demo logs curl | grep -qi html" || fail "集群内 DNS / Service 访问失败"
vk -n demo delete pod curl --wait=false >/dev/null
ok "run / expose / services-proxy / exec / logs / DNS 正常"

log "kubectl top（依赖 metrics-server，首次采集可能需要 1-2 分钟）"
retry 30 5 vk top nodes >/dev/null || fail "kubectl top nodes 失败"
retry 30 5 vk -n demo top pod web >/dev/null || fail "kubectl top pod 失败"
ok "kubectl top 正常"

log "Deployment 控制器（kcm）"
vk -n demo create deployment api --image=nginx:1.27-alpine --replicas=2
vk -n demo rollout status deployment/api --timeout=300s
[[ "$(vk -n demo get pods -l app=api --field-selector=status.phase=Running -o name | wc -l | tr -d ' ')" == "2" ]] \
  || fail "Deployment 副本数不为 2"
ok "kcm 正常"

log "LimitRange 镜像：虚拟 Pod 拿到与宿主相同的默认 requests"
retry 30 2 vk -n demo get limitrange vk8s-limits >/dev/null || fail "虚拟 namespace demo 里没有 LimitRange vk8s-limits"
vk -n demo get limitrange vk8s-limits -o jsonpath='{.spec.limits[0].defaultRequest.cpu}' | grep -q . || fail "LimitRange 没有 defaultRequest"
# web 是在 LimitRange 镜像到 demo 之前/之后创建的都可能，api 的 Pod 一定在之后；两侧 requests 必须一致
api_pod="$(vk -n demo get pods -l app=api -o jsonpath='{.items[0].metadata.name}')"
v_req="$(vk -n demo get pod "$api_pod" -o jsonpath='{.spec.containers[0].resources.requests.cpu}')"
h_req="$(hk -n "vc-${VC}" get pod "${api_pod}-x-demo" -o jsonpath='{.spec.containers[0].resources.requests.cpu}')"
[[ -n "$v_req" && "$v_req" == "$h_req" ]] || fail "两侧 requests.cpu 不一致：virtual=${v_req:-<空>} host=${h_req:-<空>}"
v_qos="$(vk -n demo get pod "$api_pod" -o jsonpath='{.status.qosClass}')"
h_qos="$(hk -n "vc-${VC}" get pod "${api_pod}-x-demo" -o jsonpath='{.status.qosClass}')"
[[ "$v_qos" == "$h_qos" ]] || fail "两侧 qosClass 不一致：virtual=${v_qos} host=${h_qos}"
ok "LimitRange 已镜像，两侧 requests 与 qosClass 一致（${v_req} / ${v_qos}）"

log "RuntimeClass 镜像"
hk apply -f - >/dev/null <<EOF
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata: {name: e2e-runc}
handler: runc
EOF
retry 30 2 vk get runtimeclass e2e-runc >/dev/null || fail "RuntimeClass 未镜像到虚拟集群"
vk -n demo run rc --image=nginx:1.27-alpine --restart=Never --overrides='{"spec":{"runtimeClassName":"e2e-runc"}}' >/dev/null
vk -n demo wait --for=condition=Ready pod/rc --timeout=180s >/dev/null || fail "使用 RuntimeClass 的 Pod 未就绪"
hk -n "vc-${VC}" get pod rc-x-demo -o jsonpath='{.spec.runtimeClassName}' | grep -qx e2e-runc || fail "宿主 Pod 未带 runtimeClassName"
vk -n demo delete pod rc --wait=false >/dev/null
hk delete runtimeclass e2e-runc >/dev/null
retry 30 2 bash -c "! vk get runtimeclass e2e-runc >/dev/null 2>&1" || fail "宿主删除后虚拟 RuntimeClass 未消失"
ok "RuntimeClass 镜像与回收正常"

log "kubectl debug（ephemeral 容器经 pods/ephemeralcontainers 下沉）"
vk -n demo debug web --image=busybox:1.37 --container=dbg --profile=general -- sleep 300 >/dev/null 2>&1
retry 60 2 bash -c "vk -n demo get pod web -o jsonpath='{.status.ephemeralContainerStatuses[?(@.name==\"dbg\")].state.running.startedAt}' | grep -q ." \
  || fail "ephemeral 容器 dbg 未在虚拟 Pod status 中变为 running"
hk -n "vc-${VC}" get pod web-x-demo -o jsonpath='{.spec.ephemeralContainers[*].name}' | grep -qw dbg || fail "宿主 Pod 没有 ephemeral 容器"
vk -n demo exec web -c dbg -- sh -c 'echo from-dbg' | grep -q from-dbg || fail "exec 进 ephemeral 容器失败"
ok "kubectl debug 正常"

log "kubectl port-forward"
vk -n demo port-forward svc/web 18081:80 >/dev/null 2>&1 &
PF2_PID=$!
retry 20 1 bash -c "curl -s --max-time 2 http://127.0.0.1:18081/ 2>/dev/null | grep -qi html" || { kill "$PF2_PID" 2>/dev/null; fail "port-forward 不通"; }
kill "$PF2_PID" 2>/dev/null; wait "$PF2_PID" 2>/dev/null || true
ok "port-forward 正常"

log "podAffinity 限定到虚拟 namespace"
vk create ns demo2
vk -n demo2 run web --image=nginx:1.27-alpine --labels=app=web >/dev/null
vk -n demo2 wait --for=condition=Ready pod/web --timeout=180s >/dev/null
# demo2/web 要求与「同 namespace 里 app=web 的 Pod」反亲和；demo/web 在宿主同一 ns 里也叫 app=web，
# 若没有 vnamespace 约束，宿主侧会把它也算进去导致两处都不可调度
vk -n demo2 run anti --image=nginx:1.27-alpine --restart=Never --labels=app=web --overrides='{"spec":{"affinity":{"podAntiAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":[{"labelSelector":{"matchLabels":{"app":"web"}},"topologyKey":"kubernetes.io/hostname"}]}}}}' >/dev/null
vk -n demo2 wait --for=condition=Ready pod/anti --timeout=180s >/dev/null || fail "反亲和 Pod 未就绪"
hk -n "vc-${VC}" get pod anti-x-demo2 -o jsonpath='{.spec.affinity.podAntiAffinity.requiredDuringSchedulingIgnoredDuringExecution[0].labelSelector.matchExpressions[*].key}' \
  | grep -q vk8s.io/vnamespace || fail "宿主 Pod 的反亲和 labelSelector 未加 vnamespace 约束"
ok "podAffinity 已限定到虚拟 namespace"

log "nodePlacement 收缩：被移出节点上的 Pod 被驱逐重建，nodeName 白名单拒绝新 Pod"
# 收到 web 所在的 worker（后面的 Ingress 测试还要用这个裸 Pod）；另一个 worker 被移出，对该集群不可见
allowed_node="$(vk -n demo get pod web -o jsonpath='{.spec.nodeName}')"
denied_node="$(hk get nodes -l '!node-role.kubernetes.io/control-plane' -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | grep -vx "$allowed_node" | head -1)"
[[ -n "$allowed_node" && -n "$denied_node" ]] || fail "kind 集群需要至少 2 个 worker"
# 收缩前在即将被移出的节点上放一个忽略 SIGTERM、grace 1 小时的 Pod：没有 grace 上限它要 1 小时才会消失
vk -n demo run doomed --image=busybox:1.37 --restart=Never \
  --overrides="{\"spec\":{\"nodeName\":\"${denied_node}\",\"terminationGracePeriodSeconds\":3600}}" \
  -- sh -c 'trap "" TERM; while true; do sleep 1; done' >/dev/null
vk -n demo wait --for=condition=Ready pod/doomed --timeout=180s >/dev/null || fail "doomed Pod 未就绪"
admin_login
api PATCH "/api/v1/vclusters/${VC}" "$(jq -nc --arg n "$allowed_node" '{nodePlacement:{nodeNames:[$n]}}')" >/dev/null
retry 30 2 bash -c "! vk get node ${denied_node} >/dev/null 2>&1" || fail "nodePlacement 收缩后 ${denied_node} 仍在虚拟集群可见"
vk get node "$allowed_node" >/dev/null || fail "允许的节点 ${allowed_node} 不可见"
# 驱逐：虚拟 Pod 进入删除中且 deletionGracePeriodSeconds 被压到上限 30（而不是它自己声明的 3600），
# 带 Event 说明原因；随后虚拟与宿主两侧的 Pod 在 grace 内消失（上游 podgc 要 40s 后才会介入，不会先于这里）
retry 30 2 bash -c "vk -n demo get pod doomed -o jsonpath='{.metadata.deletionGracePeriodSeconds}' 2>/dev/null | grep -qx 30" \
  || fail "doomed Pod 未被以 30s 上限的 gracePeriod 删除（当前 grace: $(vk -n demo get pod doomed -o jsonpath='{.metadata.deletionGracePeriodSeconds}' 2>/dev/null || echo '<已消失>')）"
vk -n demo get events --field-selector involvedObject.name=doomed,reason=EvictedByNodePlacement -o name | grep -q . \
  || fail "被移出节点上的 Pod 未产生 EvictedByNodePlacement 事件"
retry 30 2 bash -c "! hk -n vc-${VC} get pod doomed-x-demo >/dev/null 2>&1" || fail "宿主侧 doomed Pod 在 grace 后未消失"
retry 30 2 bash -c "! vk -n demo get pod doomed >/dev/null 2>&1" || fail "宿主 Pod 消失后虚拟 Pod 未被收尾"
# Deployment 的副本由虚拟 kcm 按新约束重建到允许的节点
retry 60 3 bash -c "nodes=\$(vk -n demo get pods -l app=api --field-selector=status.phase=Running -o jsonpath='{.items[*].spec.nodeName}'); [[ \$(echo \$nodes | wc -w) -eq 2 ]] && ! echo \$nodes | grep -qw ${denied_node}" \
  || fail "Deployment api 的副本未全部迁到 ${allowed_node}"
ok "被移出节点上的 Pod 已驱逐，Deployment 副本已重建到允许的节点"
vk -n demo run pinned --image=nginx:1.27-alpine --restart=Never --overrides="{\"spec\":{\"nodeName\":\"${denied_node}\"}}" >/dev/null
retry 30 2 bash -c "vk -n demo get events --field-selector involvedObject.name=pinned,reason=SyncRejected -o name | grep -q ." \
  || fail "nodeName 不在白名单未产生 SyncRejected"
if hk -n "vc-${VC}" get pod pinned-x-demo >/dev/null 2>&1; then fail "被拒绝的 Pod 出现在宿主"; fi
vk -n demo run ds-like --image=nginx:1.27-alpine --restart=Never >/dev/null
vk -n demo wait --for=condition=Ready pod/ds-like --timeout=180s >/dev/null
[[ "$(vk -n demo get pod ds-like -o jsonpath='{.spec.nodeName}')" == "$allowed_node" ]] || fail "新 Pod 没有落到允许的节点"
vk -n demo delete pod pinned ds-like --wait=false >/dev/null
api PATCH "/api/v1/vclusters/${VC}" '{"nodePlacement":{}}' >/dev/null
retry 30 2 bash -c "vk get node ${denied_node} >/dev/null 2>&1" || fail "nodePlacement 恢复后节点未回来"
ok "nodePlacement 恢复，nodeName 白名单生效"

log "节点预约：标记可预约 → 预约生效加入集群 → 删除预约回收 → 摘标签恢复"
book_node="$denied_node"
admin_login
api PUT "/api/v1/nodes/${book_node}/bookable" >/dev/null
# 带 bookable 标签的节点在 vk8s 内只能通过租约使用：对没有租约的集群立刻不可见
retry 30 2 bash -c "! vk get node ${book_node} >/dev/null 2>&1" || fail "标记可预约后 ${book_node} 仍对集群可见"
[[ "$(hk get node "$book_node" -o jsonpath='{.metadata.labels.vk8s\.io/bookable}')" == "true" ]] || fail "节点未打上 vk8s.io/bookable 标签"
# 以普通用户身份为自己的集群预约当前整点起 2 小时（UTC 整点对齐）
api POST /auth/login "$(jq -nc --arg u "$E2E_USER" --arg p "$E2E_USER_PASSWORD" '{username:$u,password:$p}')" >/dev/null
# start 取当前整点；若此刻离整点不到 10 秒，等过了整点再算，免得 POST 时已跨点被 API 以「早于当前整点」拒绝
if (( 10#$(date -u +%M%S) > 5950 )); then sleep 12; fi
book_start="$(date -u +%Y-%m-%dT%H:00:00Z)"
book_end="$(date -u -d '+2 hour' +%Y-%m-%dT%H:00:00Z 2>/dev/null || date -u -v+2H +%Y-%m-%dT%H:00:00Z)"
booking="$(api POST /api/v1/bookings "$(jq -nc --arg n "$book_node" --arg vc "$VC" --arg s "$book_start" --arg e "$book_end" '{node:$n,vcluster:$vc,start:$s,end:$e,purpose:"e2e"}')" | jq -r .name)"
[[ "$booking" == nb-* ]] || fail "创建预约失败: ${booking}"
retry 45 2 bash -c "vk get node ${book_node} >/dev/null 2>&1" || fail "预约生效后 ${book_node} 未出现在虚拟集群"
[[ "$(hk get vc "$VC" -o jsonpath='{.status.leasedNodes[0].node}')" == "$book_node" ]] || fail "VirtualCluster.status.leasedNodes 未记录租约"
[[ "$(hk get nb "$booking" -o jsonpath='{.status.phase}')" == "Active" ]] || fail "预约未进入 Active（$(hk get nb "$booking" -o jsonpath='{.status.phase}/{.status.reason}')）"
vk -n demo run leased --image=nginx:1.27-alpine --restart=Never --overrides="{\"spec\":{\"nodeName\":\"${book_node}\"}}" >/dev/null
vk -n demo wait --for=condition=Ready pod/leased --timeout=180s >/dev/null || fail "租约节点上的 Pod 未就绪"
# 删除预约 → 租约撤销 → 节点对集群不再允许 → 其上 Pod 以 30s 上限驱逐、节点消失
api DELETE "/api/v1/bookings/${booking}" >/dev/null
retry 30 2 bash -c "! vk -n demo get pod leased >/dev/null 2>&1" || fail "删除预约后租约节点上的 Pod 未被清理"
retry 30 2 bash -c "! vk get node ${book_node} >/dev/null 2>&1" || fail "删除预约后 ${book_node} 仍对集群可见"
admin_login
api DELETE "/api/v1/nodes/${book_node}/bookable" >/dev/null
retry 30 2 bash -c "vk get node ${book_node} >/dev/null 2>&1" || fail "摘标签后 ${book_node} 未回到集群"
ok "节点预约：标记、租约生效、删除回收、摘标签恢复"

log "podSecurity=baseline 下 hostNetwork 被宿主 PSA 拒绝并产生 Event；恢复 privileged 后成功"
api PATCH "/api/v1/vclusters/${VC}" '{"podSecurity":"baseline"}' >/dev/null
retry 30 2 bash -c "hk get ns vc-${VC} -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}' | grep -qx baseline" \
  || fail "宿主 ns 的 PSA enforce 标签未变为 baseline"
vk -n demo run hostnet --image=nginx:1.27-alpine --restart=Never --overrides='{"spec":{"hostNetwork":true}}' >/dev/null
retry 30 2 bash -c "vk -n demo get events --field-selector involvedObject.name=hostnet,reason=SyncError -o jsonpath='{.items[*].message}' | grep -qi 'violates PodSecurity'" \
  || fail "baseline 下 hostNetwork 未被拒绝或 Event 不含 PSA 原话"
vk -n demo delete pod hostnet --wait=false >/dev/null
api PATCH "/api/v1/vclusters/${VC}" '{"podSecurity":"privileged"}' >/dev/null
retry 30 2 bash -c "hk get ns vc-${VC} -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}' | grep -qx privileged" \
  || fail "PSA enforce 标签未恢复 privileged"
retry 30 2 bash -c "! vk -n demo get pod hostnet >/dev/null 2>&1" || fail "hostnet Pod 未删除"
vk -n demo run hostnet --image=nginx:1.27-alpine --restart=Never --overrides='{"spec":{"hostNetwork":true}}' >/dev/null
vk -n demo wait --for=condition=Ready pod/hostnet --timeout=180s >/dev/null || fail "privileged 下 hostNetwork Pod 未就绪"
[[ "$(vk -n demo get pod hostnet -o jsonpath='{.status.podIP}')" == "$(vk -n demo get pod hostnet -o jsonpath='{.status.hostIP}')" ]] \
  || fail "hostNetwork Pod 的 podIP 应等于 hostIP"
vk -n demo delete pod hostnet --wait=false >/dev/null
ok "podSecurity 档位切换生效"

# ---------- 9b. 配额即时拒绝 ----------
log "配额即时拒绝：超出 quota.pods 的 Pod 在创建时被 403，PVC 超 storage 直接失败"
admin_login
# PATCH 的 quota 是整体替换：先存下整份配额，只改 pods，最后整份恢复
orig_quota="$(hk get vc "$VC" -o json | jq -c '.spec.quota')"
# 上一段刚 --wait=false 删掉 hostnet，等它真正消失，否则 total 多算一个很快释放的名额
retry 30 2 bash -c "! vk -n demo get pod hostnet >/dev/null 2>&1" || fail "hostnet Pod 未删除"
# 虚拟集群当前非终态、未在删除中的 Pod 总数（含 kube-system 的 CoreDNS，它占的名额在宿主 hard 的开销里），恰好给用户留 1 个名额
total="$(vk get pods -A -o json | jq '[.items[] | select(.metadata.deletionTimestamp == null and .status.phase != "Succeeded" and .status.phase != "Failed")] | length')"
api PATCH "/api/v1/vclusters/${VC}" "$(jq -nc --argjson q "$orig_quota" --argjson n "$total" '{quota: ($q + {pods: $n})}')" >/dev/null
retry 20 2 bash -c "hk -n vc-${VC} get resourcequota vk8s-quota -o jsonpath='{.spec.hard.pods}' | grep -qx $((total + 2))" || fail "宿主 RQ 的 pods 未更新为 $((total + 2))"
vk create deployment quota-probe --image=nginx:1.27-alpine --replicas=3 >/dev/null
sleep 10
probe_pods="$(vk get pods -l app=quota-probe --no-headers --field-selector=status.phase!=Succeeded,status.phase!=Failed | wc -l | tr -d ' ')"
[[ "$probe_pods" == "1" ]] || { vk get pods -l app=quota-probe; vk describe rs -l app=quota-probe | tail -20; fail "期望只创建出 1 个探针 Pod，实际 ${probe_pods}"; }
vk describe rs -l app=quota-probe | grep -q "exceeded quota: vk8s-quota" || { vk describe rs -l app=quota-probe | tail -20; fail "ReplicaSet 事件里没有 exceeded quota"; }
if cat <<PVC | vk apply -f - 2>"${WORK}/pvc.err"; then fail "超配额的 PVC 不该创建成功"; fi
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: quota-probe, namespace: default}
spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 100Ti}}}
PVC
grep -q "exceeded quota: vk8s-quota" "${WORK}/pvc.err" || { cat "${WORK}/pvc.err"; fail "PVC 拒绝文案不对"; }
api PATCH "/api/v1/vclusters/${VC}" "$(jq -nc --argjson q "$orig_quota" '{quota: $q}')" >/dev/null
# ReplicaSet 对失败创建按指数退避重试，恢复后最多要等一两分钟
retry 60 2 bash -c "vk get pods -l app=quota-probe --no-headers --field-selector=status.phase=Running | wc -l | tr -d ' ' | grep -qx 3" || { vk get pods -l app=quota-probe; fail "恢复配额后探针没有补齐到 3 个"; }
vk delete deployment quota-probe --wait=false >/dev/null
ok "配额即时拒绝"

log "成员管理：加成员后 token 可用，移除后失效（apiserver 侧 TokenReview cache 2 分钟）"
api POST /api/v1/users "$(jq -nc --arg n "$E2E_USER2" '{name:$n, displayName:"Bob", email:"bob@example.com", role:"user"}')" >/dev/null
api POST "/api/v1/users/${E2E_USER2}/password" "$(jq -nc --arg p "$E2E_USER_PASSWORD" '{password:$p}')" >/dev/null
rm -f "$COOKIE_JAR"
api POST /auth/login "$(jq -nc --arg u "$E2E_USER" --arg p "$E2E_USER_PASSWORD" '{username:$u,password:$p}')" >/dev/null
api PUT "/api/v1/vclusters/${VC}/members/${E2E_USER2}" >/dev/null
rm -f "$COOKIE_JAR"
api POST /auth/login "$(jq -nc --arg u "$E2E_USER2" --arg p "$E2E_USER_PASSWORD" '{username:$u,password:$p}')" >/dev/null
api POST "/api/v1/vclusters/${VC}/tokens" '{"description":"e2e-bob","ttl":"1h"}' | jq -r '.kubeconfig' > "${WORK}/bob.kubeconfig"
retry 10 2 kubectl --kubeconfig "${WORK}/bob.kubeconfig" get ns >/dev/null || fail "成员 ${E2E_USER2} 的 kubeconfig 不可用"
kubectl --kubeconfig "${WORK}/bob.kubeconfig" auth whoami -o jsonpath='{.status.userInfo.username}' | grep -qx "$E2E_USER2" \
  || fail "虚拟集群内身份不是 ${E2E_USER2}"
rm -f "$COOKIE_JAR"
api POST /auth/login "$(jq -nc --arg u "$E2E_USER" --arg p "$E2E_USER_PASSWORD" '{username:$u,password:$p}')" >/dev/null
api DELETE "/api/v1/vclusters/${VC}/members/${E2E_USER2}" >/dev/null
retry 40 5 bash -c "! kubectl --kubeconfig ${WORK}/bob.kubeconfig get ns >/dev/null 2>&1" \
  || fail "移除成员后 ${E2E_USER2} 的 token 在 200s 内仍可用"
ok "成员加入/移除生效"

if [[ "${SKIP_AUDIT:-0}" != "1" ]]; then
  log "审计：集群内操作、平台操作、登录失败均可查询，租户权限正确"
  # 1. 集群内：e2e 用户前面的 kubectl exec（create pods/exec）应已入库（apiserver 批量 2s + manager 批量 2s + 查询）
  rm -f "$COOKIE_JAR"
  api POST /auth/login "$(jq -nc --arg u "$E2E_USER" --arg p "$E2E_USER_PASSWORD" '{username:$u,password:$p}')" >/dev/null
  retry 30 2 bash -c "api GET '/api/v1/audit/events?vcluster=${VC}&user=${E2E_USER}&verb=create&resource=pods' | jq -e '[.items[] | select(.subresource==\"exec\")] | length > 0' >/dev/null" \
    || fail "审计里查不到 ${E2E_USER} 的 pods/exec 记录"
  # 2. 平台操作：admin 建集群
  admin_login
  retry 15 2 bash -c "api GET '/api/v1/audit/events?source=platform&user=admin' | jq -e '[.items[] | select(.platformAction==\"vcluster.create\" and .vcluster==\"${VC}\")] | length > 0' >/dev/null" \
    || fail "审计里查不到 vcluster.create 记录"
  # 3. 登录失败也要有记录（不带密码）
  bad_code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST -H 'X-Requested-With: vk8s' -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg u "$E2E_USER" '{username:$u,password:"definitely-wrong"}')" "${MANAGER_URL}/auth/login")"
  [[ "$bad_code" == "401" ]] || fail "错误密码登录应返回 401，得到 ${bad_code}"
  admin_login
  retry 15 2 bash -c "api GET '/api/v1/audit/events?source=platform&outcome=failure&user=${E2E_USER}' | jq -e '[.items[] | select(.platformAction==\"auth.login\" and .responseReason==\"failure\" and (.requestObject // \"\") == \"\")] | length > 0' >/dev/null" \
    || fail "审计里查不到失败登录记录"
  # 4. 租户权限：成员查自己集群 200、不带 vcluster 400、查不存在/无权限集群 404
  rm -f "$COOKIE_JAR"
  api POST /auth/login "$(jq -nc --arg u "$E2E_USER" --arg p "$E2E_USER_PASSWORD" '{username:$u,password:$p}')" >/dev/null
  api GET "/api/v1/audit/events?vcluster=${VC}&limit=1" >/dev/null || fail "成员查自己集群的审计失败"
  code="$(curl -sS -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" "${MANAGER_URL}/api/v1/audit/events")"
  [[ "$code" == "400" ]] || fail "租户不带 vcluster 应 400，得到 ${code}"
  code="$(curl -sS -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" "${MANAGER_URL}/api/v1/audit/events?vcluster=does-not-exist")"
  [[ "$code" == "404" ]] || fail "租户查无权限集群应 404，得到 ${code}"
  ok "审计：集群内操作、平台操作、登录失败均可查询，租户权限正确"
fi

# ---------- 9c. 应用目录 ----------
log "应用目录：podinfo 经安装 Job 装进虚拟集群，Ready 后有访问信息，卸载后资源消失"
admin_login
cat <<'TPL' | hk apply -f - >/dev/null
apiVersion: vk8s.io/v1alpha1
kind: AddonTemplate
metadata: {name: podinfo}
spec:
  displayName: podinfo
  description: e2e 用的最小 chart
  chart: {repository: "oci://ghcr.io/stefanprodan/charts", name: podinfo, version: 6.15.0}
  namespace: podinfo
  release: podinfo
  values: |
    replicaCount: 1
  outputs:
    - {name: web, label: Web, kind: service, service: {namespace: podinfo, name: podinfo, port: 9898}}
---
apiVersion: vk8s.io/v1alpha1
kind: AddonTemplate
metadata: {name: needs-privileged}
spec:
  displayName: needs-privileged
  chart: {repository: "oci://ghcr.io/stefanprodan/charts", name: podinfo, version: 6.15.0}
  namespace: np
  release: np
  requires: {podSecurity: privileged}
TPL
api GET /api/v1/config | jq -e '.addonsEnabled == true' >/dev/null || fail "config.addonsEnabled 应为 true"
api POST "/api/v1/vclusters/${VC}/addons" '{"template":"podinfo"}' >/dev/null
if ! retry 90 4 bash -c "api GET /api/v1/vclusters/${VC}/addons | jq -e '.[] | select(.template==\"podinfo\" and .phase==\"Ready\")' >/dev/null"; then
  api GET "/api/v1/vclusters/${VC}/addons" | jq . || true
  api GET "/api/v1/vclusters/${VC}/addons/podinfo/logs" | jq -r .log || true
  fail "podinfo 没有在 6 分钟内 Ready"
fi
api GET "/api/v1/vclusters/${VC}/addons" | jq -e '.[] | select(.template=="podinfo") | .outputs[] | select(.name=="web" and (.value | length) > 0)' >/dev/null \
  || fail "podinfo 的 outputs 里没有 web 地址"
vk -n podinfo get deploy podinfo >/dev/null || fail "虚拟集群里没有 podinfo Deployment"
hk get addoninstall "${VC}-podinfo" -o jsonpath='{.status.installedVersion}' | grep -qx 6.15.0 || fail "AddonInstall.status.installedVersion 不对"
api DELETE "/api/v1/vclusters/${VC}/addons/podinfo" >/dev/null
retry 60 3 bash -c "! hk get addoninstall ${VC}-podinfo >/dev/null 2>&1" || fail "卸载后 AddonInstall 仍存在"
retry 30 2 bash -c "[ -z \"\$(vk -n podinfo get deploy -o name 2>/dev/null)\" ]" || fail "卸载后 podinfo Deployment 仍在"
# 档位不够的应用：建集群请求整体 400，且不留下集群
code="$(curl -s -o /dev/null -w '%{http_code}' -X POST -b "$COOKIE_JAR" -c "$COOKIE_JAR" -H 'X-Requested-With: vk8s' -H 'Content-Type: application/json' \
  -d '{"name":"addons-e2e","podSecurity":"baseline","addons":[{"template":"needs-privileged"}]}' "${MANAGER_URL}/api/v1/vclusters")"
[[ "$code" == "400" ]] || fail "档位不够的预装应用应返回 400，实际 ${code}"
! hk get vc addons-e2e >/dev/null 2>&1 || fail "400 后不应留下集群 addons-e2e"
hk delete addontemplate podinfo needs-privileged >/dev/null
ok "应用目录"

# ---------- 10. Ingress host 校验 ----------
log "合规 Ingress host"
good_host="web--${VC}.${APPS_DOMAIN}"
vk -n demo create ingress web --class=nginx --rule="${good_host}/*=web:80"
retry 30 2 bash -c "hk -n vc-${VC} get ingress -l vk8s.io/vname=web -o name | grep -q ." \
  || fail "合规 Ingress 未同步到宿主"
retry 30 2 bash -c "curl -sS --max-time 5 -H 'Host: ${good_host}' http://127.0.0.1/ | grep -qi html" \
  || fail "经 ingress-nginx 访问 ${good_host} 失败"
ok "合规 host 同步并可访问"

log "不合规 Ingress host 应被拒绝"
vk -n demo create ingress evil --class=nginx --rule="evil.example.com/*=web:80"
retry 30 2 bash -c "vk -n demo get events --field-selector involvedObject.name=evil,reason=SyncRejected -o name | grep -q ." \
  || fail "未产生 SyncRejected 事件"
if hk -n "vc-${VC}" get ingress -l vk8s.io/vname=evil -o name | grep -q .; then
  fail "不合规 Ingress 被同步到宿主"
fi
ok "不合规 host 被拒绝"

log "控制平面重启后数据仍在（sqlite 落 PVC）"
hk -n "vc-${VC}" delete pod "vc-${VC}-0" --wait=true --timeout=120s >/dev/null
hk -n "vc-${VC}" rollout status "sts/vc-${VC}" --timeout=300s >/dev/null || fail "控制平面 Pod 重启后未就绪"
retry 60 2 bash -c "vk get --raw /readyz >/dev/null 2>&1" || fail "重启后虚拟 apiserver 不可达"
vk -n demo get deployment api >/dev/null || fail "重启后 Deployment api 丢失"
vk -n demo get ingress web >/dev/null || fail "重启后 Ingress 丢失"
retry 60 2 bash -c "vk -n demo get pods -l app=api --field-selector=status.phase=Running -o name | grep -c pod/ | grep -qx 2" \
  || fail "重启后 Deployment 副本未恢复为 2"
ok "控制平面重启持久化正常"

# ---------- 11. 清理集群 ----------
log "删除集群 ${VC}"
admin_login
api DELETE "/api/v1/vclusters/${VC}" >/dev/null
retry 60 5 bash -c "! hk get vc ${VC} >/dev/null 2>&1" || fail "VirtualCluster 未消失"
retry 60 5 bash -c "! hk get ns vc-${VC} >/dev/null 2>&1" || fail "宿主 namespace vc-${VC} 未消失"
if hk get accesstokens -o jsonpath='{.items[*].spec.vcluster}' | grep -qw "$VC"; then
  fail "AccessToken 未随集群级联删除"
fi
hk delete user "$E2E_USER2" --ignore-not-found >/dev/null
ok "集群删除级联完成"

log "全部通过"
