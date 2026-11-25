#!/usr/bin/env bash
# 交叉编译命令行版 lemon-note（纯 Go、关闭 CGO、内嵌前端），产物命名与 CI 发布一致：
#   <输出目录>/lemon-note-<版本>-<系统>-<架构>.tar.gz（Windows 为 .zip）与 SHA256SUMS
#
# 用法：scripts/build-all.sh [目标 ...]
#   目标写 linux、windows、darwin 表示该系统的全部架构，也可以写单个目标如 linux/arm64、linux/armv7；不写就是全部
#   macOS 的两个架构在有 lipo 的机器（装了 Xcode 命令行工具的 Mac）上合成通用二进制 macos-universal，
#   其他机器分别打成 macos-amd64、macos-arm64
# 环境变量：
#   OUT                  输出目录，默认 <仓库>/dist
#   LEMON_NOTE_VERSION   覆盖版本号（默认见 scripts/version.sh）
#   MACOS_SIGN_IDENTITY  macOS 签名证书名（Developer ID Application: ...），不设就做临时签名
#   WINDOWS_CERT_FILE / WINDOWS_CERT_PASSWORD  Windows 代码签名证书（.pfx）与密码，不设就不签名
# 前端产物要先放进 internal/web/dist（scripts/build-console.sh），否则程序里只有「前端尚未构建」的提示页。
set -euo pipefail
. "$(dirname "$0")/lib.sh"

ALL=(linux/amd64 linux/arm64 linux/armv7 linux/386 linux/riscv64 linux/loong64 linux/ppc64le linux/s390x
  windows/amd64 windows/arm64 darwin/amd64 darwin/arm64)

TARGETS=()
if [ $# -eq 0 ]; then
  TARGETS=("${ALL[@]}")
else
  for want in "$@"; do
    found=0
    for t in "${ALL[@]}"; do
      if [ "$t" = "$want" ] || [ "${t%%/*}" = "$want" ]; then TARGETS+=("$t"); found=1; fi
    done
    [ $found = 1 ] || { echo "未知目标：${want}（可选：${ALL[*]}）" >&2; exit 2; }
  done
fi

V="$(ln_version)"
LDFLAGS="$(ln_ldflags "$V")"
OUT="${OUT:-$LN_ROOT/dist}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

if [ ! -f "$LN_SERVICE/internal/web/dist/index.html" ]; then
  echo "提示：internal/web/dist 里没有前端产物，编出来的程序只有占位页（先运行 scripts/build-console.sh）" >&2
fi
echo "版本 ${V}，输出到 $OUT"

LIPO=""
if [ "$(uname -s)" = Darwin ]; then LIPO="xcrun lipo"; elif command -v lipo >/dev/null 2>&1; then LIPO="lipo"; fi

# build <goos> <arch 名> <输出文件>
build() {
  local goos="$1" arch="$2" out="$3" goarch="$2" goarm=""
  if [ "$arch" = armv7 ]; then goarch=arm goarm=7; fi
  echo "  编译 $goos/$arch"
  (cd "$LN_SERVICE" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$out" ./cmd/lemon-note)
}

# pack <名称后缀> <目录>：目录里是 lemon-note(.exe) 与 LICENSE
pack() {
  local name="lemon-note-$V-$1" dir="$2"
  cp "$LN_ROOT/LICENSE" "$dir/"
  if [ -f "$dir/lemon-note.exe" ]; then
    ln_zip "$OUT/$name.zip" "$dir" lemon-note.exe LICENSE
  else
    chmod +x "$dir/lemon-note"
    tar -C "$dir" -czf "$OUT/$name.tar.gz" lemon-note LICENSE
  fi
  echo "  打包 $name"
}

# macOS：签名（有证书用 Developer ID 加强化运行时，否则临时签名）
sign_macos() {
  [ "$(uname -s)" = Darwin ] || return 0
  local id="${MACOS_SIGN_IDENTITY:--}"
  if [ "$id" = "-" ]; then
    codesign --force --sign - "$1"
  else
    codesign --force --options runtime --timestamp --sign "$id" "$1"
  fi
}

DARWIN=()
for t in "${TARGETS[@]}"; do
  goos="${t%%/*}" arch="${t##*/}"
  case "$goos" in
    windows)
      mkdir -p "$WORK/$t"
      build windows "$arch" "$WORK/$t/lemon-note.exe"
      ln_sign_windows "$WORK/$t/lemon-note.exe"
      pack "windows-$arch" "$WORK/$t"
      ;;
    darwin)
      mkdir -p "$WORK/$t"
      build darwin "$arch" "$WORK/$t/lemon-note"
      DARWIN+=("$arch")
      ;;
    *)
      mkdir -p "$WORK/$t"
      build linux "$arch" "$WORK/$t/lemon-note"
      pack "linux-$arch" "$WORK/$t"
      ;;
  esac
done

if [ ${#DARWIN[@]} -eq 2 ] && [ -n "$LIPO" ]; then
  mkdir -p "$WORK/darwin/universal"
  $LIPO -create -output "$WORK/darwin/universal/lemon-note" "$WORK/darwin/amd64/lemon-note" "$WORK/darwin/arm64/lemon-note"
  sign_macos "$WORK/darwin/universal/lemon-note"
  $LIPO -info "$WORK/darwin/universal/lemon-note"
  pack macos-universal "$WORK/darwin/universal"
else
  for arch in ${DARWIN[@]+"${DARWIN[@]}"}; do
    sign_macos "$WORK/darwin/$arch/lemon-note"
    pack "macos-$arch" "$WORK/darwin/$arch"
  done
fi

ln_sha256sums "$OUT"
echo "完成："
ls -l "$OUT"
