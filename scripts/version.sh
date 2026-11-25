#!/usr/bin/env bash
# 打印这次构建的版本号（构建脚本与 CI 共用）：
#   推送 v* 标签          标签去掉 v，例如 v0.2.0 → 0.2.0
#   CI 里的其他构建       internal/version 的 Version 去掉 -dev，再加构建序号，例如 0.1.0.42
#   本机构建              原样，例如 0.1.0-dev
# 设置了 LEMON_NOTE_VERSION 时直接用它。
set -euo pipefail

if [ -n "${LEMON_NOTE_VERSION:-}" ]; then
  echo "$LEMON_NOTE_VERSION"
  exit 0
fi
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASE="$(sed -n 's/^[[:space:]]*Version[[:space:]]*=[[:space:]]*"\(.*\)".*/\1/p' "$ROOT/lemon-note-service/internal/version/version.go" | head -1)"
[ -n "$BASE" ] || { echo "读不到 internal/version 里的版本号" >&2; exit 1; }
if [ "${GITHUB_REF_TYPE:-}" = tag ] && [[ "${GITHUB_REF_NAME:-}" == v* ]]; then
  echo "${GITHUB_REF_NAME#v}"
elif [ -n "${GITHUB_RUN_NUMBER:-}" ]; then
  echo "${BASE%-dev}.${GITHUB_RUN_NUMBER}"
else
  echo "$BASE"
fi
