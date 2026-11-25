#!/usr/bin/env bash
# 从前端的柠檬图标（lemon-note-console/public/favicon.svg）生成带界面版需要的全部图标，
# 输出到 lemon-note-service/cmd/lemon-note-gui/build。图标改了以后在 macOS 上运行一次并提交结果。
# 需要：rsvg-convert（librsvg）、magick（ImageMagick 7）、iconutil（macOS 自带）。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/lemon-note-console/public/favicon.svg"
OUT="$ROOT/lemon-note-service/cmd/lemon-note-gui/build"
for t in rsvg-convert magick iconutil; do
  command -v "$t" >/dev/null || { echo "缺少 $t" >&2; exit 1; }
done

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
mkdir -p "$OUT/darwin" "$OUT/windows" "$OUT/linux/icons" "$OUT/tray"

# 图标内容（去掉外层 <svg> 标签），用来拼出不同留白的版本
INNER="$(sed -e 's/<svg[^>]*>//' -e 's#</svg>##' "$SRC")"

# 满幅版本：Windows、Linux 与托盘用
cp "$SRC" "$OUT/appicon.svg"
rsvg-convert -w 1024 -h 1024 "$SRC" -o "$OUT/appicon.png"

# macOS 版本：按系统图标规范四周留白（1024 画布里 824 的圆角方块）
cat > "$TMP/mac.svg" <<EOF
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1024 1024"><g transform="translate(100 100) scale(12.875)">$INNER</g></svg>
EOF
ICONSET="$TMP/icons.iconset"
mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
  rsvg-convert -w "$s" -h "$s" "$TMP/mac.svg" -o "$ICONSET/icon_${s}x${s}.png"
  rsvg-convert -w $((s * 2)) -h $((s * 2)) "$TMP/mac.svg" -o "$ICONSET/icon_${s}x${s}@2x.png"
done
iconutil -c icns "$ICONSET" -o "$OUT/darwin/icons.icns"

# Windows：多尺寸 .ico
ICO_PNGS=()
for s in 16 20 24 32 40 48 64 128 256; do
  rsvg-convert -w "$s" -h "$s" "$SRC" -o "$TMP/ico-$s.png"
  ICO_PNGS+=("$TMP/ico-$s.png")
done
magick "${ICO_PNGS[@]}" "$OUT/windows/icon.ico"

# Linux：hicolor 主题各尺寸
for s in 16 24 32 48 64 128 256 512; do
  mkdir -p "$OUT/linux/icons/${s}x${s}"
  rsvg-convert -w "$s" -h "$s" "$SRC" -o "$OUT/linux/icons/${s}x${s}/cn.lemonit.note.png"
done

# 托盘：Windows / Linux 用彩色图标，服务停止时用灰色半透明版本
rsvg-convert -w 64 -h 64 "$SRC" -o "$OUT/tray/tray.png"
magick "$OUT/tray/tray.png" -colorspace Gray -channel A -evaluate multiply 0.55 +channel "$OUT/tray/tray-off.png"

# macOS 菜单栏：模板图标（只有柠檬剪影，系统按深浅色自动着色），中间那道线挖空
cat > "$TMP/template.svg" <<'EOF'
<svg xmlns="http://www.w3.org/2000/svg" viewBox="12 13 38 38">
  <defs>
    <mask id="cut">
      <rect x="0" y="0" width="64" height="64" fill="#fff"/>
      <path d="M22 42c6-6 12-12 20-19" stroke="#000" stroke-width="3" stroke-linecap="round" fill="none"/>
    </mask>
  </defs>
  <path d="M20 44c-4-12 2-24 14-26 7-1 12 3 12 3s1 8-4 15c-6 8-15 11-22 8z" fill="#000" mask="url(#cut)"/>
</svg>
EOF
rsvg-convert -w 44 -h 44 "$TMP/template.svg" -o "$OUT/tray/tray-template.png"

# 压掉 PNG 里的时间戳等元数据，重复生成时文件不变
find "$OUT" -name '*.png' -exec magick mogrify -strip {} +

echo "图标已生成到 $OUT"
