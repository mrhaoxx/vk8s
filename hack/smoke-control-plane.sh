#!/usr/bin/env bash
# 设计文档第 13 节验证 1：kine(sqlite) + kube-apiserver + kube-controller-manager 在同一进程内启动，
# 并用 kubectl 做最小冒烟（namespace / default SA / Deployment→ReplicaSet→Pod / 优雅退出）。
#
# 只在本机运行，apiserver 只监听 127.0.0.1:6443，kcm 监听 127.0.0.1:10257。需要 go、kubectl、curl。
# 用法：hack/smoke-control-plane.sh            （SMOKE_DIR 默认 <repo>/.smoke，已在 .gitignore）
#       KEEP=1 hack/smoke-control-plane.sh     跑完不杀进程，便于手动 kubectl 观察
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
# unix socket 路径有 104 字节上限，工作目录不能太深。
WORK=${SMOKE_DIR:-$ROOT/.smoke}
VC=${VC:-dev}
SERVICE_CIDR=${SERVICE_CIDR:-10.96.0.0/12}
READY_TIMEOUT=${READY_TIMEOUT:-180}

log() { printf '\n==> %s\n' "$*"; }
fail() {
  printf '\nFAIL: %s\n' "$*"
  if [[ -s "$WORK/probe.log" ]]; then echo "--- egressprobe.log (tail)"; tail -30 "$WORK/probe.log"; fi
  echo "--- cp.log (tail)"; tail -60 "$WORK/cp.log" 2>/dev/null || true
  exit 1
}

log "build bin/vk8s（CGO_ENABLED=1，kine 的 sqlite 需要 cgo）"
K8S_VERSION=${K8S_VERSION:-v1.36.2}
(cd "$ROOT" && CGO_ENABLED=1 CGO_CFLAGS="-DSQLITE_ENABLE_DBSTAT_VTAB=1 -DSQLITE_USE_ALLOCA=1" go build \
  -ldflags "-X k8s.io/component-base/version.gitVersion=$K8S_VERSION -X k8s.io/component-base/version.gitMajor=1 -X k8s.io/component-base/version.gitMinor=$(echo "$K8S_VERSION" | cut -d. -f2)" \
  -o bin/vk8s ./cmd/vk8s)
(cd "$ROOT" && go build -o bin/egressprobe ./hack/egressprobe)

log "generate local certs → $WORK/certs"
rm -rf "$WORK"
mkdir -p "$WORK"
(cd "$ROOT" && go run ./hack/localcerts -out "$WORK/certs" -vcluster "$VC")

log "start control-plane"
POD_IP=127.0.0.1 "$ROOT/bin/vk8s" control-plane \
  --vcluster-name="$VC" \
  --bind-address=127.0.0.1 \
  --certs-dir="$WORK/certs" --data-dir="$WORK/data" --run-dir="$WORK/run" \
  --service-cluster-ip-range="$SERVICE_CIDR" \
  --logging-format=text --component-verbosity=2 \
  >"$WORK/cp.log" 2>&1 &
CP_PID=$!
cleanup() {
  if [[ "${KEEP:-0}" == 1 ]]; then
    echo "KEEP=1: control-plane 继续运行，pid=${CP_PID}，KUBECONFIG=$WORK/certs/admin.kubeconfig，日志 $WORK/cp.log"
    return
  fi
  if [[ -n "${PROBE_PID:-}" ]]; then kill "$PROBE_PID" 2>/dev/null || true; fi
  kill "$CP_PID" 2>/dev/null || true
  wait "$CP_PID" 2>/dev/null || true
}
trap cleanup EXIT

log "wait for apiserver /readyz（最多 ${READY_TIMEOUT}s）"
for ((i = 0; i < READY_TIMEOUT; i++)); do
  if curl -fsS --cacert "$WORK/certs/platform-ca.crt" https://127.0.0.1:6443/readyz >/dev/null 2>&1; then
    break
  fi
  kill -0 "$CP_PID" 2>/dev/null || fail "control-plane exited during startup"
  sleep 1
done
curl -fsS --cacert "$WORK/certs/platform-ca.crt" https://127.0.0.1:6443/readyz >/dev/null || fail "apiserver not ready in ${READY_TIMEOUT}s"
echo "apiserver ready"

export KUBECONFIG="$WORK/certs/admin.kubeconfig"

log "wait for kube-controller-manager /healthz"
for ((i = 0; i < 120; i++)); do
  if curl -fsk https://127.0.0.1:10257/healthz >/dev/null 2>&1; then break; fi
  kill -0 "$CP_PID" 2>/dev/null || fail "control-plane exited after apiserver became ready"
  sleep 1
done
curl -fsk https://127.0.0.1:10257/healthz >/dev/null || fail "kcm not healthy"
echo "kcm healthy"

log "kubectl smoke"
kubectl version
kubectl get --raw /readyz?verbose | tail -3
kubectl get ns
kubectl get svc -A
kubectl create ns smoke

echo "- default ServiceAccount should be created by kcm"
for ((i = 0; i < 60; i++)); do
  kubectl -n smoke get sa default >/dev/null 2>&1 && break
  sleep 1
done
kubectl -n smoke get sa default || fail "kcm did not create default SA"

echo "- Deployment → ReplicaSet → Pod（无节点，Pod 保持 Pending 即为正常）"
kubectl -n smoke create deployment smoke --image=registry.k8s.io/pause:3.10 --replicas=2
for ((i = 0; i < 60; i++)); do
  n=$(kubectl -n smoke get pods --no-headers 2>/dev/null | wc -l | tr -d ' ')
  [[ "$n" == 2 ]] && break
  sleep 1
done
kubectl -n smoke get deploy,rs,pods
[[ "$(kubectl -n smoke get pods --no-headers | wc -l | tr -d ' ')" == 2 ]] || fail "replicaset controller did not create 2 pods"

echo "- kube-root-ca.crt 注入（root-ca-publisher 控制器）"
kubectl -n smoke get cm kube-root-ca.crt >/dev/null || fail "kube-root-ca.crt missing"

echo "- egress selector 配置已写入 run-dir"
test -s "$WORK/run/egress-selector.yaml" || fail "egress-selector.yaml not written"

echo "- sqlite 数据文件"
ls -la "$WORK/data/db/"

log "验证 3a：default/kubernetes 删除后以指定 ClusterIP 重建，apiserver 的 kubernetes-service 控制器（10s 周期）不改回"
ALIGN_IP=${ALIGN_IP:-10.96.77.1}
cat >"$WORK/kubernetes-svc.yaml" <<YAML
apiVersion: v1
kind: Service
metadata:
  name: kubernetes
  namespace: default
  labels: {component: apiserver, provider: kubernetes}
spec:
  type: ClusterIP
  clusterIP: $ALIGN_IP
  clusterIPs: [$ALIGN_IP]
  sessionAffinity: None
  ports:
  - {name: https, protocol: TCP, port: 443, targetPort: 6443}
YAML
aligned=0
for attempt in 1 2 3 4 5; do
  kubectl -n default delete svc kubernetes --ignore-not-found --wait=true >/dev/null
  if kubectl create -f "$WORK/kubernetes-svc.yaml" >/dev/null 2>"$WORK/svc-create.err"; then
    aligned=1
    break
  fi
  grep -q AlreadyExists "$WORK/svc-create.err" || { cat "$WORK/svc-create.err"; fail "create default/kubernetes with clusterIP $ALIGN_IP rejected"; }
  echo "  被上游控制器抢先重建，重试 $attempt"
done
[[ $aligned == 1 ]] || fail "could not recreate default/kubernetes"
echo "  recreated: clusterIP=$(kubectl -n default get svc kubernetes -o jsonpath='{.spec.clusterIP}')"
echo "  等 25s（覆盖上游控制器两个 10s 周期）..."
sleep 25
got=$(kubectl -n default get svc kubernetes -o jsonpath='{.spec.clusterIP}')
[[ "$got" == "$ALIGN_IP" ]] || fail "default/kubernetes clusterIP reverted to $got"
ports=$(kubectl -n default get svc kubernetes -o jsonpath='{.spec.ports[0].port}:{.spec.ports[0].targetPort}')
[[ "$ports" == "443:6443" ]] || fail "default/kubernetes ports changed: $ports"
echo "  OK: clusterIP 保持 ${ALIGN_IP}，ports 443→6443 未变"

log "验证 3b：创建 Service 时预置 clusterIP（等价于 mutating webhook 落库前写入）被 IP 分配器接受，重复 IP 被拒绝"
cat >"$WORK/preset-svc.yaml" <<YAML
apiVersion: v1
kind: Service
metadata: {name: preset, namespace: smoke}
spec:
  clusterIP: 10.96.88.8
  ports: [{port: 80}]
YAML
sed 's/name: preset/name: dup/' "$WORK/preset-svc.yaml" >"$WORK/dup-svc.yaml"
kubectl create -f "$WORK/preset-svc.yaml" >/dev/null
got=$(kubectl -n smoke get svc preset -o jsonpath='{.spec.clusterIP}')
[[ "$got" == 10.96.88.8 ]] || fail "preset clusterIP not honoured: $got"
if kubectl create -f "$WORK/dup-svc.yaml" >/dev/null 2>"$WORK/dup.err"; then
  fail "duplicate clusterIP accepted"
fi
grep -q 'already allocated' "$WORK/dup.err" || { cat "$WORK/dup.err"; fail "duplicate clusterIP rejected for an unexpected reason"; }
echo "  OK: 预置 IP 被接受；重复 IP 被拒绝：$(tr -d '\n' <"$WORK/dup.err" | cut -c1-160)"

log "验证 2：egress selector → HTTPConnect over UDS → syncer ConnectServer 端口分流（桩 kubelet / 桩 metrics）"
"$ROOT/bin/egressprobe" -socket "$WORK/run/konnectivity.sock" -certs-dir "$WORK/certs" -node probe-node >"$WORK/probe.log" 2>&1 &
PROBE_PID=$!
for ((i = 0; i < 40; i++)); do
  [[ -S "$WORK/run/konnectivity.sock" ]] && break
  kill -0 "$PROBE_PID" 2>/dev/null || fail "egressprobe exited"
  sleep 0.25
done
[[ -S "$WORK/run/konnectivity.sock" ]] || fail "egressprobe did not create $WORK/run/konnectivity.sock"

echo "- 2a kubectl logs：Node(InternalIP 10.200.0.1) + 已绑定 Pod → apiserver 经隧道 CONNECT 10.200.0.1:10250"
cat >"$WORK/probe-node.yaml" <<YAML
apiVersion: v1
kind: Node
metadata: {name: probe-node}
YAML
kubectl create -f "$WORK/probe-node.yaml" >/dev/null
kubectl patch node probe-node --subresource=status --type=merge \
  -p '{"status":{"addresses":[{"type":"InternalIP","address":"10.200.0.1"}],"daemonEndpoints":{"kubeletEndpoint":{"Port":10250}},"conditions":[{"type":"Ready","status":"True","reason":"Probe","message":"egress probe"}]}}' >/dev/null
cat >"$WORK/probe-pod.yaml" <<YAML
apiVersion: v1
kind: Pod
metadata: {name: probe, namespace: smoke}
spec:
  nodeName: probe-node
  containers: [{name: c, image: registry.k8s.io/pause:3.10}]
YAML
kubectl create -f "$WORK/probe-pod.yaml" >/dev/null
logs=$(kubectl get --raw "/api/v1/namespaces/smoke/pods/probe/log?container=c" 2>"$WORK/logs.err") || { cat "$WORK/logs.err"; fail "pods/log through egress tunnel failed"; }
[[ "$logs" == *"egress-probe: logs for smoke/probe/c"* ]] || fail "unexpected log body: $logs"
echo "  OK: $logs"

echo "- 2b 聚合层：APIService v1beta1.metrics.k8s.io → Service kube-system/metrics-server → EndpointSlice 10.200.0.2:8443 → 经隧道"
cat >"$WORK/probe-metrics.yaml" <<YAML
apiVersion: v1
kind: Service
metadata: {name: metrics-server, namespace: kube-system}
spec:
  ports: [{name: https, port: 443, targetPort: 8443, protocol: TCP}]
---
apiVersion: discovery.k8s.io/v1
kind: EndpointSlice
metadata:
  name: metrics-server-probe
  namespace: kube-system
  labels: {kubernetes.io/service-name: metrics-server, endpointslice.kubernetes.io/managed-by: egress-probe}
addressType: IPv4
endpoints: [{addresses: ["10.200.0.2"], conditions: {ready: true}}]
ports: [{name: https, port: 8443, protocol: TCP}]
---
apiVersion: apiregistration.k8s.io/v1
kind: APIService
metadata: {name: v1beta1.metrics.k8s.io}
spec:
  group: metrics.k8s.io
  version: v1beta1
  groupPriorityMinimum: 100
  versionPriority: 100
  insecureSkipTLSVerify: true
  service: {name: metrics-server, namespace: kube-system, port: 443}
YAML
kubectl create -f "$WORK/probe-metrics.yaml" >/dev/null
for ((i = 0; i < 60; i++)); do
  avail=$(kubectl get apiservice v1beta1.metrics.k8s.io -o jsonpath='{.status.conditions[?(@.type=="Available")].status}' 2>/dev/null || true)
  [[ "$avail" == True ]] && break
  sleep 1
done
if [[ "$avail" != True ]]; then
  kubectl get apiservice v1beta1.metrics.k8s.io -o jsonpath='{.status.conditions[?(@.type=="Available")].message}{"\n"}'
  fail "APIService v1beta1.metrics.k8s.io not Available in 60s"
fi
echo "  APIService Available"
nodes=$(kubectl get --raw /apis/metrics.k8s.io/v1beta1/nodes) || fail "GET /apis/metrics.k8s.io/v1beta1/nodes failed"
[[ "$nodes" == *'"name":"probe-node"'* ]] || fail "unexpected NodeMetricsList: $nodes"
kubectl top node || fail "kubectl top node failed"
echo "  OK: 聚合层经隧道到桩 metrics，kubectl top 可用"
kill "$PROBE_PID" 2>/dev/null || true
wait "$PROBE_PID" 2>/dev/null || true
PROBE_PID=""

if [[ "${KEEP:-0}" != 1 ]]; then
  log "SIGTERM → 优雅退出（期望 exit 0）"
  t0=$SECONDS
  kill -TERM "$CP_PID"
  if wait "$CP_PID"; then
    echo "control-plane exited 0 in $((SECONDS - t0))s"
  else
    rc=$?
    fail "control-plane exited with $rc on SIGTERM"
  fi
  trap - EXIT
fi

log "SMOKE OK"
