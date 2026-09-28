# GOCORDIS 采集器发布 Makefile（在 WSL/Linux 内交叉编译出 Windows 产物）
#
#   make release                  全平台发布：测试 + windows/linux 产物 + zip
#   make windows / make linux     单平台产物
#   make release TS=2609221231    固定时间戳目录（默认为当前 yyMMddHHmm）
#   make console                  重建内嵌控制台（需 ../go-cordis）
#
# 产物布局（每个平台目录自包含，可直接整体拷贝部署）：
#   dist/<TS>/windows/  csv-collector.exe  web-ui.exe
#                       configs/*.toml  plugins/alarm-demo/  winsw.xml
#   dist/<TS>/linux/    同上（无 winsw.xml，exe 无后缀）
#   dist/<TS-windows.zip> / <TS-linux.zip>

GO        ?= go
TS        ?= $(shell date +%y%m%d%H%M)
DIST      := dist/$(TS)
LDFLAGS   := -s -w
GOFLAGS   := -trimpath
CGO       := CGO_ENABLED=0
PLUGIN    := plugins/alarm-demo
CONFIGS   := $(wildcard configs/*.toml)

.PHONY: help build test windows linux console zip release clean

help:
	@grep '^#' Makefile | sed 's/^# //' | head -14

build:
	$(GO) build ./...

test:
	$(GO) vet ./...
	$(GO) test ./...

# 内嵌前端：web/dist 是 go:embed 输入，需要时先从框架同步
console:
	bash scripts/build-console.sh

windows:
	@mkdir -p $(DIST)/windows/plugins/alarm-demo $(DIST)/windows/configs
	$(CGO) GOOS=windows GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/windows/csv-collector.exe ./cmd/csv-collector
	$(CGO) GOOS=windows GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/windows/web-ui.exe ./cmd/web-ui
	@# 进程外插件后端：独立 module，产物与清单/前端模块同目录
	cd $(PLUGIN) && $(CGO) GOOS=windows GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(CURDIR)/$(DIST)/windows/plugins/alarm-demo/alarm-demo.exe .
	cp $(PLUGIN)/manifest.toml $(PLUGIN)/ui.js $(DIST)/windows/plugins/alarm-demo/
	cp $(CONFIGS) $(DIST)/windows/configs/
	cp packaging/winsw.xml $(DIST)/windows/winsw.xml
	cp packaging/winswv3.exe $(DIST)/windows/winswv3.exe
	cp packaging/run.bat $(DIST)/windows/run.bat
	@echo "windows -> $(DIST)/windows"

linux:
	@mkdir -p $(DIST)/linux/plugins/alarm-demo $(DIST)/linux/configs
	$(CGO) GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/linux/csv-collector ./cmd/csv-collector
	$(CGO) GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/linux/web-ui ./cmd/web-ui
	cd $(PLUGIN) && $(CGO) GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $(CURDIR)/$(DIST)/linux/plugins/alarm-demo/alarm-demo .
	cp $(PLUGIN)/manifest.toml $(PLUGIN)/ui.js $(DIST)/linux/plugins/alarm-demo/
	cp $(CONFIGS) $(DIST)/linux/configs/
	@echo "linux -> $(DIST)/linux"

zip:
	@test -d $(DIST)/windows && (cd $(DIST) && zip -qr ../$(TS)-windows.zip windows) || echo "skip windows.zip (未构建)"
	@test -d $(DIST)/linux && (cd $(DIST) && zip -qr ../$(TS)-linux.zip linux) || echo "skip linux.zip (未构建)"
	@echo "zips -> dist/$(TS)-windows.zip dist/$(TS)-linux.zip"

release: test windows linux zip

clean:
	rm -rf dist
