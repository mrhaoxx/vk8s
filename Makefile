IMAGE ?= registry.corp.com/vk8s/vk8s
TAG   ?= dev
K8S_VERSION ?= v1.36.2
# 镜像内置的 helm（应用目录的安装 Job 用）
HELM_VERSION ?= 4.3.1

GO_LDFLAGS := -s -w \
  -X main.version=$(TAG) -X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo none) \
  -X k8s.io/component-base/version.gitVersion=$(K8S_VERSION) \
  -X k8s.io/component-base/version.gitMajor=1 \
  -X k8s.io/component-base/version.gitMinor=36

# kine 的 Status（apiserver 用来探测 etcd 版本与 db 大小）查 sqlite 的 dbstat 虚表，需编译期开启；与 kine/k3s 的构建参数一致。
SQLITE_CFLAGS := -DSQLITE_ENABLE_DBSTAT_VTAB=1 -DSQLITE_USE_ALLOCA=1

.PHONY: build web image image-local crds tidy update-k8s smoke test test-web e2e

build: web
	CGO_ENABLED=1 CGO_CFLAGS='$(SQLITE_CFLAGS)' go build -trimpath -ldflags '$(GO_LDFLAGS)' -o bin/vk8s ./cmd/vk8s

web:
	cd web && npm ci && npm run build

image:
	docker build -t $(IMAGE):$(TAG) --build-arg TAG=$(TAG) --build-arg K8S_VERSION=$(K8S_VERSION) --build-arg HELM_VERSION=$(HELM_VERSION) \
	  --build-arg GOPROXY=$(shell go env GOPROXY) .

# 本机开发：不在容器里下载模块（国内网络下 go mod download 常断），而是把宿主的 module cache 只读挂进
# golang 容器离线编译 linux 二进制，再用 Dockerfile.local 打运行镜像；go build cache 放在 docker volume 以便增量。
# -buildvcs=false：Go 1.26 会把主模块按 git 推出的伪版本写进 GOMODCACHE 的 stat cache，而这里的 cache 是只读挂载；版本与 commit 已由 ldflags 注入。
# 交叉构建（如 Apple Silicon 上出 amd64 镜像）：DOCKER_DEFAULT_PLATFORM=linux/amd64 make image-local
GOMODCACHE := $(shell go env GOMODCACHE)
image-local: web
	docker run --rm -v $(GOMODCACHE):/go/pkg/mod:ro -v vk8s-gocache:/root/.cache/go-build -v $(CURDIR):/src -w /src \
	  -e GOFLAGS='-mod=mod -buildvcs=false' -e GOPROXY=off -e CGO_ENABLED=1 -e CGO_CFLAGS='$(SQLITE_CFLAGS)' \
	  golang:1.26-bookworm go build -trimpath -ldflags '$(GO_LDFLAGS)' -o bin/linux/vk8s ./cmd/vk8s
	docker build -t $(IMAGE):$(TAG) --build-arg HELM_VERSION=$(HELM_VERSION) -f Dockerfile.local .

crds:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.18.0 \
	  crd object paths=./api/... output:crd:artifacts:config=deploy/helm/vk8s/crds

tidy:
	go mod tidy

update-k8s:
	hack/update-k8s-replaces.sh $(K8S_VERSION)

smoke:
	hack/smoke-control-plane.sh

# 单元测试（翻译函数、认证、TokenReview、隧道）。envtest 与 e2e 不在其中。前端单测用 make test-web。
test:
	CGO_ENABLED=1 CGO_CFLAGS='$(SQLITE_CFLAGS)' go test ./internal/... ./cmd/...

# 前端单元测试（Vitest）。
test-web:
	cd web && npm test

# kind 端到端测试；本机国内网络下常用：BUILD_TARGET=image-local KEEP=1 make e2e
e2e:
	IMAGE=$(IMAGE) TAG=$(TAG) hack/kind-e2e.sh
