# GTasks 统一构建入口。
#
#   make            列出全部目标
#   make build      编译当前平台的 ./gtasks
#   make app        组装带图标的 macOS GTasks.app
#   make check      gofmt + go vet + go test
#   make verify     语言 / 主题切换的端到端回归（要求应用已运行且已登录）
#   make package    按当前平台产出发布物到 build/bin/
#   make dist       交叉产出全部平台的发布物
#
# 版本号默认取 git tag（git describe），可显式覆盖：make package VERSION=1.0.0

SHELL := /bin/bash

APP     := gtasks
BUNDLE  := GTasks
BIN_DIR := build/bin

GIT_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null)
VERSION     ?= $(if $(filter v%,$(GIT_VERSION)),$(patsubst v%,%,$(GIT_VERSION)),0.0.0-dev)

HOST_OS   := $(shell go env GOOS)
HOST_ARCH := $(shell go env GOARCH)
# 可覆盖：make package-linux ARCH=arm64
ARCH      ?= $(HOST_ARCH)
# go env GOOS 的 darwin 对应打包脚本里的 macos
PKG_OS    := $(if $(filter darwin,$(HOST_OS)),macos,$(HOST_OS))
LDFLAGS   := -X main.version=$(VERSION)

GOPROXY ?= https://goproxy.cn,direct
export GOPROXY

.PHONY: help build run app icon test fmt fmt-write vet check verify verify-quick \
        package package-macos package-windows package-linux \
        dist dist-macos dist-windows dist-linux checksums clean tag

help:
	@echo "GTasks $(VERSION)  可用目标："
	@echo "  build            编译 ./$(APP)（当前平台）"
	@echo "  run              编译并启动"
	@echo "  app              组装 /tmp/$(BUNDLE).app（macOS，带图标）"
	@echo "  icon             由 build/icon.svg 重新生成 icns（需本机 Chrome）"
	@echo "  test             go test ./..."
	@echo "  fmt / fmt-write  检查 / 修正 gofmt"
	@echo "  vet              go vet ./..."
	@echo "  check            fmt + vet + test"
	@echo "  verify           端到端回归（全量，约 70 秒）"
	@echo "  verify-quick     端到端回归（跳过浅色主题那一轮）"
	@echo "  package          当前平台的发布物 -> $(BIN_DIR)/"
	@echo "  package-macos    $(BIN_DIR)/$(APP)-<ver>-macos-<arch>.dmg"
	@echo "  package-windows  $(BIN_DIR)/$(APP)-<ver>-windows-<arch>.exe + .zip"
	@echo "  package-linux    $(BIN_DIR)/$(APP)-<ver>-linux-<arch> + .tar.gz"
	@echo "  dist             三个平台 x amd64/arm64 全量产出"
	@echo "  checksums        为 $(BIN_DIR)/ 里的产物生成 sha256sums.txt"
	@echo "  clean            清理构建产物"
	@echo "  tag              打 v$(VERSION) 标签（不推送）"

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(APP) .

run: build
	./$(APP)

app:
	bash build/macos-bundle.sh /tmp/$(BUNDLE).app $(VERSION)

icon:
	bash build/macos-icon.sh

test:
	go test ./...

fmt:
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then echo "以下文件需要 gofmt:"; echo "$$out"; exit 1; fi

fmt-write:
	gofmt -w .

vet:
	go vet ./...

check: fmt vet test

verify:
	python3 scripts/verify-lang-switch.py

verify-quick:
	python3 scripts/verify-lang-switch.py --quick

package: package-$(PKG_OS)

package-macos:
	bash scripts/build-macos.sh $(VERSION) $(ARCH)

package-windows:
	bash scripts/build-windows.sh $(VERSION) $(ARCH)

package-linux:
	bash scripts/build-linux.sh $(VERSION) $(ARCH)

dist: dist-macos dist-windows dist-linux

dist-macos:
	bash scripts/build-macos.sh $(VERSION) amd64
	bash scripts/build-macos.sh $(VERSION) arm64

dist-windows:
	bash scripts/build-windows.sh $(VERSION) amd64
	bash scripts/build-windows.sh $(VERSION) arm64

dist-linux:
	bash scripts/build-linux.sh $(VERSION) amd64
	bash scripts/build-linux.sh $(VERSION) arm64

checksums:
	@cd $(BIN_DIR) && ls $(APP)-* 2>/dev/null | grep -v sha256sums.txt > /tmp/gtasks-assets.lst; \
	if [ ! -s /tmp/gtasks-assets.lst ]; then echo "$(BIN_DIR)/ 下没有产物"; rm -f /tmp/gtasks-assets.lst; exit 1; fi; \
	if command -v sha256sum >/dev/null 2>&1; then \
	  sha256sum $$(cat /tmp/gtasks-assets.lst) > sha256sums.txt; \
	else \
	  shasum -a 256 $$(cat /tmp/gtasks-assets.lst) > sha256sums.txt; \
	fi; \
	rm -f /tmp/gtasks-assets.lst; cat sha256sums.txt

clean:
	rm -rf $(BIN_DIR) $(APP) $(APP).exe

tag:
	git tag -a v$(VERSION) -m "GTasks $(VERSION)"
	@echo "已打标签 v$(VERSION)；推送： git push origin v$(VERSION)"
