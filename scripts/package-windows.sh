#!/usr/bin/env bash
# 编 Windows 带界面版并打包：每个架构一个免安装 zip，有 makensis 时再出安装包。
# Windows 版的 Wails 不需要 CGO，任何系统都能交叉编译；CI 在 Windows 上跑（签名、打安装包都在那边）。
#
# 用法：scripts/package-windows.sh [输出目录]        默认 <仓库>/dist
# 环境变量：
#   ARCHS                     要编的架构，默认 "amd64 arm64"
#   LEMON_NOTE_VERSION        覆盖版本号
#   WINDOWS_CERT_FILE         代码签名证书（.pfx）路径；不设就不签名
#   WINDOWS_CERT_PASSWORD     证书密码
#   WINDOWS_TIMESTAMP_URL     时间戳服务，默认 http://timestamp.digicert.com
# 产物：LemonNote-<版本>-windows-<架构>.zip、LemonNote-<版本>-windows-<架构>-setup.exe
set -euo pipefail
. "$(dirname "$0")/lib.sh"

OUT="${1:-$LN_ROOT/dist}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
V="$(ln_version)"
NV="$(ln_numeric_version "$V")"
LDFLAGS="$(ln_ldflags "$V") -H windowsgui"
GUI="$LN_SERVICE/cmd/lemon-note-gui"
BUILD="$GUI/build/windows"
ARCHS="${ARCHS:-amd64 arm64}"
WINRES="github.com/tc-hib/go-winres@v0.3.3"

WORK="$(mktemp -d)"
cleanup() { rm -rf "$WORK"; rm -f "$GUI"/rsrc_windows_*.syso; }
trap cleanup EXIT

# 图标、清单（DPI、通用控件 6）与版本信息编成 .syso，go build 时按架构自动链接进去
(cd "$BUILD" && go run "$WINRES" make --in winres.json --arch "${ARCHS// /,}" \
  --product-version "$NV" --file-version "$NV" --out "$GUI/rsrc")
ls "$GUI"/rsrc_windows_*.syso

MAKENSIS="$(command -v makensis || true)"
if [ -z "$MAKENSIS" ] && [ -x "/c/Program Files (x86)/NSIS/makensis.exe" ]; then MAKENSIS="/c/Program Files (x86)/NSIS/makensis.exe"; fi

for arch in $ARCHS; do
  echo "编译 windows/$arch"
  mkdir -p "$WORK/$arch"
  (cd "$LN_SERVICE" && CGO_ENABLED=0 GOOS=windows GOARCH="$arch" \
    go build -tags gui,production -trimpath -ldflags "$LDFLAGS" -o "$WORK/$arch/LemonNote.exe" ./cmd/lemon-note-gui)
  ln_sign_windows "$WORK/$arch/LemonNote.exe"
  cp "$LN_ROOT/LICENSE" "$WORK/$arch/LICENSE.txt"
  ln_zip "$OUT/LemonNote-$V-windows-$arch.zip" "$WORK/$arch" LemonNote.exe LICENSE.txt
  echo "  打包 LemonNote-$V-windows-$arch.zip"

  if [ -n "$MAKENSIS" ]; then
    SETUP="$OUT/LemonNote-$V-windows-$arch-setup.exe"
    src="$WORK/$arch/LemonNote.exe" dst="$SETUP"
    if command -v cygpath >/dev/null 2>&1; then src="$(cygpath -w "$src")" dst="$(cygpath -w "$dst")"; fi
    # makensis 的参数统一用 - 开头（Git Bash 会把 /D 这样的参数当成路径改写）
    (cd "$BUILD" && "$MAKENSIS" -V2 -DVERSION="$V" -DVIVERSION="$NV" -DARCH="$arch" -DSRC="$src" -DOUTFILE="$dst" installer.nsi)
    ln_sign_windows "$SETUP"
    echo "  安装包 $(basename "$SETUP")"
  else
    echo "  没有 makensis，跳过安装包"
  fi
done
echo "完成："
ls -l "$OUT"
