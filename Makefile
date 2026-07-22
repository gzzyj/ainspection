.PHONY: build build-debug test fmt vet check generate clean install

# 二进制输出目录
BIN_DIR := bin
BINARY := $(BIN_DIR)/ainspection

# Go 编译参数
GO := go
GOFLAGS := -ldflags="-s -w"
GOFLAGS_DEBUG := -gcflags="all=-N -l"

# 默认目标
.DEFAULT_GOAL := build

# build 编译 linux/amd64 二进制到 bin/
build:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o $(BINARY) ./cmd/ainspection/

# build-debug 编译 debug 版本（带符号，无优化）
build-debug:
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS_DEBUG) -o $(BINARY) ./cmd/ainspection/

# test 运行所有测试
test:
	$(GO) test -gcflags=all=-l ./... -count=1

# test-cover 运行测试并输出覆盖率
test-cover:
	$(GO) test -gcflags=all=-l ./... -coverprofile=coverage.out -count=1
	$(GO) tool cover -func=coverage.out

# fmt 格式化代码
fmt:
	gofumpt -w .

# vet 静态分析
vet:
	$(GO) vet ./...

# check tidy + fmt + vet
check: tidy fmt vet
	@echo "check passed"

# tidy 整理依赖
tidy:
	$(GO) mod tidy

# generate 代码生成
generate:
	$(GO) generate ./...

# clean 清理编译产物
clean:
	rm -rf $(BIN_DIR)

# install 安装到 GOPATH/bin
install:
	$(GO) install ./cmd/ainspection/
