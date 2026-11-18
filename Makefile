# 优先使用仓库内置的 Go 工具链（.tools/go），否则回退到 PATH 中的 go。
GO ?= $(shell command -v go 2>/dev/null || echo $(CURDIR)/.tools/go/bin/go)
PNPM ?= pnpm

# 使用内置 Go 时固定其 GOROOT 与模块缓存，避免外部 GOROOT 指向旧 SDK 导致构建失败。
TOOLS_GO := $(CURDIR)/.tools/go
ifeq ($(GO),$(TOOLS_GO)/bin/go)
GO_ENV := GOROOT="$(TOOLS_GO)" GOMODCACHE="$(CURDIR)/.tools/gomodcache" GOPATH="$(CURDIR)/.tools/gopath"
else
GO_ENV :=
endif

EMBED_DIST := server/internal/webui/dist
BIN := server/bin/packrund
# 版本号注入二进制，供 /healthz 与运维排查确认线上跑的是哪个构建。
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/packrun/packrun/internal/api.Version=$(VERSION)

PKG_GOOS ?= linux
PKG_GOARCH ?= amd64
PKG_NAME := packrun-$(PKG_GOOS)-$(PKG_GOARCH)
PKG_DIR := dist/$(PKG_NAME)

.PHONY: build web sync-web clean package check

# 单包构建：前端产物同步到 internal/webui/dist 后由 go:embed 打进二进制。
# 交叉编译示例：GOOS=linux GOARCH=amd64 make build
build: web sync-web
	cd server && CGO_ENABLED=0 $(GO_ENV) "$(GO)" build -trimpath -ldflags "$(LDFLAGS)" -o bin/packrund ./cmd/packrund
	@echo "单文件产物: $(BIN)（前端已内嵌）"

# 生成可直接上传服务器的部署包（默认 linux/amd64）。
# 其他架构：PKG_GOARCH=arm64 make package
package: web sync-web
	rm -rf "$(PKG_DIR)"
	mkdir -p "$(PKG_DIR)/server/bin" "$(PKG_DIR)/server/configs" "$(PKG_DIR)/deploy"
	cd server && CGO_ENABLED=0 GOOS=$(PKG_GOOS) GOARCH=$(PKG_GOARCH) \
		$(GO_ENV) "$(GO)" build -trimpath -ldflags "$(LDFLAGS)" -o "$(CURDIR)/$(PKG_DIR)/server/bin/packrund" ./cmd/packrund
	cp server/configs/config.yaml "$(PKG_DIR)/server/configs/"
	cp start.sh "$(PKG_DIR)/"
	cp deploy/nginx.conf.example deploy/frps.toml deploy/frpc.toml "$(PKG_DIR)/deploy/"
	tar -C dist -czf "dist/$(PKG_NAME).tar.gz" "$(PKG_NAME)"
	@echo "部署包: dist/$(PKG_NAME).tar.gz"

# 本地对齐 CI：Go 静态检查与测试 + 前端类型检查。
check:
	cd server && $(GO_ENV) "$(GO)" vet ./...
	cd server && $(GO_ENV) "$(GO)" test ./...
	cd web && $(PNPM) type-check

web:
	cd web && $(PNPM) install --frozen-lockfile && $(PNPM) build

# 把前端构建产物同步到 go:embed 目录（保留 .gitkeep）。
sync-web:
	@mkdir -p $(EMBED_DIST)
	@find $(EMBED_DIST) -mindepth 1 ! -name '.gitkeep' -delete
	cp -R web/dist/. $(EMBED_DIST)/

clean:
	rm -f $(BIN)
	rm -rf dist
	@mkdir -p $(EMBED_DIST)
	@find $(EMBED_DIST) -mindepth 1 ! -name '.gitkeep' -delete
