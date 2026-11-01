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

.PHONY: all build build-linux fmt vet test test-race clean

all: build

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

clean:
	rm -rf $(DIST)
