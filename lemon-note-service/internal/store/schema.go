package store

// ColType 是与引擎无关的列类型。
type ColType int

const (
	TID     ColType = iota // 26 位 ULID
	TKey                   // 可建索引的短字符串（<=191）
	TKeyBin                // 排序键：严格按字节比较
	TText                  // 长文本 / JSON
	TInt                   // 64 位整数（时间一律毫秒）
	TBool                  // 0/1
	TReal                  // 双精度
	TBlob                  // 二进制（日志里以 base64 表示）
)

type Column struct {
	Name     string
	Type     ColType
	Nullable bool
}

type Index struct {
	Name    string
	Columns []string
	Unique  bool
}

// Table 描述一张表。Journaled 的表必须以字符串 id 为主键，所有写入经过外箱进日志。
type Table struct {
	Name      string
	Journaled bool
	Columns   []Column
	Indexes   []Index
	// PKInt 表示主键是整数列（只用于内部表 _outbox / _seq）
	PKInt bool
}

func (t *Table) Column(name string) (Column, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return Column{}, false
}

func id() Column                             { return Column{Name: "id", Type: TID} }
func key(n string) Column                    { return Column{Name: n, Type: TKey} }
func nkey(n string) Column                   { return Column{Name: n, Type: TKey, Nullable: true} }
func ref(n string) Column                    { return Column{Name: n, Type: TID} }
func nref(n string) Column                   { return Column{Name: n, Type: TID, Nullable: true} }
func text(n string) Column                   { return Column{Name: n, Type: TText} }
func ntext(n string) Column                  { return Column{Name: n, Type: TText, Nullable: true} }
func integer(n string) Column                { return Column{Name: n, Type: TInt} }
func ninteger(n string) Column               { return Column{Name: n, Type: TInt, Nullable: true} }
func boolean(n string) Column                { return Column{Name: n, Type: TBool} }
func pos(n string) Column                    { return Column{Name: n, Type: TKeyBin} }
func blob(n string) Column                   { return Column{Name: n, Type: TBlob} }
func idx(name string, cols ...string) Index  { return Index{Name: name, Columns: cols} }
func uidx(name string, cols ...string) Index { return Index{Name: name, Columns: cols, Unique: true} }

func timestamps() []Column {
	return []Column{integer("created_at"), integer("updated_at")}
}

func cols(groups ...[]Column) []Column {
	var out []Column
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func one(c ...Column) []Column { return c }

// SchemaVersion 只做加法：加表、加列、加索引。
const SchemaVersion = 1

// Tables 是全部表定义。顺序即建表顺序。
var Tables = []*Table{
	// ---------- 业务数据（进日志） ----------
	{Name: "users", Journaled: true, Columns: cols(one(id(), key("username"), text("display_name"), nkey("email"), text("avatar"),
		text("password_hash"), key("role"), key("status"), key("locale"), text("prefs"),
		text("totp_secret"), boolean("totp_enabled"), text("recovery_codes"),
		integer("password_changed_at"), ninteger("last_login_at")), timestamps()),
		Indexes: []Index{uidx("ux_users_username", "username")}},
	{Name: "api_tokens", Journaled: true, Columns: cols(one(id(), ref("user_id"), text("name"), key("prefix"), key("token_hash"),
		text("scopes"), text("space_ids"), ninteger("expires_at"), ninteger("last_used_at"), integer("created_at"), ninteger("revoked_at"))),
		Indexes: []Index{uidx("ux_api_tokens_hash", "token_hash"), idx("ix_api_tokens_user", "user_id")}},
	{Name: "spaces", Journaled: true, Columns: cols(one(id(), key("kind"), text("name"), text("icon"), ref("owner_id"), text("settings")),
		timestamps(), one(ninteger("deleted_at"))),
		Indexes: []Index{idx("ix_spaces_owner", "owner_id")}},
	{Name: "space_members", Journaled: true, Columns: cols(one(id(), ref("space_id"), ref("user_id"), key("role")), timestamps()),
		Indexes: []Index{uidx("ux_space_members", "space_id", "user_id"), idx("ix_space_members_user", "user_id")}},
	{Name: "pages", Journaled: true, Columns: cols(one(id(), ref("space_id"), nref("parent_id"), nref("database_id"), key("kind"),
		text("title"), text("icon"), text("cover"), text("props"), pos("position"), text("flags"), boolean("is_template"),
		integer("content_rev"), integer("created_at"), ref("created_by"), integer("updated_at"), ref("updated_by"),
		ninteger("trashed_at"), nref("trashed_by"), nref("trash_root"))),
		Indexes: []Index{idx("ix_pages_parent", "space_id", "parent_id"), idx("ix_pages_database", "database_id"), idx("ix_pages_trashed", "trashed_at")}},
	{Name: "blocks", Journaled: true, Columns: cols(one(id(), ref("page_id"), pos("position"), key("type"), text("data"), integer("version"),
		integer("created_at"), ref("created_by"), integer("updated_at"), ref("updated_by"))),
		Indexes: []Index{idx("ix_blocks_page", "page_id")}},
	{Name: "databases", Journaled: true, Columns: cols(one(id(), text("schema"), text("settings"), integer("row_counter")), timestamps())},
	{Name: "db_views", Journaled: true, Columns: cols(one(id(), ref("database_id"), nref("host_id"), text("name"), key("type"), pos("position"),
		text("config"), ref("created_by")), timestamps()),
		Indexes: []Index{idx("ix_db_views_db", "database_id"), idx("ix_db_views_host", "host_id")}},
	{Name: "page_versions", Journaled: true, Columns: cols(one(id(), ref("page_id"), key("reason"), text("title"), blob("data"), integer("size"),
		text("meta"), integer("created_at"), ref("created_by"))),
		Indexes: []Index{idx("ix_page_versions_page", "page_id", "created_at")}},
	{Name: "comment_threads", Journaled: true, Columns: cols(one(id(), ref("page_id"), nref("block_id"), text("anchor"),
		ninteger("resolved_at"), nref("resolved_by"), integer("created_at"), ref("created_by"))),
		Indexes: []Index{idx("ix_comment_threads_page", "page_id")}},
	{Name: "comments", Journaled: true, Columns: cols(one(id(), ref("thread_id"), ref("page_id"), text("body"), integer("created_at"), ref("created_by"),
		integer("updated_at"), ninteger("deleted_at"))),
		Indexes: []Index{idx("ix_comments_thread", "thread_id"), idx("ix_comments_page", "page_id")}},
	{Name: "favorites", Journaled: true, Columns: cols(one(id(), ref("user_id"), ref("page_id"), pos("position"), integer("created_at"))),
		Indexes: []Index{uidx("ux_favorites", "user_id", "page_id")}},
	{Name: "files", Journaled: true, Columns: cols(one(id(), ref("space_id"), nref("page_id"), key("sha256"), integer("size"), key("mime"), text("name"),
		integer("width"), integer("height"), integer("created_at"), ref("created_by"), ninteger("deleted_at"))),
		Indexes: []Index{idx("ix_files_sha", "sha256"), idx("ix_files_page", "page_id")}},
	{Name: "page_shares", Journaled: true, Columns: cols(one(id(), ref("page_id"), ref("space_id"), key("kind"), nref("user_id"), key("role"),
		nkey("token"), text("password_hash"), ninteger("expires_at"), boolean("include_children"), boolean("allow_index"), text("settings"),
		integer("created_at"), ref("created_by"), ninteger("revoked_at"))),
		Indexes: []Index{idx("ix_page_shares_page", "page_id"), idx("ix_page_shares_user", "user_id"), idx("ix_page_shares_token", "token")}},
	{Name: "notifications", Journaled: true, Columns: cols(one(id(), ref("user_id"), key("kind"), nref("page_id"), nref("actor_id"), text("data"),
		ninteger("read_at"), integer("created_at"))),
		Indexes: []Index{idx("ix_notifications_user", "user_id", "created_at")}},
	{Name: "agent_conversations", Journaled: true, Columns: cols(one(id(), ref("user_id"), nref("space_id"), text("title"), key("model"),
		text("settings"), text("state"), text("usage")), timestamps(), one(ninteger("archived_at"))),
		Indexes: []Index{idx("ix_agent_conv_user", "user_id", "updated_at")}},
	{Name: "agent_messages", Journaled: true, Columns: cols(one(id(), ref("conversation_id"), integer("seq"), key("role"), text("parts"),
		text("meta"), integer("created_at"))),
		Indexes: []Index{idx("ix_agent_msg_conv", "conversation_id", "seq")}},
	{Name: "agent_checkpoints", Journaled: true, Columns: cols(one(id(), ref("conversation_id"), key("run_id"), nref("message_id"), text("changes"),
		integer("created_at"), ninteger("reverted_at"))),
		Indexes: []Index{idx("ix_agent_ckpt_conv", "conversation_id")}},
	{Name: "ai_providers", Journaled: true, Columns: cols(one(id(), key("kind"), text("name"), text("base_url"), text("api_key"), text("settings"),
		boolean("enabled")), timestamps())},
	{Name: "settings", Journaled: true, Columns: cols(one(key("id"), text("value"), integer("updated_at")))},
	{Name: "backup_targets", Journaled: true, Columns: cols(one(id(), text("name"), key("kind"), text("config"), key("mode"), text("schedule"),
		text("retention"), text("encryption"), boolean("enabled"), ref("created_by"), ninteger("last_run_at"), key("last_status")), timestamps())},
	{Name: "backup_runs", Journaled: true, Columns: cols(one(id(), ref("target_id"), key("kind"), integer("started_at"), ninteger("finished_at"),
		key("status"), integer("files"), integer("bytes"), text("error"), text("log"), text("artifact"))),
		Indexes: []Index{idx("ix_backup_runs_target", "target_id", "started_at")}},
	{Name: "audit_logs", Journaled: true, Columns: cols(one(id(), integer("ts"), nref("user_id"), key("action"), text("ip"), text("ua"),
		text("target"), text("detail"))),
		Indexes: []Index{idx("ix_audit_ts", "ts"), idx("ix_audit_user", "user_id", "ts")}},

	// ---------- 本机数据（不进日志） ----------
	{Name: "sessions", Columns: cols(one(key("id"), ref("user_id"), integer("created_at"), integer("last_seen_at"), integer("expires_at"),
		text("ip"), text("ua"), boolean("mfa_ok"), text("csrf"), text("data"))),
		Indexes: []Index{idx("ix_sessions_user", "user_id")}},
	{Name: "login_attempts", Columns: cols(one(key("id"), integer("failures"), integer("first_at"), integer("last_at"), integer("locked_until")))},
	{Name: "oauth_clients", Columns: cols(one(key("id"), text("name"), text("redirect_uris"), text("data"), integer("created_at")))},
	{Name: "oauth_codes", Columns: cols(one(key("id"), key("client_id"), ref("user_id"), text("data"), integer("expires_at")))},
	{Name: "oauth_tokens", Columns: cols(one(key("id"), key("kind"), key("client_id"), ref("user_id"), text("data"), integer("expires_at"),
		integer("created_at"), ninteger("revoked_at"))),
		Indexes: []Index{idx("ix_oauth_tokens_user", "user_id")}},
	{Name: "recent_pages", Columns: cols(one(key("id"), ref("user_id"), ref("page_id"), integer("visited_at"))),
		Indexes: []Index{idx("ix_recent_user", "user_id", "visited_at")}},
	{Name: "import_jobs", Columns: cols(one(id(), ref("user_id"), key("kind"), key("status"), text("progress"), text("report")), timestamps())},
	{Name: "meta", Columns: cols(one(key("id"), text("value")))},
	{Name: "_seq", PKInt: true, Columns: cols(one(integer("id"), integer("seq")))},
	{Name: "_outbox", PKInt: true, Columns: cols(one(integer("id"), integer("ts"), text("actor"), text("ops")))},
}

var tableByName = func() map[string]*Table {
	m := map[string]*Table{}
	for _, t := range Tables {
		m[t.Name] = t
	}
	return m
}()

// TableByName 返回表定义。
func TableByName(name string) (*Table, bool) {
	t, ok := tableByName[name]
	return t, ok
}

// JournaledTables 返回进日志的表。
func JournaledTables() []*Table {
	var out []*Table
	for _, t := range Tables {
		if t.Journaled {
			out = append(out, t)
		}
	}
	return out
}
