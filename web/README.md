# vk8s Web 控制台

React 18 + TypeScript + Vite + react-router-dom v6，无 UI 框架（手写 CSS，浅色 / 深色随系统切换并可手动覆盖）。
构建产物由 Go manager 通过 `embed.FS` 打进二进制，在 `/` 提供。

## 开发

```sh
cd web
npm ci
npm run dev          # http://localhost:5173
```

dev server 把 `/api` 和 `/auth` 代理到 `http://localhost:8080`（本地运行的 `vk8s manager`）。
会话 cookie 是同源的，经代理后同样生效。OIDC 回调地址由 manager 的 `auth.publicURL` 决定，
本地开发走 OIDC 时需要把它指向 dev server 地址。

### 不依赖集群：mock API

```sh
npm run mock         # 终端一：http://127.0.0.1:8080，Node 内建模块实现的全部端点，数据在内存里
npm run dev          # 终端二：vite 照常把 /api /auth 代理到 8080
```

固定用户 `admin`（管理员）、`alice`、`bob`、`carol`（已禁用），密码都是 `password`。
四个集群覆盖 Running / Provisioning / Failed / Deleting 四种状态；新建的集群 10 秒后变为 Running，删除 5 秒后消失。
三个可预约节点（`gpu-07`、`gpu-08`、`gpu-09`，最后一个 NotReady）与 5 条预约（进行中 / 待开始 / 已结束 / 已拒绝），phase 按请求时刻计算。
应用目录三项（devpods 要求 privileged、grafana 带一个按需显示的密码、prometheus），alice-dev 已装旧版 grafana（可演示升级），安装 8 秒后 Ready、卸载 4 秒后消失。
约 300 条审计事件（两类来源、成功与失败、带请求体），`config.auditEnabled=true`；租户权限与游标翻页与真实后端一致。
变更只在内存中生效，重启还原。本机已有真实 manager 占用 8080 时用 `PORT=18080 npm run mock`，并把 `vite.config.ts` 的代理目标改成同一端口。

## 测试

```sh
npm test             # vitest run（jsdom）；仓库根目录 make test-web 等价
```

只给有逻辑的部分写测试：`ThemeContext`（默认跟随系统、持久化、存储不可用）、`useTabParam`（URL 同步、非法与无权限回落）、
`StatusDot`（状态映射）、`Tabs`（键盘漫游）、`Sidebar` / `UserMenu` / `AppShell`（权限分组、菜单、抽屉随路由关闭）、
`pages/cluster/tabs`（Tab 可见性与计数）、`pages/bookings/timeline`（窗口与几何、拖选遇占用停止）、`BookingTimeline`（拖选与色块）、
`pages/admin/bookableImpact`（标记确认文案）、`pages/audit/filters`（过滤 ↔ URL、时间段、翻页合并）、`pages/audit/labels`（动作与对象文案）、
`AuditView`（列表、抽屉与 `?event=` 同步）。样式不写自动化测试，改样式后在 mock 下逐页看浅色、深色、窄屏（< 900px）。

## 构建

```sh
npm run build        # tsc --noEmit && vite build
```

输出目录是 `../internal/manager/ui/dist`（`emptyOutDir: true`，每次构建先清空）。
仓库根目录 `make web` / `make build` 和 Dockerfile 都调用这条命令。

## 主题

- 令牌只定义在 `src/styles/tokens.css`：`:root` 为浅色；`@media (prefers-color-scheme: dark)` 下 `:root:not([data-theme="light"])` 与
  `:root[data-theme="dark"]` 各有一份相同的深色值。组件只引用语义令牌（`--bg` `--surface` `--fg` `--accent` `--status-*` …），不写裸色值。
- `src/theme/ThemeContext.tsx`：偏好 `system | light | dark` 存 localStorage 键 `vk8s.theme`，非 system 时写到 `<html data-theme>`。
  用户菜单里的「外观」就是它的入口。

## 目录

```
mock/           开发用 mock API（server.mjs 路由、fixtures.mjs 数据）
src/
  api/          fetch 封装（client.ts）、契约类型（types.ts）、按资源拆分的调用
  auth/         会话（AuthProvider）与平台配置（ConfigProvider）上下文
  theme/        ThemeProvider / useTheme
  styles/       tokens（令牌与主题）、base（重置与排版）、layout（侧栏、抽屉、登录页）、components（组件类）
  components/   AppShell、Sidebar、UserMenu、ThemeToggle、PageHeader、Breadcrumb、Tabs、Table、Card、
                DescriptionList、Chips、StatusDot、Badge、Button、Modal、ConfirmDialog、Toast、Field、KeyValueEditor…
  hooks/        useAsync / usePolling / useNow / useTabParam
  pages/        登录、我的集群、节点预约（bookings/ 下是时间轴与对话框）、审计（audit/ 下是过滤条、列表、抽屉，管理员页与集群 Tab 共用）、
                集群详情（cluster/ 下按 Tab 拆分）、管理员页面（admin/）
  utils/        时间、Go duration、校验、格式化、剪贴板
  test/         vitest setup
```

## 与后端的约定

- 所有变更请求（POST/PUT/PATCH/DELETE）带 `X-Requested-With: vk8s`，`credentials: same-origin`。
- 错误响应为 `{error}` + HTTP 状态码；任一接口返回 401 时清空会话并跳到 `/login`。
- `GET /auth/me` 401 时若正文带 `oidcEnabled`，登录页据此决定是否显示 SSO 按钮。
- `config.limits.tokenDefaultTTL / tokenMaxTTL` 为 Go duration 字符串（如 `720h0m0s`）；
  生成令牌时提交 `ttl: "<n>h"`。
- 集群详情的 Tab 用查询参数 `?tab=overview|kubeconfig|members|settings`，缺省 overview 不写进 URL；
  `settings` 只对管理员与所有者可见，无权限时回落到概览并改写 URL。
