# 构建脚本的公共函数，由其他脚本 source，不单独运行。

LN_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LN_SERVICE="$LN_ROOT/lemon-note-service"
LN_VERSION_PKG="github.com/LemonITCN/lemon-note/lemon-note-service/internal/version"

ln_version() { "$LN_ROOT/scripts/version.sh"; }

# ln_ldflags <版本>：注入版本、提交与构建时间（取提交时间，同一提交重复构建结果一致），并去掉调试信息
ln_ldflags() {
  local commit date
  commit="${GITHUB_SHA:-$(git -C "$LN_ROOT" rev-parse HEAD 2>/dev/null || true)}"
  date="$(TZ=UTC git -C "$LN_ROOT" show -s --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ HEAD 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "-s -w -X $LN_VERSION_PKG.Version=$1 -X $LN_VERSION_PKG.Commit=$commit -X $LN_VERSION_PKG.Date=$date"
}

# ln_numeric_version <版本>：转成 Windows 资源与安装包要求的四段数字，例如 0.1.0-dev → 0.1.0.0
ln_numeric_version() {
  local v="${1%%-*}" a b c d
  v="${v%%+*}"
  IFS=. read -r a b c d <<<"$v"
  echo "${a:-0}.${b:-0}.${c:-0}.${d:-0}"
}

# ln_zip <输出 zip 的绝对路径> <目录> <目录里的文件...>
ln_zip() {
  local out="$1" dir="$2" py
  shift 2
  rm -f "$out"
  if command -v zip >/dev/null 2>&1; then
    (cd "$dir" && zip -q -X -r "$out" "$@")
  elif command -v 7z >/dev/null 2>&1; then
    (cd "$dir" && 7z a -tzip -bd -bso0 "$out" "$@")
  else
    py="$(command -v python3 || command -v python)"
    (cd "$dir" && "$py" -c 'import sys, zipfile
with zipfile.ZipFile(sys.argv[1], "w", zipfile.ZIP_DEFLATED) as z:
    for f in sys.argv[2:]:
        z.write(f)' "$out" "$@")
  fi
}

# ln_sha256sums <目录>：给目录里的文件写 SHA256SUMS（不含它自己）
ln_sha256sums() {
  (
    cd "$1"
    local files=()
    for f in *; do [ -f "$f" ] && [ "$f" != SHA256SUMS ] && files+=("$f"); done
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "${files[@]}"; else shasum -a 256 "${files[@]}"; fi > SHA256SUMS
  )
}

# ln_sign_windows <exe>：配置了 WINDOWS_CERT_FILE（.pfx）与 WINDOWS_CERT_PASSWORD 时给 Windows 程序签名，否则什么也不做。
# Windows 上用 Windows SDK 的 signtool，其他系统用 osslsigncode。时间戳服务可用 WINDOWS_TIMESTAMP_URL 覆盖。
ln_sign_windows() {
  [ -n "${WINDOWS_CERT_FILE:-}" ] || return 0
  local ts="${WINDOWS_TIMESTAMP_URL:-http://timestamp.digicert.com}" tool=""
  if command -v signtool >/dev/null 2>&1; then
    tool=signtool
  elif [ -d "/c/Program Files (x86)/Windows Kits/10/bin" ]; then
    tool="$(ls -d "/c/Program Files (x86)/Windows Kits/10/bin/"10.*/x64/signtool.exe 2>/dev/null | sort -V | tail -1 || true)"
  fi
  if [ -n "$tool" ]; then
    local cert="$WINDOWS_CERT_FILE"
    command -v cygpath >/dev/null 2>&1 && cert="$(cygpath -w "$cert")"
    "$tool" sign //f "$cert" //p "$WINDOWS_CERT_PASSWORD" //fd SHA256 //tr "$ts" //td SHA256 //d "Lemon Note" "$1"
  elif command -v osslsigncode >/dev/null 2>&1; then
    osslsigncode sign -pkcs12 "$WINDOWS_CERT_FILE" -pass "$WINDOWS_CERT_PASSWORD" -h sha256 -ts "$ts" -n "Lemon Note" \
      -in "$1" -out "$1.signed"
    mv "$1.signed" "$1"
  else
    echo "配置了 Windows 签名证书，但找不到 signtool 或 osslsigncode" >&2
    return 1
  fi
}
