#!/usr/bin/env bash
# 在 macOS 上编带界面版并打 DMG：arm64 与 amd64 各编一次（需要 CGO 与 Xcode 命令行工具）→ lipo 合成通用二进制
# → LemonNote.app → 签名 → LemonNote-<版本>-macos-universal.dmg。
# .app 只在临时目录（.noindex，不进聚焦索引）里组装，打完 DMG 就删掉，不在本机留副本。
#
# 用法：scripts/package-macos.sh [输出目录]        默认 <仓库>/dist
# 环境变量：
#   MACOS_SIGN_IDENTITY  Developer ID Application 证书名；不设就做临时签名（ad-hoc）
#   LEMON_NOTE_VERSION   覆盖版本号
#   KEEP_APP=<目录>       把组装好的 .app 另外复制一份到这个目录（CI 自测用，本机不要用）
set -euo pipefail
. "$(dirname "$0")/lib.sh"

[ "$(uname -s)" = Darwin ] || { echo "只能在 macOS 上运行" >&2; exit 1; }
OUT="${1:-$LN_ROOT/dist}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
V="$(ln_version)"
LDFLAGS="$(ln_ldflags "$V")"
ID="${MACOS_SIGN_IDENTITY:--}"
SHORT="$(ln_numeric_version "$V" | cut -d. -f1-3)"
BUNDLE_VERSION="$(ln_numeric_version "$V")"
BUILD="$LN_SERVICE/cmd/lemon-note-gui/build"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/lemon-note-app.XXXXXX")"
mv "$WORK" "$WORK.noindex"
WORK="$WORK.noindex"
trap 'rm -rf "$WORK"' EXIT

# Go 最低支持 macOS 12；C 部分（Wails 的 Objective-C）也按 12 编，避免链接时的版本警告
export MACOSX_DEPLOYMENT_TARGET=12.0
export CGO_ENABLED=1 CGO_CFLAGS="-mmacosx-version-min=12.0" CGO_LDFLAGS="-mmacosx-version-min=12.0"
for arch in arm64 amd64; do
  echo "编译 darwin/$arch"
  (cd "$LN_SERVICE" && GOARCH="$arch" go build -tags gui,production -trimpath -ldflags "$LDFLAGS" \
    -o "$WORK/LemonNote-$arch" ./cmd/lemon-note-gui)
done

APP="$WORK/LemonNote.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
xcrun lipo -create -output "$APP/Contents/MacOS/LemonNote" "$WORK/LemonNote-arm64" "$WORK/LemonNote-amd64"
xcrun lipo -info "$APP/Contents/MacOS/LemonNote"
sed -e "s/@SHORT_VERSION@/$SHORT/" -e "s/@BUNDLE_VERSION@/$BUNDLE_VERSION/" "$BUILD/darwin/Info.plist" > "$APP/Contents/Info.plist"
plutil -lint "$APP/Contents/Info.plist" >/dev/null
printf 'APPL????' > "$APP/Contents/PkgInfo"
cp "$BUILD/darwin/icons.icns" "$APP/Contents/Resources/icons.icns"

if [ "$ID" = "-" ]; then
  echo "临时签名（没有配置 Developer ID 证书）"
  codesign --force --deep --sign - "$APP"
else
  echo "用 $ID 签名"
  codesign --force --options runtime --timestamp --sign "$ID" "$APP"
fi
codesign --verify --strict --verbose=2 "$APP"
"$APP/Contents/MacOS/LemonNote" --version

if [ -n "${KEEP_APP:-}" ]; then
  mkdir -p "$KEEP_APP"
  rm -rf "$KEEP_APP/LemonNote.app"
  ditto "$APP" "$KEEP_APP/LemonNote.app"
fi

DMG="$OUT/LemonNote-$V-macos-universal.dmg"
STAGE="$WORK/dmg"
mkdir -p "$STAGE"
ditto "$APP" "$STAGE/LemonNote.app"
ln -s /Applications "$STAGE/Applications"
rm -f "$DMG"
# hdiutil 在 CI 上偶尔报「资源忙」，重试几次
for i in 1 2 3 4 5; do
  if hdiutil create -quiet -volname "Lemon Note" -srcfolder "$STAGE" -fs HFS+ -format UDZO -ov "$DMG"; then break; fi
  [ "$i" = 5 ] && exit 1
  echo "hdiutil 失败，重试（$i）"
  sleep 3
done
if [ "$ID" != "-" ]; then
  codesign --force --timestamp --sign "$ID" "$DMG"
fi
echo "完成：$DMG"
