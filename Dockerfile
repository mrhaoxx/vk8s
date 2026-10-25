ARG HELM_VERSION=4.3.1
# 单镜像：manager / gateway / control-plane / syncer 都用它。
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# kine 的 sqlite 驱动（mattn/go-sqlite3）需要 cgo：CGO_ENABLED=0 时 kine 只注册一个返回错误的桩。
# 因此用 glibc 的 builder + distroless/base（带 glibc）运行，而不是 static 镜像。
FROM golang:1.26-bookworm AS build
ARG TAG=dev
ARG K8S_VERSION=v1.36.2
# 构建机的 GOPROXY 透传进来（make image 自动带上），国内环境必需。
ARG GOPROXY
ENV GOPROXY=${GOPROXY:-https://proxy.golang.org,direct}
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /src/internal/manager/ui/dist ./internal/manager/ui/dist
# SQLITE_ENABLE_DBSTAT_VTAB：kine Status 查询 dbstat 虚表所需（与 kine/k3s 构建参数一致）。
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 CGO_CFLAGS="-DSQLITE_ENABLE_DBSTAT_VTAB=1 -DSQLITE_USE_ALLOCA=1" go build -trimpath \
    -ldflags "-s -w -X main.version=${TAG} -X k8s.io/component-base/version.gitVersion=${K8S_VERSION} -X k8s.io/component-base/version.gitMajor=1 -X k8s.io/component-base/version.gitMinor=36" \
    -o /out/vk8s ./cmd/vk8s

# 应用目录的安装 Job 用内置 helm（设计 2026-10-25 §3.1）
ARG HELM_VERSION=4.3.1
FROM alpine/helm:${HELM_VERSION} AS helm
FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /out/vk8s /usr/local/bin/vk8s
COPY --from=helm /usr/bin/helm /usr/local/bin/helm
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/vk8s"]
