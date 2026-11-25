# Lemon Note 架构契约

本文是 lemon-note-service（Go）与 lemon-note-console（Vue 3）共同遵守的契约。改动数据格式、接口、块类型、工具名之前先改这里。

## 1. 产品定位

- 对标 Notion 的私有化部署笔记：页面树、块编辑、数据库（多视图）、评论、历史、回收站、模板、分享、导入导出。
- B/S 架构，第一期只有浏览器界面（电脑与手机完整适配）。
- 内置智能体（服务端 agent loop），同一套工具同时以 MCP 暴露给 Claude Code、Cursor、Codex 等外部智能体。
- 数据目录 = 全部数据。备份数据目录就能完整恢复；可以放在 OneDrive / 谷歌网盘等同步盘里。
- 定时备份到多种目标，支持全量与增量、可选加密。
- 界面中文 + 英文，主题可切换。

## 2. 仓库结构

```
lemon-note/
  docs/spec.md                 本文
  lemon-note-service/          Go 服务（命令行版与带界面版共用内核）
    cmd/lemon-note/            命令行入口
    cmd/lemon-note-gui/        带界面版（Wails v3，全部文件带 gui 构建标签，同一 go.mod；macOS / Linux 需要 CGO）
    Dockerfile                 服务镜像（构建上下文是仓库根目录）
    internal/...               见 §14
  scripts/                     本地构建与打包脚本（CI 也用）
  lemon-note-console/          Vue 3 + Vite 前端，dist 在打包时嵌进服务
  .github/workflows/build.yml  构建、测试、发布
  .secrets/                    本机测试密钥（已 gitignore，绝不提交）
```

## 3. 运行时目录

### 3.1 三个位置

| 位置 | 内容 | 能否放同步盘 | 能否删除 |
|---|---|---|---|
| 引导配置文件 | 数据目录在哪、监听地址、TLS、外部数据库连接串 | 否（本机专属） | 删了要重新指定数据目录 |
| 数据目录 | 全部持久数据（只写不可变文件） | 能 | 不能 |
| 缓存目录 | 工作数据库、全文索引、缩略图、日志、临时文件 | 否 | 能，启动时自动从数据目录重建 |

引导配置文件默认位置：

- Linux：root 运行时 `/etc/lemon-note/lemon-note.toml`，否则 `~/.config/lemon-note/lemon-note.toml`
- macOS：`~/Library/Application Support/Lemon Note/lemon-note.toml`
- Windows：以服务运行时 `%ProgramData%\Lemon Note\lemon-note.toml`，否则 `%APPDATA%\Lemon Note\lemon-note.toml`

数据目录默认：Linux root `/var/lib/lemon-note`、Linux 用户 `~/.local/share/lemon-note`、macOS `~/Library/Application Support/Lemon Note/data`、Windows 服务 `%ProgramData%\Lemon Note\data`、Windows 用户 `%LOCALAPPDATA%\Lemon Note\data`。

缓存目录默认：Linux root `/var/cache/lemon-note`、Linux 用户 `~/.cache/lemon-note`、macOS `~/Library/Caches/Lemon Note`、Windows `%LOCALAPPDATA%\Lemon Note\cache`（服务为 `%ProgramData%\Lemon Note\cache`）。缓存目录下按实例 ID 分子目录。

优先级：命令行参数 > 环境变量（`LEMON_NOTE_DATA_DIR` 等）> 引导配置文件 > 默认值。

### 3.2 数据目录格式（格式版本 1）

```
<数据目录>/
  lemon-note.json        实例信息，初始化时写一次：format、instance_id、engine、created_at
  MANIFEST               当前快照指针，原子替换写入
  config/secret.key      32 字节随机密钥，加密数据库里的第三方凭据（模型密钥、备份目标凭据）
  snapshots/<seq>.db     完整 SQLite 快照（seq 为 16 位补零十进制），写后不改
  journal/<first>-<last>.lnj   变更日志段，写后不改
  blobs/<sha256 前两位>/<sha256>   附件原文件，按内容寻址，写后不改
  locks/owner.json       占用者心跳（见 §3.5）
```

所有写入数据目录的文件都遵守：先写同目录下 `~$<随机>.tmp` 临时文件（OneDrive、Dropbox 不会上传 `~$` 开头的文件） → `fsync` → 原子改名 → `fsync` 目录（Windows 用 `MoveFileEx` 替换语义）。同步盘只会看到完整文件。读到 `~$` 开头的文件一律忽略，启动时清理超过 1 小时的残留。

`MANIFEST`（JSON）：

```json
{
  "format": 1,
  "instance_id": "...",
  "lineage": "...",                     
  "snapshot": "snapshots/0000000000001234.db",
  "snapshot_seq": 1234,
  "snapshot_sha256": "...",
  "snapshot_size": 1048576,
  "schema_version": 1,
  "written_at": 1764000000000,
  "writer_host": "..."
}
```

`lineage`：数据血统 ID。初始化或从备份恢复成新数据时重新生成；缓存目录里的工作库血统不一致就整体重建。

### 3.3 写入路径（事务外箱）

1. 业务代码在一个数据库事务里写业务表，所有写入必须走 `tx.Put(表, 行)` / `tx.Delete(表, id)`。
2. 同一事务里把本次全部变更按顺序写进 `_outbox`（seq 自增、变更 JSON）。业务表和外箱同生共死。
3. 封存器（后台协程）每 2 秒（有变更才动作，最长攒 30 秒或 4 MB）把外箱里连续的若干条写成一个日志段，原子落盘到 `journal/`，成功后删除外箱对应行。
4. 快照器在满足任一条件时出新快照：距上次快照的日志累计超过 16 MB；距上次快照超过 24 小时且有变更；正常退出且累计超过 1 MB；用户手动触发。
   - SQLite 模式：`VACUUM INTO` 写出一致性副本到缓存临时目录，算 sha256，复制进 `snapshots/` 临时名 → 原子改名。
   - MySQL / PostgreSQL 模式：在可重复读事务里把所有表导出到一个新建的 SQLite 文件（同一份表结构），其余同上。
   - 快照里 `meta.journal_seq` 精确表示已包含的最后一条变更。
   - 写完快照再原子替换 `MANIFEST`，之后删除早于「上一个快照」的快照与日志段（始终多留一代）。
5. 日志段过多时（超过 256 个）合并为大段，同样先写新再删旧。

日志段格式 `.lnj`：zstd 压缩的 NDJSON。

```
{"lnj":1,"first":101,"last":180,"lineage":"...","schema":1}
{"seq":101,"ts":1764000000000,"actor":"user-id","ops":[{"t":"pages","o":"put","id":"...","row":{...}},{"t":"blocks","o":"del","id":"..."}]}
...
{"end":true,"count":80,"sha256":"<前面所有行的 sha256>"}
```

只有带外箱的业务表进日志。纯本机表（会话、限流、OAuth 授权码、最近访问、导入任务、全文索引）不进日志，丢了无所谓。

### 3.4 启动与恢复

1. 读 `lemon-note.json`，不存在则进入首次设置。
2. 抢占用锁（§3.5），失败拒绝启动并提示占用者主机名。
3. 读 `MANIFEST`、列出 `journal/`，校验连续性（缺段报错并给出可恢复到的最大 seq）。
4. 打开工作库：
   - 工作库血统与数据目录一致，且工作库 seq ≥ 数据目录最大 seq：正常使用；外箱有残留先封存。
   - 工作库缺失、损坏、血统不一致或 seq 落后：从 `MANIFEST` 指向的快照复制出新工作库（MySQL/PG 模式则清空后导入），再按 seq 顺序重放之后的日志段。重放是幂等的整行 upsert / delete。
5. 全文索引、缩略图等缓存缺失时后台重建。

### 3.5 占用锁

- 本机互斥：缓存目录里的文件锁（flock / LockFileEx），防止同机启动两个实例。
- 跨机互斥：`locks/owner.json` 记录 host_id、主机名、pid、心跳时间，每 60 秒原子更新。启动时若发现别的 host_id 且心跳在 5 分钟内，拒绝启动；`--take-over` 可强制接管。同步盘有延迟，这是尽力而为的保护，界面上明确提示「同一数据目录同一时间只能在一台机器上运行」。
- 检测到同步盘冲突副本（例如 `MANIFEST-主机名`、`owner (1).json`）时在系统状态页告警。

### 3.6 附件

上传流式写缓存临时文件并计算 sha256 → 移入 `blobs/`（已存在则直接复用）→ `files` 表记录元数据与引用。附件不压缩不改写。引用计数为零且超过 30 天（覆盖回收站与历史版本保留期）才清理。

## 4. 数据库

- 引擎：SQLite（默认，纯 Go 驱动 modernc.org/sqlite，WAL 模式）、MySQL 8 / MariaDB 10.6+、PostgreSQL 13+。首次设置时选择，之后不可更改（`lemon-note.json.engine`）。外部库连接串存在引导配置文件，不进数据目录；换机器恢复时重新填写连接串，空库会从数据目录自动导入。
- 主键一律 26 位小写 ULID 字符串列 `id`；时间一律 Unix 毫秒 `BIGINT`；JSON 一律存文本；布尔用整数 0/1。SQL 只用三种引擎都支持的子集，占位符统一写 `?`，PostgreSQL 由驱动层改写。
- 表结构迁移只做加法（加表、加列、加索引）。日志重放按列名取值，不认识的列忽略。
- 全文索引独立存在缓存目录 `search.db`（SQLite FTS5 trigram，中英混合可搜），与主引擎无关，可随时重建。

### 4.1 进日志的表（业务数据）

| 表 | 说明 |
|---|---|
| users | 账号、口令哈希（argon2id）、系统角色（admin/member）、状态、语言、主题、两步验证（密钥加密存储）、恢复码哈希 |
| api_tokens | 个人访问令牌：名称、哈希、前缀、权限范围、限定空间、过期时间 |
| spaces | 空间：个人 / 共享，名称、图标、所有者 |
| space_members | 共享空间成员与角色 |
| pages | 页面与数据库（kind=page/database）；数据库行也是页面（database_id 非空）；标题、图标、封面、行属性值、排序键、各种开关、回收站标记 |
| blocks | 页面顶层块，一行一个块（§5） |
| databases | 数据库结构：属性定义、行号计数器 |
| db_views | 数据库视图：类型与配置（筛选、排序、分组、显示列、图表设置） |
| page_versions | 页面历史版本（压缩的块与属性快照） |
| comment_threads / comments | 评论 |
| favorites | 收藏 |
| files | 附件元数据 |
| page_shares | 页面分享：给用户（角色）或公开链接（令牌、口令、有效期、含子页面） |
| notifications | 收件箱：提及、评论、分享 |
| agent_conversations / agent_messages / agent_checkpoints | 智能体会话 |
| ai_providers | 模型供应商（密钥加密存储） |
| settings | 系统级与用户级设置（键值 JSON） |
| backup_targets / backup_runs | 备份目标（凭据加密存储）与执行记录 |
| audit_logs | 安全审计（登录、令牌、分享、权限、备份恢复） |

### 4.2 不进日志的表（本机）

sessions、oauth_clients、oauth_codes、oauth_tokens、login_attempts、recent_pages、import_jobs、_outbox、meta。

## 5. 块模型

### 5.1 存储

- 页面正文 = 有序的顶层块列表。`blocks` 一行存一个顶层节点的完整 ProseMirror JSON（含其内部嵌套，如列表项、分栏内容、折叠块内容）。
- 排序用分数索引字符串 `position`（与 rocicorp/fractional-indexing 同算法，Go 与 TS 各实现一份并共用测试向量）。
- 每个块有 `version`（每次写入 +1）。页面有 `content_rev`（任一块变化 +1）。

### 5.2 节点目录（ProseMirror schema，前后端与 Markdown 转换器一致）

节点名与属性名以本表为准（与 TipTap 默认命名尽量一致）。所有块级节点都可带 `attrs.id`（顶层块必有）；文字类块可带 `attrs.color`（文字色名或 `*_background` 背景色名）与 `attrs.textAlign`（left / center / right）。

| 节点 | 属性 | 内容 | 增强 Markdown |
|---|---|---|---|
| paragraph | color, textAlign | inline* | 普通段落；空段落写 `<empty-block/>` |
| heading | level 1-3 | inline* | `#` / `##` / `###` |
| bulletList | | listItem+ | `- ` |
| orderedList | start | listItem+ | `1. ` |
| listItem | | paragraph block* | 子块按列表标记宽度缩进 |
| taskList | | taskItem+ | |
| taskItem | checked | paragraph block* | `- [ ] ` / `- [x] ` |
| toggle | open, level 0-3 | toggleTitle toggleBody | `<details>` `<summary>…</summary>` … `</details>`；折叠标题 `<details heading="2">`；展开 `open` |
| toggleTitle | | inline* | |
| toggleBody | | block* | |
| blockquote | | block+ | `> ` |
| callout | icon, color | block+ | `<callout icon="…" color="…">` … `</callout>` |
| codeBlock | language | text* | 围栏代码块 |
| horizontalRule | | | `---` |
| image | src, alt, title, width, align | | `![alt](src "title")` 后接 `{width=480 align="center"}` |
| fileBlock | src, name, size, mime | | `<file src="…" name="…"/>` |
| video / audio | src, title | | `<video src="…"/>` / `<audio src="…"/>` |
| bookmark | url, title, description, image | | `<bookmark url="…"/>` |
| embed | url, height | | `<embed url="…" height="400"/>` |
| table | | tableRow+ | GFM 表格（第一行为表头，单元格内换行写 `<br>`） |
| tableRow | | (tableCell \| tableHeader)+ | |
| tableCell / tableHeader | colspan, rowspan, colwidth | paragraph+ | |
| columns | | column+ | `<columns>` `<column width="0.5">` … `</column>` … `</columns>` |
| column | width | block+ | |
| mathBlock | latex | | `$$` 换行 公式 换行 `$$` |
| mermaid | code | | 围栏语言 `mermaid` |
| graphviz | code, engine | | 围栏语言 `dot`（非 dot 引擎写 ` ```dot engine=neato`） |
| mindmap | data | | 围栏语言 `mindmap`，缩进列表，第一项为中心主题 |
| whiteboard | data, title | | `<whiteboard id="<块id>" title="…"/>` |
| chart | data | | 围栏语言 `chart`，内容为 JSON |
| childPage | pageId | | `<page id="…">标题</page>` |
| childDatabase | pageId, viewId | | `<database id="…" view="…">标题</database>` |
| linkToPage | pageId | | `<link-to-page id="…">标题</link-to-page>` |
| syncedBlock | sourceId（原件为空） | block* | 原件 `<synced-block id="<块id>">` … `</synced-block>`；引用 `<synced-block source="…"/>` |
| tableOfContents / breadcrumb | | | `<toc/>` / `<breadcrumb/>` |

行内：文字标记 bold `**`、italic `*`、underline `<u>`、strike `~~`、code `` ` ``、link `[](href)`、textColor `<span color="red">`、highlight `<mark color="yellow">`、subscript `<sub>`、superscript `<sup>`、comment（threadId，Markdown 里不显示）；行内节点 mention（kind=page/user/date，id，date，endDate）写作 `<mention-page id="…">标题</mention-page>`、`<mention-user id="…">名字</mention-user>`、`<mention-date date="2026-11-25" end="…"/>`；mathInline（latex）写作 `$…$`；hardBreak 写作 `<br>`。

### 5.3 实时编辑协议

- 浏览器在编辑器内容变化 400 ms 后，按块 id 与上次确认的状态比对，得出 `put`（新增或内容变了）、`del`、`move` 操作，`POST /api/pages/{id}/ops`，每个操作带 `baseVersion`。
- 服务端逐块应用。`baseVersion` 与当前版本不同即为并发冲突：后写者胜，但被覆盖的旧内容先存进页面历史（reason=conflict），响应里标出冲突块，前端轻提示并可在历史里找回。
- 服务端通过 WebSocket 向订阅了该页面的其他连接广播 `{type:"blocks", pageId, rev, ops, actor}`。前端只替换本地没有未提交改动的块。
- 智能体写页面走同一条路径，因此用户正在看的页面会实时出现智能体的修改。

## 6. 增强 Markdown（Lemon Markdown）

给智能体、MCP、导入导出、粘贴用。转换器只在服务端实现一份（Go），前端粘贴 Markdown 时调用 `POST /api/markdown/parse`。

基础：CommonMark + GFM（表格、任务列表、删除线、自动链接）。扩展：

| 内容 | 写法 |
|---|---|
| 标注框 | `<callout icon="lightbulb" color="blue">` 换行 Markdown 内容 换行 `</callout>` |
| 折叠块 | `<details>` `<summary>标题</summary>` 内容 `</details>`；折叠标题 `<details heading="2">` |
| 分栏 | `<columns>` `<column width="0.5">` … `</column>` … `</columns>` |
| 子页面 | `<page id="...">标题</page>` |
| 内嵌数据库 | `<database id="..." view="...">标题</database>` |
| 页面链接块 | `<link-to-page id="..."/>` |
| 同步块 | `<synced-block id="...">` 内容 `</synced-block>`；引用 `<synced-block source="..."/>` |
| 提及 | `<mention-page id="..."/>`、`<mention-user id="..."/>`、`<mention-date date="2026-11-25" end="..."/>` |
| 颜色 | 行内 `<span color="red">文字</span>`、`<mark color="yellow">文字</mark>`；块颜色写在块首行末尾 `{color="gray_background"}` |
| 下划线 | `<u>文字</u>` |
| 公式 | 行内 `$x^2$`，块 `$$` 换行 公式 换行 `$$` |
| 图片 | `![说明](lemon://file/<文件id> "标题")`，宽度 `{width=480}` 跟在后面 |
| 附件 / 视频 / 音频 | `<file src="lemon://file/<id>" name="..."/>`、`<video src="..."/>`、`<audio src="..."/>` |
| 书签 / 嵌入 | `<bookmark url="..."/>`、`<embed url="..." height="400"/>` |
| 代码 | 围栏代码块，语言写在围栏后 |
| Mermaid / Graphviz | 围栏语言 `mermaid` / `dot` |
| 思维导图 | 围栏语言 `mindmap`，内容为缩进列表，第一项为中心主题 |
| 图表 | 围栏语言 `chart`，内容为 JSON（§8.4） |
| 白板 | `<whiteboard id="<块id>" title="..."/>`（只读摘要，内容用白板工具改） |
| 目录 / 面包屑 | `<toc/>`、`<breadcrumb/>` |
| 空行块 | `<empty-block/>` |

转换规则：

- 渲染（块 → Markdown）时每个顶层块之间空一行；智能体看到的 Markdown 不含块 id。
- 解析（Markdown → 块）时，自定义标签不要求独占一行，转换器先做规范化。
- 局部修改（`edit_page`）：对整页 Markdown 做字符串替换后重新切分顶层块，与原块按 Markdown 文本做最长公共子序列对齐。文本没变的块原样保留原 JSON（id、评论标记、颜色等全部无损）；变了的块按位置继承原 id 后重新解析；多出来的新建，少了的删除。

## 7. 权限

- 系统角色：admin（管理用户与系统设置）/ member。**管理员不能读别人的个人空间。**
- 空间角色：owner > admin > editor > commenter > viewer。个人空间只有所有者。
- 页面分享给用户：授予该页面及其子孙页面的角色。实际角色 = max(空间角色, 自己及祖先页面上的分享角色)。
- 公开链接：令牌只读访问该页面（可含子页面），可设口令与有效期，默认禁止搜索引擎收录。公开页面里指向未公开页面的提及、链接、子页面一律显示为「无权访问」，不泄露标题。
  - 分享（用户与公开链接）只有对页面有完全访问（admin）的人能改；被分享的人可以自己退出。每页最多一个有效的公开链接，可重置令牌（旧链接立即失效）。
  - 页面外壳 `/share/<令牌>[/<页面 id>]`（`Referrer-Policy: no-referrer`，默认 `X-Robots-Tag: noindex`）；匿名接口统一在 `/api/public/<令牌>/` 下：`GET /`（站点名、是否要口令、范围内页面树）、`POST /unlock`、`GET /pages/{id}`、`GET /databases/{id}`、`POST /databases/{id}/query`（只按视图查询，不接受临时条件）、`POST /charts/query`、`GET /files/{id}`。
  - 范围 = 分享的页面（含子页面时加子孙）+ 范围内页面里的内嵌数据库及其行。数据库里关联到范围外数据库的关联、经由它的汇总、引用了这些属性的公式整列去掉；评论标记去掉。
  - 口令用 argon2id 存哈希；解锁凭据是用实例密钥加密的 Cookie（路径限定在该令牌的接口下、12 小时、口令改了即失效）；解锁按 IP + 链接限流。管理员关闭「允许公开链接」后全部公开接口立即返回不存在。
- 收件箱（notifications）：页面分享给我、评论线程有新回复、正文里新 @ 到我时写一条；只给能看到该页面的人发，不发给自己；`GET /api/notifications`、`POST /api/notifications/read`，新通知经实时连接推送。
- 智能体和 MCP 以用户身份行事，权限检查与界面完全相同；令牌可再收窄到只读或指定空间。
- 所有查询在数据层强制带权限过滤，权限判断集中在一个包里并有隔离测试。

## 8. 内容类型细节

### 8.1 数据库

- 属性类型：title、text、number（格式）、select、multi_select、status（分组）、date（含结束、时间、时区）、person、files、checkbox、url、email、phone、formula、relation（可双向）、rollup、created_time、created_by、last_edited_time、last_edited_by、unique_id（前缀）、button。
- 视图：table、board、list、gallery、calendar、timeline、chart。每个视图独立的筛选（与/或嵌套）、排序、分组、子分组、显示属性、列宽、卡片封面、日期属性等。
- 查询在服务端内存中完成（按数据库缓存，写入即失效），公式、关联、汇总也在服务端计算，界面、智能体、MCP 拿到同样结果。
- 公式语言兼容 Notion 公式 2.0 的常用子集（运算、条件、文本、数字、日期、列表、prop() 与属性名引用）。
- 行模板、数据库模板、链接视图（同一数据库在别的页面再开视图）。

### 8.2 思维导图

块数据：`{root:{id,text,children:[...],style?,note?,collapsed?}, layout, theme}`。界面用 simple-mind-map 编辑。Markdown 表示为缩进列表；重新解析时按「从根到节点的文本路径」继承原节点的 id、样式和备注。

### 8.3 白板

块数据：Excalidraw 的 elements、appState 子集、files（files 中的图片落到附件）。智能体用 `whiteboard_read`（列出元素：id、类型、文字、位置、连线关系）与 `whiteboard_edit`（简化元素语法增删改，支持按 Mermaid 生成）操作。

### 8.4 图表

```json
{"type":"bar","title":"...","source":{"kind":"database","databaseId":"...","viewId":"...","x":"属性名","y":[{"property":"属性名","agg":"sum"}],"groupBy":"属性名"}}
{"type":"line","title":"...","source":{"kind":"static","option":{ ...ECharts option... }}}
```

## 9. 智能体

### 9.1 总体

- agent loop 运行在服务端协程里，浏览器断开不影响执行，重连按事件序号续传（SSE + Last-Event-ID）。
- 工具注册表只有一份：内部智能体调用、MCP 暴露、外部 API 共用同一个实现与同一份权限检查。智能体额外拥有「智能体专用工具」（计划、提问、子智能体、界面联动）。
- 会话、消息、工具调用与结果、检查点全部持久化（进日志，备份可恢复）。

### 9.2 模型供应商

- 第一期只有 OpenCode Go。启动时与每小时拉一次 `GET https://opencode.ai/zen/go/v1/models`，按每个模型的 `recommended_api` 选择协议适配器与 `base_url`，并读取上下文长度、输出上限、输入模态、推理档位、推理字段名。
- 适配器三种：OpenAI Chat Completions、Anthropic Messages、OpenAI Responses，统一转换成内部的消息与流事件。以后加别的供应商只是再配一个适配器。
- 请求必带 `x-opencode-session`（会话 ID，纯 ASCII）与 `User-Agent: lemon-note/<版本>`。
- 首个分片之前的 429 / 5xx / 网络错误指数退避重试；流中途断开整步重试（最多 2 次）。正文里泄漏的 `</tool_call>` 等字面量在展示前清洗。

### 9.3 一轮执行

```
用户消息（含引用）入库
循环（默认最多 200 步）：
  组装上下文：系统提示 + 提醒 + 历史（必要时先压缩）
  流式请求模型 → 实时推送正文、思考、工具调用参数
  没有工具调用 → 本轮结束
  执行工具：只读工具并发，写入工具按顺序；按权限模式可能暂停等用户批准
  工具结果入库并推送
  用户在执行中追加的消息作为新的用户消息插入
  用户点停止 → 立即中断当前请求与工具，记录中断
```

- 权限模式（输入框旁切换）：只读规划（写工具全部禁用）/ 每次写入确认 / 删除确认（默认）/ 全自动。
- 先读后改：`edit_page` 要求本会话读过该页面，且读取后页面没被别人改过，否则报错让智能体重新读取（同 Claude Code 的文件过期检查）。
- 检查点：一轮里第一次写某个页面之前，先存该页面历史版本（reason=ai）。界面「撤销本轮修改」把本轮动过的页面恢复到轮前版本，本轮新建的移入回收站、删除的恢复；若用户在之后又改过，先提示。
- 上下文管理：工作预算默认 16 万 token（可配，不超过模型上限的 80%）。超出先把较早的工具结果替换成占位说明（「已省略，需要时重新读取」），仍超出再用模型把较早对话压缩成结构化摘要，保留最近若干轮原文。token 数先估算，再用上一次请求返回的真实用量校准。
- 子智能体：`task` 工具开一个独立上下文的子智能体（默认只读工具），并发执行，返回最终总结，只允许一层。
- 提醒注入：计划清单状态、读过的页面已被他人修改、用户刚在界面里打开或选中了什么等，以系统提醒附在下一次请求里。

### 9.4 上下文与引用

- 引用来源：从页面树拖入节点（页面、数据库、数据库行）、在编辑器里选中一段「添加到聊天」、数据库视图、评论、附件、图片。
- 引用随消息存储为结构化附件（类型、id、选区块 id 与文字偏移、快照文字）。组装上下文时：小内容直接内联（单个引用上限约 8 千 token），大内容只给大纲与读取提示。
- 系统提示包含：身份与行为准则、增强 Markdown 速查、工具使用准则、当前用户与空间、用户当前打开的页面、日期时间与时区、界面语言、用户自定义指令（设置里的「智能体指令」）与空间级指令。

### 9.5 工具清单

通用（智能体与 MCP 都有）：

| 工具 | 作用 |
|---|---|
| search | 全文搜索页面、数据库行、评论；可按空间、上级页面、类型、编辑时间过滤；返回片段与 id |
| list_pages | 列出空间或页面下的页面树（可指定深度），含类型、子页面数、更新时间 |
| read_page | 读页面：属性 + 增强 Markdown 正文；支持按行偏移分段读；返回 content_rev |
| grep_pages | 在某个页面子树里按关键字或正则查找，返回匹配行与所在页面 |
| create_pages | 批量新建页面或数据库行：上级、标题、图标、属性、Markdown 正文、位置 |
| edit_page | 对页面 Markdown 做精确字符串替换（old/new，可 replace_all，一次多处），旧文本不唯一时报错要求加上下文 |
| write_page | 整页替换正文，或在指定位置（开头、结尾、某段文字之后）插入 Markdown |
| update_page | 改标题、图标、封面、属性值、锁定、全宽等 |
| move_pages | 移动到新上级或调整顺序 |
| duplicate_page | 复制页面（可含子页面） |
| delete_pages / restore_pages | 移入回收站 / 从回收站恢复 |
| create_database | 新建数据库：属性结构、初始视图、内嵌或整页 |
| update_database | 增删改属性、选项、公式、关联 |
| query_database | 按结构化筛选、排序、分页查询，或直接用某个视图的条件；返回紧凑表格 |
| upsert_rows | 批量新建或更新数据库行（属性按名称写） |
| manage_views | 新建、修改、删除视图 |
| whiteboard_read / whiteboard_edit | 白板元素读写 |
| list_comments / add_comment / resolve_comment | 评论 |
| page_history / restore_page_version | 历史版本 |
| list_spaces / list_users | 空间与成员 |
| read_file | 读附件：文本类直接返回，图片按模型能力给视觉输入 |
| web_fetch | 抓取网页转 Markdown（拦截内网地址与重定向到内网） |
| web_search | 管理员配置了搜索服务（SearXNG / Tavily / 博查）后才出现 |

智能体专用：

| 工具 | 作用 |
|---|---|
| todo_write | 维护本轮计划清单，界面显示为计划卡片 |
| ask_user | 结构化提问（单选 / 多选 / 自由输入），暂停等待用户回答 |
| task | 开子智能体 |
| ui_open | 让用户的浏览器打开某个页面或定位到某个块（浏览器执行） |
| render_check | 让用户的浏览器渲染 Mermaid / Graphviz / 图表 / 思维导图并回报错误（浏览器执行；没有在线浏览器时返回「跳过」） |

### 9.6 MCP

- 端点 `/mcp`，Streamable HTTP 传输（官方 Go SDK）。
- 认证：个人访问令牌（`Authorization: Bearer ln_pat_...`），或 OAuth 2.1（授权码 + PKCE + 动态客户端注册，授权页让用户选择只读 / 读写与可访问的空间）。
  - 元数据：`/.well-known/oauth-protected-resource`（RFC 9728，未授权时 `WWW-Authenticate` 带 resource_metadata）、`/.well-known/oauth-authorization-server`（RFC 8414）；端点 `/oauth/register`、`/oauth/authorize`（前端确认页，要登录）、`/oauth/token`、`/oauth/revoke`。
  - 只支持公开客户端与 S256；回调地址只允许 https、本机回环 http（按 RFC 8252 忽略端口）和桌面应用自定义协议。授权码 10 分钟一次性，重放即作废已发出的令牌；访问令牌 `ln_oat_` 1 小时；刷新令牌 `ln_ort_` 60 天、每次刷新轮换，旧的被重用整条链作废。令牌只存哈希。
  - 这些都是本机表（不进日志）：换机器恢复后客户端重新授权。用户在「设置 → 访问令牌 → 已授权的应用」里撤销。
- 工具与 §9.5 通用工具完全一致，带 readOnlyHint / destructiveHint 标注。

## 10. 备份

### 10.1 目标

本地目录 / NAS 挂载盘、S3 兼容、腾讯云 COS、阿里云 OSS、WebDAV、SFTP、百度网盘。统一接口：

```go
type Target interface {
    Put(ctx, path string, r io.Reader, size int64) error
    Get(ctx, path string) (io.ReadCloser, error)
    List(ctx, prefix string) ([]Entry, error)
    Delete(ctx, path string) error
    Stat(ctx, path string) (Entry, error)
}
```

COS 与 OSS 走各自的 S3 兼容接口（虚拟主机风格），界面上是独立类型（按地域生成端点）。百度网盘由用户在百度网盘开放平台自建应用填 AppKey / SecretKey，用设备码授权，令牌自动续期；开源程序不内置任何密钥。

### 10.2 模式

- 全量：每次生成一个独立备份包 `lemon-note-<实例前 8 位>-<UTC 时间>.lnbak`（tar + zstd，可选加密），包含当时的快照、之后的日志段、被引用的附件、`lemon-note.json`、`MANIFEST`、`config/secret.key`；tar 第一个条目是包信息，最后一个条目是全部文件的大小与 sha256 索引。包按固定大小分卷（默认 1 GB，可设 16–4096 MB）：前面的卷名为 `.lnbak.0001`、`.0002`…，最后一卷就叫 `.lnbak` 且最后上传，所以它存在就说明整包完整。未加密的包可以手工按顺序拼接后 `zstd -d | tar -x` 还原。保留策略：保留最近 N 个，或按天 / 周 / 月分层保留。
- 增量（镜像）：目标上的子目录 `mirror-<实例前 8 位>/` 里维护一份与数据目录同结构的副本（放子目录而不是目标根，避免目标根的 `lemon-note-backup.json` 被当成数据目录里的冲突副本，也让多个实例可以共用一个目标），目标根另有指针文件 `lemon-note-<实例前 8 位>-mirror.json` 供备份列表发现镜像。只上传新增文件（不可变文件按名字判断；目标上 MANIFEST 可读且血统一致时才复用同名快照与日志段，附件按内容哈希命名总是复用），删除源端已删除的文件（只在新 MANIFEST 上传之后删，只删日志段、快照、附件）。上传顺序：附件 → 日志段 → 快照 → `MANIFEST` 最后，所以任何时刻目标上的副本都能直接当数据目录用。
- 备份进行期间持有「备份租约」，快照器暂停清理旧文件，保证拷贝的文件集合一致。

### 10.3 加密

目标可设备份口令：argon2id 派生密钥，文件内容用 AES-256-GCM 分块流式加密（64 KB 一块，块序号进附加数据，防截断与重排）。目标根目录写 `lemon-note-backup.json` 记录算法、盐和口令校验值。镜像模式下文件名保持原样（文件名本身是哈希与序号，不含内容信息）。忘记口令无法恢复，界面明确告知。

### 10.4 计划与恢复

- 每个目标独立计划：每 N 小时 / 每天几点 / 每周几几点；也可手动执行。执行记录含耗时、文件数、字节数、错误。
- 恢复：选目标 → 列出备份 → 选一个 → 恢复到新的数据目录（默认）或覆盖当前数据目录（二次确认，先自动做一次本地全量备份）。恢复后血统更新、服务重启。
- 校验：可对目标执行完整性校验（逐文件 sha256 / 解密校验）。

## 11. 导入导出

- 导入：Notion 导出包（zip，Markdown + CSV）、Notion API（集成密钥，保留属性类型、关联、标注框、分栏等）、Obsidian 库（zip 或文件夹上传：双链、嵌入、附件、属性区、标签、Canvas 转白板）、通用 Markdown 文件夹、CSV（建数据库）。导入在后台任务里执行，有进度与报告（哪些内容降级了）。
- 导出：页面或整个空间导出为 Markdown + CSV + 附件 zip、HTML；单页可浏览器打印为 PDF；整站导出为 Lemon Note 备份包。

接口与约定（`internal/importer`、`internal/exporter`）：

| 接口 | 说明 |
|---|---|
| `POST /api/import` | multipart，字段按顺序 `kind`（notion_zip / obsidian / markdown / csv）、`space_id`、`parent_id`、`file`（放最后）；返回 `{job}` |
| `POST /api/import/notion-api` | JSON `{token, root_page_id, space_id, parent_id}`；密钥只在任务内存里用，不落盘、不进日志与报告 |
| `GET /api/import/{id}` | 任务进度与报告，只能看自己的任务 |
| `GET /api/pages/{id}/export?format=md\|html&children=1` | 单页、无子页面、无附件时下载单个 .md / .html；否则流式下载 zip |

- 任务：`{id, kind, status: queued|running|done|failed, progress: {phase, done, total, current}, report: {pages, databases, rows, files, root_ids, warnings: [{code, path, detail, message}], omitted_warnings, error}}`。同一用户同时一个任务，全站同时最多 3 个。
- 导入目标是空间根或某个普通页面（需要编辑权限，不能是数据库）。先读来源生成计划树并预先分配全部页面 id，再按层级分批建页（上级提交后才建下级）。
- 安全：zip 路径一律规范化，含 `..`、绝对路径、盘符的条目与符号链接跳过；文件数、解压总量、内层 zip 个数都有上限；附件大小上限取系统设置 `max_upload_mb`；外部请求（Notion API、文件下载）走防 SSRF 客户端。
- 导出 zip 布局：`标题.md`，子页面在同名文件夹 `标题/` 里（重名加「 (2)」），数据库是 `标题.csv`（显示值）加 `标题/行.md`，附件在 `files/`，正文里的站内链接与 `lemon://file/<id>` 改成相对路径。这个布局可以用 Markdown 导入原样导回来。只导出当前用户能读的页面与附件。

## 12. 安全

- 口令 argon2id；登录按 IP 与账号双维度限流、逐步延迟；错误信息不区分「用户不存在」与「口令错误」。
- 两步验证（TOTP + 一次性恢复码）用户自选；管理员可设「全员强制」。会话列表可查看与注销；空闲超时与绝对超时可配。
- 会话 Cookie：HttpOnly、SameSite=Lax、HTTPS 下 Secure；所有改动类请求校验 Origin 并带 CSRF 令牌。
- 个人访问令牌只存哈希，创建时显示一次。
- 第三方凭据（模型密钥、备份目标凭据、Notion 密钥）用 `config/secret.key` 以 AES-256-GCM 加密后入库，接口永不回传明文。
- 响应头：严格 CSP、`X-Content-Type-Options: nosniff`、`Referrer-Policy`、`frame-ancestors 'none'`（公开页面可配置允许嵌入）、HTTPS 下 HSTS。
- 用户上传的文件：非图片一律 `Content-Disposition: attachment`；SVG 与 HTML 加 `Content-Security-Policy: sandbox`。
- 服务端发起的外部请求（网页抓取、书签预览、URL 导入、搜索）统一经过防 SSRF 的拨号器：解析后拦截回环、私有、链路本地、组播等地址，重定向逐跳检查，限制大小与时长。
- 内嵌第三方页面一律 sandbox iframe；Mermaid 用 strict 安全级别；导入的 HTML 走白名单清洗。
- TLS：可直接配置证书、可启用 ACME 自动证书，也可放在反向代理后（受信代理列表控制 X-Forwarded-For）。
- 审计日志记录登录成功 / 失败、两步验证变更、令牌增删、分享与公开链接、权限变化、备份与恢复。
- 稳定性：所有请求有超时与恐慌恢复；磁盘满时拒绝写入并在界面醒目提示；数据目录不可写时只读运行。

## 13. 前端

- Vue 3 + TypeScript + Vite + Pinia + Vue Router + vue-i18n（zh-CN / en），无头组件用 reka-ui，样式自建设计令牌（CSS 变量）。
- 编辑器：TipTap 3（ProseMirror），节点与 §5.2 一致；斜杠菜单、块拖拽把手、浮动格式栏、提及、粘贴 Markdown、拖拽上传。
- 图：mermaid、@hpcc-js/wasm-graphviz、KaTeX、simple-mind-map、Excalidraw（React 岛，按需加载）、ECharts。重型依赖全部按需懒加载。
- 布局（电脑）：最左固定竖条（智能体开关等）→ 智能体面板（展开时把右侧主界面挤窄，可拖宽）→ 侧栏（空间、搜索、收藏、页面树、回收站、设置）→ 主区（顶栏面包屑与操作 + 页面）。手机：侧栏与智能体面板都是抽屉。
- 主题：浅色 / 深色 / 跟随系统 + 强调色 + 字体（默认 / 衬线 / 等宽）+ 界面密度；按用户保存。
- 界面规范：图标全部自绘 SVG，不用 emoji 当图标；不堆说明小字，需要说明的地方放 i 图标，点开 popover。

## 14. 服务端代码结构

```
lemon-note-service/
  cmd/lemon-note/          命令行：serve、setup、service install|uninstall|start|stop|status、backup run|list|restore、
                           admin reset-password|reset-2fa、doctor、version
  cmd/lemon-note-gui/      带界面版（Wails v3，构建标签 gui）：托盘图标 + 状态窗口（运行状态、地址、数据目录、
                           启动 / 停止、更换数据目录、开机自启、打开控制台），进程内调用 internal/app
  internal/
    app/        组装与生命周期（命令行和带界面版共用 Start / Stop）
    config/     引导配置与默认路径
    datadir/    数据目录布局、占用锁、MANIFEST、附件存储、原子写
    store/      引擎、表结构、迁移、事务与外箱、封存器、快照器、重建
    ids/  fracidx/  公共小工具
    auth/       口令、会话、两步验证、令牌、OAuth、限流
    perm/       权限判断
    pages/      页面、块、树、回收站、历史、模板
    dbs/        数据库结构、视图、查询、公式、关联与汇总
    lnmd/       增强 Markdown 与块 JSON 互转
    search/     全文索引
    realtime/   WebSocket 广播
    files/      上传、缩略图、下载
    share/      分享与公开页面
    llm/        供应商与三种协议适配器
    agent/      agent loop、上下文、压缩、会话存储、检查点、子智能体
    tools/      工具注册表与实现（智能体与 MCP 共用）
    mcp/        MCP 端点与 OAuth
    netsafe/    防 SSRF 拨号器、网页转 Markdown、搜索服务
    backup/     备份引擎、计划、加密、目标实现
    importer/   Notion、Obsidian、Markdown、CSV
    exporter/   导出
    web/        路由、中间件、接口、静态前端（go:embed）
```

## 15. 产物与发布

| 产物 | 平台 |
|---|---|
| 命令行版 `lemon-note` | Linux amd64 / arm64 / armv7 / 386 / riscv64 / loong64 / ppc64le / s390x；Windows amd64 / arm64；macOS 通用（Apple 芯片 + Intel） |
| 带界面版 `Lemon Note` | Windows amd64 / arm64（安装包与免安装 zip）；macOS 通用 .app（DMG）；Linux amd64 / arm64（deb 与 AppImage） |
| Docker 镜像 | linux/amd64、linux/arm64（GHCR） |

- 命令行版纯 Go、关闭 CGO，单文件，内嵌前端。
- 带界面版托盘图标常驻（Windows 托盘、macOS 菜单栏、Linux AppIndicator），窗口关闭只是隐藏。
- 命令行版可 `service install` 装成系统服务（systemd / launchd / Windows 服务）。
- GitHub Actions：每次推送 main 与每个 PR 都构建全部产物并跑测试；推送 main 全部通过后自动发布 Release「Lemon Note <版本>.<构建序号>」并标为最新；推送 v* 标签按标签版本发布。签名证书通过仓库 Secrets 可选配置，未配置则跳过签名。
