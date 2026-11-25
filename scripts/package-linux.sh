#!/usr/bin/env bash
# 在 Linux 上编带界面版并打 deb（可选再打 AppImage）。只编本机架构（需要 CGO 与 GTK4 / WebKitGTK 6.0）：
#   sudo apt-get install -y build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev
# 编出来的程序依赖 GTK 4.14+ 与 WebKitGTK 6.0：Ubuntu 24.04、Debian 13、Fedora 40 起可用。
#
# 用法：scripts/package-linux.sh [输出目录]        默认 <仓库>/dist
# 环境变量：
#   LEMON_NOTE_VERSION  覆盖版本号
#   APPIMAGE=1          再打 AppImage（用 Wails 的 wails3 工具，会联网下载 linuxdeploy）；
#   APPIMAGE=try        同上，但打不出来只警告、不算失败
# 产物：lemon-note_<版本>_<amd64|arm64>.deb、LemonNote-<版本>-linux-<架构>.AppImage
set -euo pipefail
. "$(dirname "$0")/lib.sh"

OUT="${1:-$LN_ROOT/dist}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
V="$(ln_version)"
LDFLAGS="$(ln_ldflags "$V")"
BUILD="$LN_SERVICE/cmd/lemon-note-gui/build"
case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) echo "不支持的架构 $(uname -m)" >&2; exit 1 ;;
esac

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

echo "编译 linux/$ARCH"
(cd "$LN_SERVICE" && CGO_ENABLED=1 go build -tags gui,production -trimpath -ldflags "$LDFLAGS" \
  -o "$WORK/lemon-note-gui" ./cmd/lemon-note-gui)
"$WORK/lemon-note-gui" --version

# ---------- deb ----------
D="$WORK/deb"
install -Dm755 "$WORK/lemon-note-gui" "$D/usr/bin/lemon-note-gui"
install -Dm644 "$BUILD/linux/cn.lemonit.note.desktop" "$D/usr/share/applications/cn.lemonit.note.desktop"
for dir in "$BUILD"/linux/icons/*x*; do
  s="$(basename "$dir")"
  install -Dm644 "$dir/cn.lemonit.note.png" "$D/usr/share/icons/hicolor/$s/apps/cn.lemonit.note.png"
done
install -Dm644 "$BUILD/appicon.svg" "$D/usr/share/icons/hicolor/scalable/apps/cn.lemonit.note.svg"
install -Dm644 "$LN_ROOT/LICENSE" "$D/usr/share/doc/lemon-note/copyright"
mkdir -p "$D/DEBIAN"
SIZE="$(du -sk "$D/usr" | cut -f1)"
cat > "$D/DEBIAN/control" <<EOF
Package: lemon-note
Version: $V
Section: utils
Priority: optional
Architecture: $ARCH
Maintainer: LemonITCN <lemonitcn@users.noreply.github.com>
Installed-Size: $SIZE
Depends: libc6, libgtk-4-1 (>= 4.14), libwebkitgtk-6.0-4
Suggests: gnome-shell-extension-appindicator
Homepage: https://github.com/LemonITCN/lemon-note
Description: Lemon Note - self-hosted notes (desktop tray app)
 Pages, databases, comments, history, sharing and a built-in AI agent,
 served to your browser. The desktop app keeps the service running in the
 system tray and shows its status. All data lives in one data folder.
EOF
# 装好、卸载后刷新菜单与图标缓存
cat > "$D/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database -q /usr/share/applications || true
command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q -t /usr/share/icons/hicolor || true
exit 0
EOF
cp "$D/DEBIAN/postinst" "$D/DEBIAN/postrm"
chmod 755 "$D/DEBIAN/postinst" "$D/DEBIAN/postrm"
DEB="$OUT/lemon-note_${V}_${ARCH}.deb"
dpkg-deb --root-owner-group -Zxz --build "$D" "$DEB" >/dev/null
echo "deb：$DEB"

# ---------- AppImage（可选）----------
appimage() {
  mkdir -p "$WORK/appimage"
  cp "$BUILD/linux/icons/256x256/cn.lemonit.note.png" "$WORK/appimage/cn.lemonit.note.png"
  cp "$BUILD/linux/cn.lemonit.note.desktop" "$WORK/appimage/cn.lemonit.note.desktop"
  # wails3 工具与程序用同一个 Wails 版本；在模块外面运行，免得它的依赖混进本仓库的 go.sum
  local wails img
  wails="$(cd "$LN_SERVICE" && go list -m -f '{{.Version}}' github.com/wailsapp/wails/v3)"
  (cd "$WORK/appimage" && go run "github.com/wailsapp/wails/v3/cmd/wails3@$wails" generate appimage \
    -binary "$WORK/lemon-note-gui" -icon "$WORK/appimage/cn.lemonit.note.png" \
    -desktopfile "$WORK/appimage/cn.lemonit.note.desktop" -outputdir "$WORK/appimage/out" -builddir "$WORK/appimage/build") || return 1
  img="$(ls "$WORK"/appimage/out/*.AppImage 2>/dev/null | head -1)"
  [ -n "$img" ] || return 1
  install -m755 "$img" "$OUT/LemonNote-$V-linux-$ARCH.AppImage"
  echo "AppImage：$OUT/LemonNote-$V-linux-$ARCH.AppImage"
}
case "${APPIMAGE:-}" in
  1) echo "打 AppImage"; appimage ;;
  try) echo "打 AppImage（失败不影响 deb）"; appimage || echo "警告：AppImage 没有打出来" >&2 ;;
esac
