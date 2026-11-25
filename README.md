# Lemon Note

私有化部署的笔记与知识库：页面、数据库、评论、历史版本、分享，内置智能体，同一套工具以 MCP 开放给 Claude Code、Cursor 等外部智能体。一个程序、一个数据目录，装在自己的电脑、NAS 或服务器上，用浏览器访问。

[English](#english)

## 功能概览

- **页面与块编辑**：无限层级页面树；标题、列表、待办、引用、标注、代码块（高亮）、数学公式、表格、图片与附件、Mermaid 与 Graphviz 图、图表、思维导图、白板；斜杠菜单、拖拽排序、@ 提及与页面链接；实时协同编辑。
- **数据库**：表格、看板、列表、画廊、日历、时间线、图表视图；公式、关联与汇总、筛选排序分组。
- **协作**：多用户、空间与成员权限、评论、历史版本与还原、回收站、模板、分享与公开页面。
- **智能体**：内置智能体（多家模型供应商）读写你的笔记；同一套工具通过 MCP 提供给外部智能体。
- **导入导出**：Notion、Obsidian、Markdown、CSV；增强 Markdown 双向转换。
- **数据**：数据目录就是全部数据，可以放在 OneDrive、iCloud 等同步盘里；工作库默认 SQLite，也可以用 MySQL 8 / MariaDB 10.6+ / PostgreSQL 13+。
- **备份**：定时备份到本地目录、S3 兼容存储、腾讯云 COS、阿里云 OSS、WebDAV、SFTP、百度网盘，全量或增量，可加密。
- **安全**：两步验证、访问令牌与 MCP 的 OAuth 授权、登录限流、审计日志、自动 HTTPS 证书（ACME）。
- 中文与英文界面，浅色与深色主题，电脑与手机都完整适配。

## 下载

在 [Releases](https://github.com/LemonITCN/lemon-note/releases/latest) 下载对应的文件：

| 适合 | 下载 |
|---|---|
| 个人电脑，想要托盘图标和状态窗口 | 带界面版：macOS `LemonNote-<版本>-macos-universal.dmg`；Windows `LemonNote-<版本>-windows-<架构>-setup.exe`（或免安装 `.zip`）；Linux `lemon-note_<版本>_<架构>.deb` 或 `.AppImage` |
| 服务器、NAS、树莓派 | 命令行版：`lemon-note-<版本>-<系统>-<架构>.tar.gz`（Windows 为 `.zip`），单文件，内嵌网页界面 |
| 容器 | `ghcr.io/lemonitcn/lemon-note`（linux/amd64、linux/arm64） |

命令行版支持 Linux amd64 / arm64 / armv7 / 386 / riscv64 / loong64 / ppc64le / s390x、Windows amd64 / arm64、macOS 通用（Apple 芯片与 Intel）。带界面版支持 Windows 10 / 11（需要 WebView2 运行时，Windows 11 自带）、macOS 12 及以上、Ubuntu 24.04 / Debian 13 及以上（GTK 4.14+）。每个 Release 都附带 `SHA256SUMS`。

## 快速开始

### 带界面版

1. 安装后打开 Lemon Note。Windows 与 Linux 上柠檬图标出现在系统托盘，同时打开状态窗口；macOS 上图标出现在菜单栏，点它选「显示状态窗口」（首次使用时会自动打开）。
2. 第一次使用时状态窗口会显示「首次设置地址」，点「打开设置页」，在浏览器里选择数据库、创建管理员账号。
3. 之后从托盘菜单「打开 Lemon Note」进入。关闭状态窗口只是隐藏，服务继续在后台运行；从托盘菜单「退出」会先停止服务再退出。

状态窗口里可以启动 / 停止服务、复制访问地址、打开或更换数据目录、查看日志；托盘菜单里可以打开「开机自启」（macOS 用登录项，Windows 用当前用户的启动项，Linux 用 XDG 自启动）。

Linux 上托盘图标走 AppIndicator（StatusNotifierItem）：KDE、Xfce 等桌面自带，GNOME 需要安装 AppIndicator 扩展（Ubuntu 默认已装）。

### 命令行版

```sh
tar -xzf lemon-note-<版本>-linux-amd64.tar.gz
./lemon-note
```

启动后终端里会打印访问地址；数据目录还没初始化时，会再打印一个带一次性令牌的首次设置地址，在浏览器里打开它完成设置：

```
Lemon Note 0.1.0 已启动
  地址：http://127.0.0.1:8686
  数据目录：/home/me/.local/share/lemon-note

首次使用，请在浏览器打开下面的地址完成设置：
  http://127.0.0.1:8686/setup?token=...
```

常用选项：

```sh
./lemon-note --listen 127.0.0.1:8686      # 只允许本机访问（默认 :8686 监听所有网卡）
./lemon-note --data-dir /srv/lemon-note    # 指定数据目录
./lemon-note config --data-dir /srv/lemon-note --listen :8686   # 写进引导配置文件，之后不用再带参数
```

macOS 上用浏览器下载的命令行版需要先去掉隔离标记：`xattr -d com.apple.quarantine lemon-note`。

### 首次设置

首次设置页会让你：

1. 选择工作库：默认 SQLite，不需要任何外部服务；也可以填 MySQL 或 PostgreSQL 的连接串（必须是空库）。选定后不能更改。
2. 创建管理员账号。

设置令牌只用于防止别人抢先完成设置，完成后失效。忘记了重启程序会生成新的。

## 数据目录与同步盘

程序用到三个位置：

| 位置 | 内容 | 放同步盘 | 能否删除 |
|---|---|---|---|
| 引导配置文件 | 数据目录在哪、监听地址、TLS、外部数据库连接串 | 不要（本机专属） | 删了要重新指定数据目录 |
| 数据目录 | 全部持久数据 | 可以 | 不能 |
| 缓存目录 | 工作数据库、全文索引、缩略图、日志、临时文件 | 不要 | 能，启动时自动从数据目录重建 |

默认位置（以当前用户运行时；装成系统服务时见下文）：

| 系统 | 引导配置文件 | 数据目录 | 缓存目录 |
|---|---|---|---|
| Linux | `~/.config/lemon-note/lemon-note.toml` | `~/.local/share/lemon-note` | `~/.cache/lemon-note` |
| macOS | `~/Library/Application Support/Lemon Note/lemon-note.toml` | `~/Library/Application Support/Lemon Note/data` | `~/Library/Caches/Lemon Note` |
| Windows | `%APPDATA%\Lemon Note\lemon-note.toml` | `%LOCALAPPDATA%\Lemon Note\data` | `%LOCALAPPDATA%\Lemon Note\cache` |

优先级：命令行参数 > 环境变量 > 引导配置文件 > 默认值。环境变量有 `LEMON_NOTE_DATA_DIR`、`LEMON_NOTE_CACHE_DIR`、`LEMON_NOTE_LISTEN`、`LEMON_NOTE_BASE_URL`（对外访问地址）、`LEMON_NOTE_DSN`（外部数据库连接串）、`LEMON_NOTE_LOG_LEVEL`、`LEMON_NOTE_TRUSTED_PROXIES`（受信反向代理，逗号分隔的 IP 或网段）。

### 放进同步盘

数据目录里的文件只新增、不修改：每次写入都先写 `~$` 开头的临时文件（OneDrive、Dropbox 不会上传这类文件），写完再原子改名，所以同步盘只会看到完整的文件。日常的读写都在本机缓存目录的工作库里进行，同步盘慢也不影响使用。

需要注意：

- **同一个数据目录同一时间只能在一台电脑上运行。** 程序会在数据目录里记录占用者心跳，发现另一台电脑正在使用时拒绝启动；确认那台电脑已经停止后，可以用 `--take-over`（带界面版是状态窗口里的「强制接管」）接管。
- 换电脑：在旧电脑上退出 Lemon Note，等同步盘同步完成，在新电脑上把数据目录指向同一个文件夹（带界面版在状态窗口点「更换」），启动时会从数据目录自动重建缓存。
- 同步盘生成冲突副本（例如两台电脑同时运行过）时，系统状态页会告警。

带界面版选择同步盘根目录这类已有其他文件的文件夹时，会自动在里面建 `Lemon Note` 子目录当数据目录。

## 备份

数据目录就是全部数据，有两种备份方式：

- **内置定时备份**（推荐）：在网页的「设置 → 备份」里添加目标（本地目录 / NAS、S3 兼容、腾讯云 COS、阿里云 OSS、WebDAV、SFTP、百度网盘），每个目标单独设计划。
  - 全量：每次生成一个独立的 `.lnbak` 备份包，按数量或按天 / 周 / 月分层保留。
  - 增量（镜像）：目标上维护一份与数据目录同结构的副本，只上传新增文件；任何时刻目标上的副本都能直接当数据目录用。
  - 可设备份口令（argon2id + AES-256-GCM）。忘记口令无法恢复。
  - 恢复时选目标、选备份，默认恢复到新的数据目录。
- **直接复制数据目录**：先停止服务，再用任何工具复制整个数据目录。恢复时把数据目录指向这份副本即可。

缓存目录不用备份。使用 MySQL / PostgreSQL 时同样只需要备份数据目录：换一个空库启动，会从数据目录自动导入。

## MCP 接入

Lemon Note 在 `/mcp` 提供 MCP 端点（Streamable HTTP），工具与内置智能体相同。两种接入方式：

**方式一：OAuth 授权（推荐）**

支持 OAuth 2.1（动态注册 + PKCE）的客户端只需要填地址，第一次连接时会打开浏览器，登录 Lemon Note 后在确认页选择是否允许修改、允许访问哪些空间：

```sh
claude mcp add --transport http lemon-note https://<地址>/mcp
```

```json
{
  "mcpServers": {
    "lemon-note": { "url": "https://<地址>/mcp" }
  }
}
```

授权过的客户端在「设置 → 访问令牌 → 已授权的应用」里可以随时撤销。

**方式二：访问令牌**

在「设置 → 访问令牌」新建一个令牌（只显示一次，按需勾选「允许写入」、限定空间），放进请求头：

```sh
claude mcp add --transport http lemon-note https://<地址>/mcp --header "Authorization: Bearer <访问令牌>"
```

```json
{
  "mcpServers": {
    "lemon-note": {
      "url": "https://<地址>/mcp",
      "headers": { "Authorization": "Bearer <访问令牌>" }
    }
  }
}
```

从别的电脑访问时，`<地址>` 用服务器的域名或 IP，并建议启用 HTTPS（引导配置里的 `[tls]`，支持证书文件或 ACME 自动证书，也可以放在反向代理后面）。

## 系统服务

命令行版可以装成开机自启的系统服务（Linux 用 systemd，macOS 用 launchd，Windows 用 Windows 服务）：

```sh
sudo ./lemon-note service install --data-dir /var/lib/lemon-note --listen :8686
sudo ./lemon-note service start
sudo ./lemon-note service status      # stop | restart | uninstall
```

Windows 上用管理员身份打开 PowerShell 运行 `.\lemon-note.exe service install`。服务首次启动时同样进入首次设置，设置地址写在服务日志里（Linux：`journalctl -u lemon-note`；也可以看缓存目录下的 `logs/lemon-note.log`）。

以系统服务运行时的默认位置：

| 系统 | 引导配置文件 | 数据目录 | 缓存目录 |
|---|---|---|---|
| Linux | `/etc/lemon-note/lemon-note.toml` | `/var/lib/lemon-note` | `/var/cache/lemon-note` |
| macOS | `/Library/Application Support/Lemon Note/lemon-note.toml` | `/Library/Application Support/Lemon Note/data` | `/Library/Caches/Lemon Note` |
| Windows | `%ProgramData%\Lemon Note\lemon-note.toml` | `%ProgramData%\Lemon Note\data` | `%ProgramData%\Lemon Note\cache` |

维护命令：

```sh
./lemon-note admin list-users
./lemon-note admin reset-password <用户名>
./lemon-note admin reset-2fa <用户名>
./lemon-note doctor        # 检查数据目录完整性
./lemon-note config        # 显示当前生效的配置
./lemon-note version
```

## Docker

```sh
docker run -d --name lemon-note --restart unless-stopped \
  -p 8686:8686 \
  -v lemon-note-data:/data \
  -v lemon-note-cache:/cache \
  ghcr.io/lemonitcn/lemon-note:latest
docker logs lemon-note     # 首次启动会打印带令牌的首次设置地址
```

- 数据目录 `/data`（全部数据，务必挂卷并备份），缓存目录 `/cache`（可以删），监听 `:8686`。
- 以非 root 用户（UID 65532）运行。挂宿主机目录时先 `chown -R 65532:65532 <目录>`。
- 用外部数据库时用环境变量 `LEMON_NOTE_DSN` 配置连接串；通过域名或反向代理访问时设置 `LEMON_NOTE_BASE_URL`。
- 标签：`latest`（main 分支最新构建）、`<版本>`、`<主版本>.<次版本>`（正式版）。

完整示例见 [docker-compose.example.yml](docker-compose.example.yml)。

## 从源码构建

需要 Go 1.26+、Node.js 22+。

```sh
scripts/build-console.sh                  # 构建前端，并复制到 lemon-note-service/internal/web/dist（会被嵌进程序）
cd lemon-note-service
go build -o lemon-note ./cmd/lemon-note   # 命令行版，纯 Go，可加 CGO_ENABLED=0
go test ./...
```

其他构建脚本（都在 `scripts/`）：

| 脚本 | 作用 |
|---|---|
| `build-all.sh [目标...]` | 交叉编译全部平台的命令行版并打包，产物在 `dist/`，例如 `scripts/build-all.sh linux/arm64 windows` |
| `package-macos.sh` | 在 macOS 上编带界面版通用 `.app` 并打 DMG（需要 Xcode 命令行工具） |
| `package-windows.sh` | 编 Windows 带界面版（任何系统都能交叉编译），有 `makensis` 时再出安装包 |
| `package-linux.sh` | 在 Linux 上编带界面版并打 deb（`APPIMAGE=1` 再打 AppImage） |
| `gen-icons.sh` | 从前端图标重新生成带界面版的各平台图标 |
| `version.sh` | 打印本次构建的版本号 |

带界面版（`cmd/lemon-note-gui`，Wails v3）的源码带 `gui` 构建标签，`go build ./...` 不会编它，命令行版保持纯 Go。手动编译：

```sh
cd lemon-note-service
go build -tags gui,production ./cmd/lemon-note-gui
```

- macOS：需要 Xcode 命令行工具（CGO）。
- Windows：不需要 CGO，可以从任何系统交叉编译（`GOOS=windows`，链接参数加 `-H windowsgui`）。
- Linux：需要 CGO 与 `libgtk-4-dev`、`libwebkitgtk-6.0-dev`。

Docker 镜像（构建上下文是仓库根目录）：

```sh
docker build -f lemon-note-service/Dockerfile -t lemon-note .
```

存储层的外部数据库集成测试需要准备空的测试库，设置环境变量后运行（测试会清空库里的表）：

```sh
LEMON_NOTE_TEST_MYSQL_DSN='root:test@tcp(127.0.0.1:3306)/lemon_note_test' \
LEMON_NOTE_TEST_PG_DSN='postgres://postgres:test@127.0.0.1:5432/lemon_note_test?sslmode=disable' \
go test -run External ./internal/persist/
```

架构与数据格式见 [docs/spec.md](docs/spec.md)。

## 许可证

[MIT](LICENSE)

## English

Lemon Note is a self-hosted notes and knowledge base in the spirit of Notion: nested pages with a block editor, databases with table / board / list / gallery / calendar / timeline / chart views, comments, version history, templates, sharing, and import / export (Notion, Obsidian, Markdown, CSV). A built-in AI agent works on your notes, and the same tool set is exposed over MCP for Claude Code, Cursor and other agents.

- **One program, one data folder.** The data folder holds everything and only ever gains new immutable files, so it can live in OneDrive, iCloud or any sync folder (one running machine at a time). The working database (SQLite by default, or MySQL / PostgreSQL) and indexes live in a disposable cache folder and are rebuilt automatically.
- **Desktop app** (Windows, macOS, Linux): a tray icon plus a status window to start / stop the service, open it in the browser, change the data folder, view logs and launch at login.
- **Command-line server** for Linux (8 architectures), Windows and macOS: a single static binary with the web UI embedded; `lemon-note service install` registers a systemd / launchd / Windows service.
- **Docker**: `ghcr.io/lemonitcn/lemon-note` for linux/amd64 and linux/arm64, running as non-root with `/data` and `/cache` volumes.
- **Backups** to local folders, S3-compatible storage, Tencent COS, Alibaba OSS, WebDAV, SFTP and Baidu Netdisk, full or incremental, optionally encrypted.
- **MCP**: `claude mcp add --transport http lemon-note https://<host>/mcp` — clients that support OAuth 2.1 open a browser to authorize; alternatively pass `--header "Authorization: Bearer <token>"` with a personal access token created in Settings.

Quick start: download a release, run it, and open the one-time setup URL it prints (the desktop app shows it in the status window) to pick a database and create the admin account. The default address is http://127.0.0.1:8686. Build from source with Go 1.26+ and Node.js 22+ (`scripts/build-console.sh`, then `go build ./cmd/lemon-note`). Licensed under MIT.
