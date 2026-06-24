#!/usr/bin/env bash
# 编译 gsync 为无外部依赖的 Linux 静态单文件可执行程序。
#   - CGO_ENABLED=0  : 禁用 cgo，使用纯 Go 的 net/os/user 实现，不链接 libc
#   - -ldflags "-s -w" : 去掉符号表和调试信息，减小体积
#   - -trimpath      : 去掉编译机的绝对路径，构建可复现
# 用法: ./build.sh [输出路径]   (默认 dist/gsync)
set -euo pipefail

cd "$(dirname "$0")"

OUT="${1:-dist/gsync}"
GOARCH="${GOARCH:-amd64}"   # 可用环境变量覆盖，如 GOARCH=arm64 ./build.sh

mkdir -p "$(dirname "$OUT")"

echo "==> 构建 $OUT  (linux/$GOARCH)"
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" \
    go build -trimpath -ldflags "-s -w" -o "$OUT" .

echo "==> 完成: $(ls -lh "$OUT" | awk '{print $5}')"
echo "==> 文件类型:"
file "$OUT" || true
echo "==> 动态依赖检查 (应为 'not a dynamic executable'):"
ldd "$OUT" 2>&1 || true
