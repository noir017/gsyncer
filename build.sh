#!/usr/bin/env bash
# 编译 gsyncer 为无外部依赖的 Linux 静态单文件可执行程序。
#   - CGO_ENABLED=0  : 禁用 cgo，使用纯 Go 的 net/os/user 实现，不链接 libc
#   - -ldflags "-s -w" : 去掉符号表和调试信息，减小体积
#   - -trimpath      : 去掉编译机的绝对路径，构建可复现
# 用法: ./build.sh [输出路径]   (默认 dist/gsyncer)
# 环境变量:
#   GOARCH=arm64   交叉编译目标架构（默认 amd64）
#   GOARM=7        GOARCH=arm 时的 ARM 版本（默认 7）
#   VERSION=1.2.3  写入 `gsyncer version` 的版本号（默认用源码里的值）
set -euo pipefail

cd "$(dirname "$0")"

OUT="${1:-dist/gsyncer}"
GOARCH="${GOARCH:-amd64}"   # 可用环境变量覆盖，如 GOARCH=arm64 ./build.sh
GOARM="${GOARM:-7}"         # 仅在 GOARCH=arm 时有意义

# 版本号通过 -X 注入，未指定时保留 main.go 里的默认值，本地构建无需关心。
LDFLAGS="-s -w"
if [ -n "${VERSION:-}" ]; then
    LDFLAGS="$LDFLAGS -X main.version=$VERSION"
fi

mkdir -p "$(dirname "$OUT")"

echo "==> 构建 $OUT  (linux/$GOARCH${VERSION:+, 版本 $VERSION})"
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" GOARM="$GOARM" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$OUT" .

echo "==> 完成: $(ls -lh "$OUT" | awk '{print $5}')"
echo "==> 文件类型:"
file "$OUT" || true
echo "==> 动态依赖检查 (应为 'not a dynamic executable'):"
ldd "$OUT" 2>&1 || true
