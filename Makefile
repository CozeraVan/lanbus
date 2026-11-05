PY ?= python
PORT ?= 8788
TOKEN ?= devtoken
GOEXE := $(shell go env GOEXE)

.PHONY: help install test test-proto test-e2e wheel hub clean docker-build docker-up lint go-test go-scan go-worker go-hub go-gui package go-scan-all go-worker-all go-hub-all

help:
	@echo "make install      # 可编辑安装 + dev 依赖"
	@echo "make test         # 全部测试"
	@echo "make wheel        # 构建 dist/*.whl（离线）"
	@echo "make hub          # 本机起 hub (PORT=$(PORT) TOKEN=$(TOKEN))"
	@echo "make go-test      # Go scanner tests"
	@echo "make go-scan      # build native Go scanner into bin/"
	@echo "make go-worker    # build native Go worker into bin/"
	@echo "make go-hub       # build native Go hub (Phase 2) into bin/"
	@echo "make go-gui       # build Windows Wails GUI into bin/"
	@echo "make package      # portable Windows zip under dist/"
	@echo "make go-scan-all  # cross-build mac/linux scanners"
	@echo "make go-worker-all # cross-build mac/linux workers"
	@echo "make go-hub-all   # cross-build mac/linux Go hubs"
	@echo "make docker-build # docker build"
	@echo "make clean        # 清构建产物"

install:
	$(PY) -m pip install -e ".[dev]"

test:
	$(PY) -m pytest tests -v

wheel:
	$(PY) -m pip wheel . --no-deps --no-build-isolation -w dist

hub:
	$(PY) -m lanbus hub --port $(PORT) --token $(TOKEN)

go-test:
	cd go && go test ./...

go-scan:
	cd go && go build -trimpath -ldflags="-s -w" -o ../bin/lanbus-scan$(GOEXE) ./cmd/lanbus-scan

go-worker:
	cd go && go build -trimpath -ldflags="-s -w" -o ../bin/lanbus-worker$(GOEXE) ./cmd/lanbus-worker

go-hub:
	cd go && go build -trimpath -ldflags="-s -w" -o ../bin/lanbusd$(GOEXE) ./cmd/lanbusd

go-gui:
	cd go && go build -tags production -ldflags="-H=windowsgui -s -w" -o ../bin/lanbus-gui.exe ./cmd/lanbus-gui

package: go-hub go-worker go-gui
	$(PY) scripts/package_windows.py

go-worker-all:
	cd go && GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o ../bin/lanbus-worker-darwin-arm64 ./cmd/lanbus-worker
	cd go && GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o ../bin/lanbus-worker-darwin-amd64 ./cmd/lanbus-worker
	cd go && GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o ../bin/lanbus-worker-linux-amd64 ./cmd/lanbus-worker

go-hub-all:
	cd go && GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o ../bin/lanbusd-darwin-arm64 ./cmd/lanbusd
	cd go && GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o ../bin/lanbusd-darwin-amd64 ./cmd/lanbusd
	cd go && GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o ../bin/lanbusd-linux-amd64 ./cmd/lanbusd

go-scan-all:
	cd go && GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o ../bin/lanbus-scan-darwin-arm64 ./cmd/lanbus-scan
	cd go && GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o ../bin/lanbus-scan-darwin-amd64 ./cmd/lanbus-scan
	cd go && GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o ../bin/lanbus-scan-linux-amd64 ./cmd/lanbus-scan

docker-build:
	docker build -t lanbus:1.9.0 .

docker-up:
	docker compose up -d

clean:
	rm -rf dist build *.egg-info .pytest_cache
	find . -name __pycache__ -type d -prune -exec rm -rf {} +