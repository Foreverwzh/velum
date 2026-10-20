# sing-box 功能依赖的构建标签：wireguard 节点 / reality 与 uTLS 指纹 / hysteria2 与 tuic / gVisor 栈
TAGS ?= with_wireguard,with_utls,with_quic,with_gvisor
export GOPROXY ?= https://goproxy.cn,direct

.PHONY: test vet build release lint golden

test:
	go test -tags "$(TAGS)" ./...

vet:
	go vet -tags "$(TAGS)" ./...

build:
	go build -tags "$(TAGS)" -o bin/ ./cmd/...

# 发布构建（打包进 App）：去掉符号表与调试信息、路径
release:
	go build -tags "$(TAGS)" -trimpath -ldflags "-s -w" -o bin/ ./cmd/...

golden:
	go test -tags "$(TAGS)" ./pkg/engine/singbox/render/ -run TestRenderGolden -update

lint: vet
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
