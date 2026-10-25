# vk8s — 多用户隔离的虚拟 Kubernetes 开发环境

vk8s 让一个共享的宿主 Kubernetes 集群（GPU/RDMA 等高性能节点）为 20–200 名开发者各自提供一个
「看起来像独享集群」的虚拟 Kubernetes：每个人拿到自己的 kubeconfig，是虚拟集群的 cluster-admin，
可以自由建 namespace / CRD / RBAC / Deployment，`kubectl` 全部命令可用（含 `logs` / `exec` /
`port-forward` / `top`）；而实际工作负载由宿主集群调度运行，并被限制在管理员指定的节点池和配额内（超配额在创建时即 403）。

## 架构

一个 Go module、一个二进制 `vk8s`、一个容器镜像、四个子命令：

| 子命令 | 部署位置 | 职责 |
|---|---|---|
| `vk8s manager` | `vk8s-system`，Deployment | Web UI、REST API、登录（OIDC / 静态账号）、签发 kubectl token、TokenReview webhook、CRD reconciler（operator）、平台 CA |
| `vk8s gateway` | `vk8s-system`，Deployment ×2，Service LoadBalancer | L4 SNI 透传：`<vc>.<apiDomain>:443` → `vc-<vc>.vc-<vc>.svc:443`；不终结 TLS |
| `vk8s control-plane` | 每个虚拟集群的 StatefulSet pod，容器 1 | 同一进程内依次启动 kine(sqlite on PVC) → kube-apiserver → kube-controller-manager |
| `vk8s syncer` | 同一 pod，容器 2 | 虚拟 ↔ 宿主 资源同步、假 kubelet、`metrics.k8s.io`、Service ClusterIP 对齐 webhook、CoreDNS 投放 |

```
浏览器 ──HTTPS──▶ vk8s manager ─────────────── 写 CRD / 签 token / 平台 CA
                      ▲ TokenReview webhook（mTLS）
kubectl ──TLS SNI──▶ vk8s gateway ──┬─▶ ns vc-alice-dev  [control-plane | syncer] ⇄ alice 的 Pod/Svc/Ingress/PVC
                                    └─▶ ns vc-team-x     [control-plane | syncer] ⇄ team-x 的 ...
                                        宿主集群，每个虚拟集群严格一个 namespace
```

要点（详见设计文档）：

- **隔离模型**：每个虚拟集群有独立的 kube-apiserver 与 sqlite，租户看不到他人任何对象；只有 Pod 及其依赖
  （ConfigMap / Secret / PVC / Service / Ingress）下沉到宿主 `vc-<name>` namespace。
- **认证**：虚拟集群只认平台 token。apiserver 通过 TokenReview webhook 回调 manager，权限按当时的
  `VirtualCluster.spec`（owner / members / 平台 admin）实时解析，改成员或撤销 token 最多 2 分钟生效。
- **PKI**：平台 CA 签所有虚拟 apiserver 的服务端证书（所有 kubeconfig 共用一个 CA）；每集群内部 CA 签
  kcm / syncer / kubelet 客户端证书。任何 CA 私钥都不进入租户 namespace。
- **调度**：管理员为每个集群指定 `nodePlacement`（nodeSelector / nodeNames / tolerations），由 syncer 在翻译
  Pod 时强制注入，用户在虚拟集群内无法放宽。节点被移出（收缩 nodePlacement、节点标签变化、节点被删）时，
  其上已运行的虚拟 Pod 被 syncer 删除（gracePeriod 上限 30 秒，不看 PDB，Event `EvictedByNodePlacement`），
  由虚拟集群里的控制器按新约束重建。
- **节点预约**：管理员在「节点」页把节点标记为可预约（给宿主 Node 打 `vk8s.io/bookable=true`，不打 taint），
  该节点在 vk8s 内只能通过预约使用（默认池与静态放置的集群都调度不上去，已在其上的租户 Pod 被驱逐；宿主上非 vk8s 的
  工作负载不受影响）。集群所有者与成员按小时为自己的集群预约，同一节点同一时段只有一个预约，不限长度与提前量；
  到点由 manager 里的控制器写进 `VirtualCluster.status.leasedNodes`，syncer 据此放行并镜像节点，到期或删除预约后
  节点移出、Pod 按上面的驱逐规则清理；摘掉标签会撤销该节点全部租约。
- **审计**：开启 `audit.enabled` 后，每个虚拟 apiserver 以 batch 模式把审计事件回调 manager（复用 TokenReview 的 mTLS 通道，集群名取自证书），
  manager 连同平台管理接口的全部变更操作与登录一起批量写进 ClickHouse（HTTP 接口，默认保留 365 天）。策略：用户的读记元数据、变更记请求体
  （Secret / ConfigMap 不记体），kcm / syncer 等系统组件的只读请求与 kube-system 里的 Lease 续期不记。管理员在「审计」页按集群 / 用户 / 动词 / 资源 / 时间查询，
  集群所有者与成员在集群详情的「审计」Tab 看自己的集群。ClickHouse 不可达不影响 manager 就绪，事件先留在内存队列（manager 重启会丢队列里的）。
- **应用目录**：管理员维护集群级 `AddonTemplate`（kubectl 或 chart values 的 `addons.catalog`），用户在建集群时勾选预装，
  或在集群「应用」Tab 安装 / 卸载 / 重试 / 升级，装好后看到访问地址（密码类按需显示）。安装由 vk8s-system 里跑平台镜像
  （内置 helm）的 Job 完成；每次安装是一个 `AddonInstall`（`kubectl get addon`），集群删除时级联卸载。
- **安全档位**：`podSecurity: privileged | baseline | restricted` 写成宿主 ns 的 PSA 标签；本部署默认 `privileged`
  —— 此时 nodePlacement 是唯一硬隔离边界，互不信任的租户不要共享节点。

## 仓库结构

```
cmd/vk8s/                 main：子命令 manager / gateway / control-plane / syncer
api/v1alpha1/             User、VirtualCluster、AccessToken 类型
internal/manager/         HTTP API、认证、token、TokenReview、UI embed
internal/controllers/     VirtualCluster reconciler、AccessToken GC
internal/pki/             两层 CA 与叶子证书
internal/controlplane/    kine + kube-apiserver + kube-controller-manager 进程内组装
internal/syncer/          资源同步、假 kubelet、metrics.k8s.io、Service webhook
internal/gateway/         SNI 网关
internal/names/           两侧共用的名字、标签、路径、端口
internal/config/          平台配置文件结构
web/                      React SPA
deploy/helm/vk8s/         Helm chart（CRD 由 make crds 生成）
hack/                     staging replace 生成脚本、kind e2e 脚本
```

## 构建

前置：Go 1.26、Node 22、Docker、C 编译器（kine 的 sqlite 驱动是 cgo）。

```bash
make build        # 构建 web 并编译 bin/vk8s（CGO_ENABLED=1）
make image        # 构建容器镜像 $(IMAGE):$(TAG)，默认 registry.corp.com/vk8s/vk8s:dev（容器内下载模块）
make image-local  # 同上，但在 golang 容器里只读挂载宿主 module cache 离线编译，再用 Dockerfile.local 打包
make crds         # 用 controller-gen 从 api/ 重新生成 deploy/helm/vk8s/crds
make tidy         # go mod tidy
make update-k8s K8S_VERSION=v1.36.x   # 重新生成 go.mod 中 k8s staging 模块的 replace
```

国内网络下容器内 `go mod download` 对 goproxy.cn 经常断流，本机开发请用 `make image-local`
（go build cache 放在 docker volume `vk8s-gocache`，增量构建约 1 分钟）。

`k8s.io/kubernetes` 以库形式引入，go.mod 中对 33 个 staging 模块写了 `replace`，版本锁定 k8s 1.36.2。
二进制约 175MB，manager / gateway / control-plane / syncer 使用同一个镜像。

必须 `CGO_ENABLED=1`：kine 在无 cgo 时只注册一个返回 "sqlite is disabled" 的桩驱动。镜像因此用
`golang:1.26-bookworm` 构建、`distroless/base-debian12`（带 glibc）运行。

## 安装

前置：宿主集群已有 StorageClass、Ingress Controller（默认按 ingress-nginx 配置）、LoadBalancer 实现；
`kubectl top` 需要宿主安装 metrics-server。

```bash
helm install vk8s deploy/helm/vk8s \
  --namespace vk8s-system --create-namespace \
  --set image.repository=registry.corp.com/vk8s/vk8s --set image.tag=v0.1.0 \
  --set apiDomain=vk8s.corp.com \
  --set appsDomain=apps.vk8s.corp.com \
  --set hostServiceCIDR=10.96.0.0/12 \
  --set hostPodCIDR=10.244.0.0/16 \
  --set manager.ingress.tls.secretName=vk8s-console-tls
```

然后按 `helm install` 输出的 NOTES 配置 DNS：

| 记录 | 指向 |
|---|---|
| `*.<apiDomain>` | `vk8s-gateway` Service 的 LoadBalancer 地址（kubectl 流量） |
| `console.<apiDomain>` | 宿主 Ingress Controller 的 LoadBalancer 地址（Web UI / API） |
| `*.<appsDomain>` | 宿主 Ingress Controller 的 LoadBalancer 地址（用户工作负载） |

常用 values（完整列表见 `deploy/helm/vk8s/values.yaml`）：

| 键 | 说明 |
|---|---|
| `auth.bootstrapAdmin.*` | 首次启动创建的静态管理员；密码留空则随机生成到 Secret `bootstrap-admin` |
| `auth.oidc.*` | OIDC 登录（issuer、clientID、clientSecret / existingSecret、autoCreateUsers、allowedEmailDomains） |
| `defaults.*` / `limits.*` | 用户建集群时的默认配额、安全档位、节点约束、上限 |
| `controlPlanePlacement` | 控制平面 pod 自身落在哪些节点 |
| `nodePlacementPresets` | UI 下拉中的节点池预设 |
| `appsTLS.*` | `*.<appsDomain>` 泛证书 Secret，用户 Ingress 自动获得 TLS |
| `gateway.service.*` | 网关 Service 类型 / 注解 / loadBalancerIP |

**CRD 升级**：`crds/` 下的 CRD 只在 `helm install` 时安装，`helm upgrade` 不会更新。升级前先执行
`kubectl apply -f deploy/helm/vk8s/crds/`。

## 首次登录

1. 取 bootstrap 管理员密码：

   ```bash
   kubectl -n vk8s-system get secret bootstrap-admin -o jsonpath='{.data.password}' | base64 -d; echo
   ```

2. 浏览器打开 `https://console.<apiDomain>`，用 `admin` + 上述密码登录。
3. 「管理员 → 用户」创建用户（静态账号）或配置 OIDC 后让用户自行登录；OIDC 用户由管理员在 UI 中提升为 admin。
4. 「管理员 → 所有集群」可修改每个集群的配额、节点池、安全档位。选择 `privileged` 档位时会提示
   节点池是唯一隔离边界。

## 下载 kubeconfig

1. 登录后在「我的集群」新建集群（名字为 DNS label，≤30 字符，不含 `--`），等待状态变为 `Running`。
2. 进入集群详情，点「下载 kubeconfig」，填写描述与有效期（默认 30 天，上限 90 天）。
   明文 token 只在这一次响应中出现；kubeconfig 的 `server` 为 `https://<vc>.<apiDomain>`（`apiPort` 非 443 时带端口，
   网关只能走 NodePort 的集群要设它），CA 为平台 CA。
3. 使用：

   ```bash
   export KUBECONFIG=~/Downloads/<vc>.kubeconfig
   kubectl get nodes -o wide          # 看到的是管理员允许的真实节点
   kubectl create deployment web --image=nginx
   kubectl expose deployment web --port=80
   kubectl exec / logs / port-forward / top ...
   ```

4. 在集群详情的 token 列表中可随时撤销 token；撤销、改成员、禁用用户最多 2 分钟后生效，无需重发 kubeconfig。
5. 用户 Ingress 的 host 必须是 `<vc>.<appsDomain>` 或 `<label>--<vc>.<appsDomain>`，其他 host 会被拒绝并写
   Event `SyncRejected`。

命令行同样可以观察平台状态：

```bash
kubectl get users
kubectl get virtualclusters          # 简写 vc；列出 Owner / Phase / Endpoint
kubectl get accesstokens
kubectl -n vc-<name> get pods        # 控制平面 pod 与该集群的全部工作负载
```

## 开发与测试

- `make test`：单元测试。翻译函数（命名截断、Pod 翻译各步、nodePlacement 合并、podAffinity 限定 namespace、
  Ingress host 校验、RuntimeClass / LimitRange 镜像）、认证（bcrypt、会话 JWT、登录限速）、TokenReview 判定
  （fake client）、egress 隧道转发。不含 envtest。
- `make test-web`：前端单元测试（Vitest）：主题 Provider、Tab 与 URL 同步、状态点映射、侧栏权限分组。
  `cd web && npm run mock` 加 `npm run dev` 可不依赖集群运行控制台（见 `web/README.md`）。
- `hack/kind-e2e.sh`（`make e2e`）：在 kind 上部署平台并跑完整 e2e（ingress-nginx、metrics-server、建用户与集群、
  kubectl run/expose/exec/logs/top/debug/port-forward、LimitRange 与 RuntimeClass 镜像、podAffinity 限定、
  Ingress host 校验、nodePlacement 收缩后存量 Pod 被驱逐重建与 nodeName 白名单、节点预约（标记、租约生效、删除回收、摘标签恢复）、审计（集群内 exec、平台建集群、失败登录、租户权限；`SKIP_AUDIT=1` 跳过）、podSecurity 档位切换、成员加入/移除后 token 生效/失效、
  控制平面重启持久化、清理）。脚本内有参数说明；国内网络建议 `BUILD_TARGET=image-local`，排错时 `KEEP=1`，
  复用已建好的集群用 `SKIP_CREATE=1 SKIP_BUILD=1`（约 10 分钟，其中成员移除后等 TokenReview cache 过期占 2 分钟）。
- `hack/smoke-control-plane.sh`（`make smoke`）：设计文档第 13 节验证 1 与验证 3 的本机部分。以
  `--bind-address=127.0.0.1` 启动进程内 kine + apiserver + kcm，用 `hack/localcerts` 生成的证书跑 kubectl 冒烟
  （ns、默认 SA、Deployment→RS→Pod、kube-root-ca.crt），再验证 `default/kubernetes` 删除后以指定 ClusterIP
  重建不被上游控制器改回、创建 Service 时预置 clusterIP 被分配器接受且重复 IP 被拒绝，最后 SIGTERM 优雅退出。
  不需要 Docker，约 1 分钟。
- 单元测试、envtest 与进程内控制平面冒烟测试的策略见设计文档第 12 节。

## 已知状态

**2026-10-19：`hack/kind-e2e.sh` 在本机 kind（k8s v1.36.1，3 节点）上全程通过**：Helm 安装 → manager 登录、建用户、
建集群（约 20s 到 Running）→ 签发 kubeconfig → 经 SNI 网关 kubectl → run / expose / `services/proxy` / exec / logs / 集群内 DNS →
`kubectl top` → Deployment → Ingress 合规同步与不合规拒绝 → 级联删除无残留；Web UI 经 Ingress 可达。
`go build ./...`、`go vet ./...`、`hack/smoke-control-plane.sh` 也全部通过。**还没有在真实宿主集群上部署过**；
OIDC 登录、非 privileged 档位的 NetworkPolicy 实际拦截（kindnet 不执行 NetworkPolicy）、多副本 manager、Web UI 交互都还没有测过。

**2026-10-20：对照设计文档审一遍后补的四处 syncer 行为**（kind e2e 已覆盖）：RuntimeClass 只读镜像（否则
`runtimeClassName: nvidia` 被虚拟 apiserver 拒绝）；宿主 ns 的 LimitRange 镜像进每个虚拟 namespace（否则虚拟 Pod 没有
requests，HPA 报 `missing request for cpu`，两侧 qosClass 不同）；`kubectl debug` 的 ephemeral 容器经宿主
`pods/ephemeralcontainers` 下沉；podAffinity / topologySpread 的 labelSelector 追加 `vk8s.io/vnamespace`（所有虚拟
namespace 在宿主同一 ns，否则跨 namespace 误匹配）。同时补了单元测试（`make test`），chart 在 `manager.replicas > 1`
时自动加 `--leader-elect`。v1 明确接受的边界（控制平面节点与租户节点池必须互斥、privileged 下跨租户网络互通、PDB 不下沉、
配额对租户不可见、Ingress class / annotation 透传、无平台级 imagePullSecret、平台 CA 不轮换、sqlite 无备份）列在设计文档第 10 节。

**2026-10-23：节点被移出时驱逐存量 Pod。** 原先设计文档写的是「收缩 nodePlacement 不迁移存量 Pod」，实际上虚拟 kcm 的
podgc 会在 Node 镜像消失约 40 秒后把这些 Pod 标成 Failed 并以 grace 0 强删，宿主侧却按 Pod 自己的 terminationGracePeriodSeconds
等待，既没有上限也没有可读的原因。现在 syncer 在节点变为不允许时立即删除其上的虚拟 Pod（Node 镜像控制器按 `spec.nodeName`
索引批量驱逐；Pod 同步器对 placement 变化后才被调度到被禁节点的 Pod 也驱逐），gracePeriod 取 Pod 自身值与 30 秒的较小者，
写 Event `EvictedByNodePlacement`，指标 `vk8s_syncer_placement_evictions_total`。单测与 kind e2e 已覆盖（e2e 用一个忽略
SIGTERM、grace 1 小时的 Pod 验证上限生效）。podgc 仍作为兜底保留。

kind e2e 首轮修掉的问题（都在 syncer）：CoreDNS 清单需显式 `runAsUser`（镜像 USER 是非数字的 nonroot）；宿主 kcm 会把
Service 的 label 原样复制到它建的 Endpoints / EndpointSlice 上（含 `vk8s.io/managed`），syncer 曾把它们当孤儿反复删除，
现按 `endpoints.kubernetes.io/managed-by` 与 `endpointslice.kubernetes.io/managed-by` 排除；Pod status 回写保留虚拟侧
`qosClass`（宿主 LimitRange 补默认值后两侧 QoS 不同，照抄被拒）；egress 隧道原先只放行 10250 与 8443，service 形式的
admission / conversion webhook、用户的聚合 APIService、`pods/services proxy` 都会被 502，现在其余目标由 syncer 直连转发。

实施计划中列为必须先行验证的三条假设全部成立：

1. `k8s.io/kubernetes` 作为库能编过；apiserver + kcm + kine 能在同一进程启动并通过冒烟测试
   （`internal/controlplane`）。**2026-10-18 已通过** `hack/smoke-control-plane.sh`。过程中修掉的坑：
   kine 以库调用时必须显式填全部 CLI 默认值（否则 Watch panic / 启动被拒）；kine 需 ≥0.16 才兼容 1.34+ 的
   compactor；sqlite 需编译期开 DBSTAT；apiserver 需 `--shutdown-watch-termination-grace-period` 否则
   SIGTERM 后卡满 `--request-timeout`。
2. apiserver egress selector 以 HTTPConnect over UDS 到 syncer，`kubectl exec` 与聚合层到 metrics 接口都能通。
   **已通过本机验证**（`hack/egressprobe` 复用 syncer 的 `kubelet.ConnectServer`）：`pods/log` 经隧道到
   10250 并携带 kubelet 客户端证书；APIService → Service → EndpointSlice 经隧道到 8443，APIService
   Available，`kubectl top node` 可用。exec 的 upgrade 流程与假 kubelet 已在 kind e2e 中通过（exec、logs、attach 回退、top node/pod）。
   发现并修复：kube-aggregator ≥1.33 只用 EndpointSlice 解析后端，syncer 改为直接维护 EndpointSlice。
3. `default/kubernetes` 删除重建为宿主 IP 后，上游 kubernetes-service 控制器不改回；Service 创建时的
   mutating webhook 能在落库前写入 clusterIP 且不被 apiserver 的 IP 分配器拒绝。**apiserver 侧已通过本机
   验证**（3a：重建后 25s 内 IP 与端口不变；3b：预置 IP 被接受、重复 IP 被拒绝）；webhook 本身已在 kind e2e 中通过（kube-dns 与用户
   Service 两侧 ClusterIP 一致，`default/kubernetes` 等于宿主 vc-<name> 的 ClusterIP）。

**2026-10-24：节点预约。** 新 CRD `NodeBooking`（`kubectl get nb`）与 manager 里的 `NodeBookingReconciler`：按时刻推进
Pending / Active / Completed，重叠时创建早的胜出（后者 Rejected），标签被摘、节点或目标集群消失时撤销（Revoked），
终态保留 `booking.retention`（默认 168h）后删除。syncer 的节点判定改为「静态规则允许 或 在租约内；带 bookable 标签的节点只认租约」，
Pod 注入在静态 term 上加 `vk8s.io/bookable NotIn [true]`、为每个租约节点追加一条 OR term。控制台新增「节点预约」时间轴页
（拖选创建、点色块看详情或删除）、节点页的可预约开关（切换前显示将被驱逐的 Pod 或将被撤销的租约）、集群概览的「预约节点」行。
单测覆盖判定与注入、控制器 phase / 仲裁 / 租约同步 / finalizer / GC、API 权限矩阵与校验、前端时间轴几何与拖选；
`hack/kind-e2e.sh` 已加一段完整流程，e2e 待 Docker 可用后运行。
**升级到这个版本必须先 apply 新的 CRD**（见上文「安装」一节）：缺 `nodebookings` CRD 时 manager 的 informer 起不来；
`virtualclusters` CRD 没更新时 apiserver 会把 `status.leasedNodes` 整段剪掉，预约显示 Active 但 syncer 永远收不到租约。

**2026-10-24：审计。** 新包 `internal/audit`：事件行、有界批量管线、ClickHouse HTTP sink（JSONEachRow 插入、类型化参数查询）、内嵌审计策略
（用 apiserver 自己的策略评估器做了单测）、平台操作记录器（17 个路由的动作表、请求体脱敏、登录体不记）。manager 新增 mTLS 端点 `/webhook/audit/<vc>`
与查询接口 `GET /api/v1/audit/events[/{id}]`；控制平面按 `--audit-webhook-kubeconfig` 给 apiserver 加审计参数并写策略文件；证书 Secret 多一个
`audit-webhook.kubeconfig`（开关审计时各控制平面滚动重启一次）。chart 可选部署单副本 ClickHouse（`audit.clickhouse.deploy=true`，镜像约 1 GB）。
控制台新增「审计」页与集群「审计」Tab（过滤同步 URL、游标翻页、右侧抽屉看详情与请求体）。kube 事件带访问令牌 ID（TokenReview 的 `user.extra`），抽屉里可按令牌过滤，令牌页有「审计」入口。e2e 默认开启审计段落，待 Docker 可用后运行。
已知取舍：manager 无磁盘缓冲，重启丢内存队列（最多 5 万条，有 `vk8s_audit_dropped_total{reason=shutdown}` 计数），停机期间靠 apiserver 的 2 万条缓冲；
存储明确拒绝的批（如某行解析失败）重试 3 次后整批丢弃并计 `reason=rejected`，不让一批坏数据卡死整条管线。接外部 ClickHouse 时必须填
`audit.clickhouse.existingSecret`（chart 渲染时校验）。

**2026-10-25：平台配额在虚拟集群 API 层即时拒绝。** syncer 新增验证 webhook `/validate-quota`（pods CREATE、PVC CREATE/UPDATE）：
用量取宿主 ResourceQuota `vk8s-quota` 的 `status.used` 加上虚拟集群里尚未下沉的对象，请求量用 k8s 自带的配额求值器，超出即 403，
文案与原生 `exceeded quota: …` 逐字同格式且换算成用户视角（operator 在 RQ 上写 `vk8s.io/quota-overhead` 注解）。
failurePolicy Ignore：syncer 重启期间退化为「宿主拒绝后写 Event」。Service 不走它：ClusterIP 对齐 webhook 在准入时已同步创建宿主 Service，
宿主的配额拒绝由它原样转成 403；headless / ExternalName 与极短并发窗口仍由宿主兜底。e2e 新增 9b 段落，待 Docker 可用后运行。

**2026-10-25：应用目录。** 新 CRD `AddonTemplate` / `AddonInstall` 与 manager 里的 `AddonInstallReconciler`：等集群 Running → 在虚拟集群里生成模板声明的
Secret（ssh 密钥对、随机密码）→ 在 vk8s-system 起 Job 执行 `vk8s addon run`（exec 镜像内置的 helm 4.3.1，结果写 termination message）→ Ready 后解析
访问信息；模板版本变化只标「有新版本」，由所有者点升级。chart values `addons.catalog` 附 devpods / grafana / prometheus 三个注释掉的示例。
目录项只改 values（版本号不变）时，已装实例标「有更新」，所有者点「升级」按新内容重装；管理员也可 `kubectl annotate addoninstall <名> vk8s.io/addon-retry=now`。
2026-10-26 之前因 namespace 归属失败的安装（如 devpods 报 "cannot be imported into the current release"），在虚拟集群里删掉那个 namespace 后重试即可。
**升级到这个版本必须先 apply 新的 CRD**；平台镜像含 helm，约大 50 MB。已知限制：虚拟集群里的 Prometheus 抓不到 kubelet / cAdvisor 指标
（宿主 kubelet 不认虚拟集群的 ServiceAccount token），只有 kube-state-metrics 与应用自身指标。e2e 新增 9c 段落（podinfo），待 Docker 可用后运行。

**2026-10-26：虚拟集群的 cAdvisor 指标。** 虚拟集群里的 Prometheus 经虚拟 apiserver 的节点代理抓 `/metrics/cadvisor`、`/metrics/resource`：
请求经隧道到 syncer 的假 kubelet，假 kubelet 用集群的 mTLS 证书问 manager，manager 用 `nodes/proxy` 从宿主 kubelet 取数并**只留本集群宿主 ns 的序列与
`machine_*`**（整机用量与其他租户一律丢弃），syncer 再把 namespace / pod 改回虚拟名。kubelet 自身的 `/metrics` 不提供。虚拟 Node 保留真实地址，
所以 Pod 直连 `节点IP:10250` 的默认抓取方式抓不到（宿主 kubelet 不认虚拟集群的 token），要改走节点代理：应用目录里的 Prometheus 已配好；
自己装的用下面两份 values 之一。虚拟集群 CoreDNS 同时开启了 `prometheus :9153`。

社区 `prometheus` chart：

```yaml
scrapeConfigs:
  kubernetes-nodes: {enabled: false}
  kubernetes-nodes-cadvisor:
    relabel_configs:
      - {action: labelmap, regex: __meta_kubernetes_node_label_(.+)}
      - {target_label: __address__, replacement: "kubernetes.default.svc:443"}
      - {source_labels: [__meta_kubernetes_node_name], regex: (.+), target_label: __metrics_path__, replacement: /api/v1/nodes/$1/proxy/metrics/cadvisor}
```

两者都还要给 Prometheus 的 ServiceAccount 一个 `nodes/proxy` get（chart 自带的 ClusterRole 只有 `nodes/metrics`，节点代理会 403），
用 chart 的 `extraManifests` 加一对 ClusterRole / ClusterRoleBinding 即可（写法见 chart values 里 prometheus 目录项的示例）。
注意 `nodes/proxy` 同时能经节点代理读本集群所有 Pod 的日志（`/containerLogs/...`），只授给 Prometheus 自己的 ServiceAccount。

`kube-prometheus-stack`（关掉自带的 kubelet ServiceMonitor，换成走代理的同名任务，现成看板可用）：

```yaml
kubelet: {enabled: false}
prometheus:
  prometheusSpec:
    additionalScrapeConfigs:
      - job_name: kubelet
        honor_labels: true
        scheme: https
        tls_config: {ca_file: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt, insecure_skip_verify: true}
        bearer_token_file: /var/run/secrets/kubernetes.io/serviceaccount/token
        kubernetes_sd_configs: [{role: node}]
        relabel_configs:
          - {target_label: __address__, replacement: "kubernetes.default.svc:443"}
          - {source_labels: [__meta_kubernetes_node_name], regex: (.+), target_label: __metrics_path__, replacement: /api/v1/nodes/$1/proxy/metrics/cadvisor}
          - {source_labels: [__meta_kubernetes_node_name], target_label: node}
          - {target_label: metrics_path, replacement: /metrics/cadvisor}
```

v1 不包含：休眠与按需唤醒、宿主侧 ValidatingAdmissionPolicy 兜底、按 OIDC group 自动成员、owner 转让、
sqlite 备份恢复、多宿主集群、L7 网关、集群内角色细分（审计见上一条）。
