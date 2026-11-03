# cagent 构建与质量门禁
# 产物：单一静态二进制 cagent（agent/server/ctl 三子命令）

BINARY   := cagent
MODULE   := github.com/crazychengmm/cagent
VERSION  ?= dev
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS  := -s -w \
	-X $(MODULE)/internal/version.Version=$(VERSION) \
	-X $(MODULE)/internal/version.Commit=$(COMMIT)

DIST := dist
# Linux 用 sha256sum，macOS 无 coreutils 时回退到 shasum（输出格式与 -c 互通）。
SHASUM := $(shell command -v sha256sum 2>/dev/null || echo "shasum -a 256")

.PHONY: all build build-linux proto fmt vet test test-race package release-assets clean

all: build

# 从 proto/ 重新生成 gRPC 代码（需 protoc + protoc-gen-go(-grpc)，见 proto/README.md）
proto:
	protoc --proto_path=proto \
		--go_out=. --go_opt=module=$(MODULE) \
		--go-grpc_out=. --go-grpc_opt=module=$(MODULE) \
		proto/cagent/v1/*.proto

# 本机构建（开发自测）
build:
	go build -ldflags '$(LDFLAGS)' -o $(DIST)/$(BINARY) ./cmd/cagent

# 目标平台静态构建：linux/amd64 + linux/arm64（CGO_ENABLED=0）
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags '$(LDFLAGS)' \
		-o $(DIST)/$(BINARY)-linux-amd64 ./cmd/cagent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags '$(LDFLAGS)' \
		-o $(DIST)/$(BINARY)-linux-arm64 ./cmd/cagent

fmt:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then echo "gofmt 未格式化文件:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

# package：用本机 cagent 把 linux/amd64 产物打成签名前的 .pkg（再用 ctl release sign）。
package: build-linux build
	mkdir -p $(DIST)
	./$(DIST)/$(BINARY) ctl release package \
		--binary $(DIST)/$(BINARY)-linux-amd64 \
		--version $(VERSION) \
		--out $(DIST)/$(BINARY)-$(VERSION).pkg

# release-assets：构建双架构静态二进制并按版本重命名、生成 SHA256SUMS。
# 用法：make release-assets VERSION=1.0.0
# GitHub Actions 在打 v* tag 时自动执行同样步骤（见 .github/workflows/release.yml）。
SEMVER := $(patsubst v%,%,$(VERSION))
release-assets: build-linux
	mv $(DIST)/$(BINARY)-linux-amd64 $(DIST)/$(BINARY)-$(SEMVER)-linux-amd64
	mv $(DIST)/$(BINARY)-linux-arm64 $(DIST)/$(BINARY)-$(SEMVER)-linux-arm64
	cd $(DIST) && $(SHASUM) \
		$(BINARY)-$(SEMVER)-linux-amd64 \
		$(BINARY)-$(SEMVER)-linux-arm64 > $(BINARY)-$(SEMVER)-SHA256SUMS
	@echo "产物在 $(DIST)/，校验和："
	@cat $(DIST)/$(BINARY)-$(SEMVER)-SHA256SUMS

clean:
	rm -rf $(DIST)
