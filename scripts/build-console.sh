#!/usr/bin/env bash
# 构建前端（lemon-note-console），把产物复制进 lemon-note-service/internal/web/dist，
# 之后 go build 会用 go:embed 把它嵌进程序。
# 用法：scripts/build-console.sh [--skip-build]
#   --skip-build  不重新构建，只复制已有的 lemon-note-console/dist
set -euo pipefail
. "$(dirname "$0")/lib.sh"

CONSOLE="$LN_ROOT/lemon-note-console"
DIST="$LN_SERVICE/internal/web/dist"

if [ "${1:-}" != "--skip-build" ]; then
  cd "$CONSOLE"
  # 依赖没装或锁文件比已装的新时才重新安装
  if [ ! -f node_modules/.package-lock.json ] || [ package-lock.json -nt node_modules/.package-lock.json ]; then
    npm ci --no-audit --no-fund
  fi
  npm run build
fi

[ -f "$CONSOLE/dist/index.html" ] || { echo "没有找到 $CONSOLE/dist/index.html，请先构建前端" >&2; exit 1; }
mkdir -p "$DIST"
# 清掉上一次的产物（保留占位文件 .keep），再整体复制
find "$DIST" -mindepth 1 -maxdepth 1 ! -name .keep -exec rm -rf {} +
cp -R "$CONSOLE/dist/." "$DIST/"
echo "前端已复制到 $DIST"
