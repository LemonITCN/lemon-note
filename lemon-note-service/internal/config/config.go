// Package config 负责引导配置：数据目录在哪、监听地址、TLS、外部数据库连接串。
// 引导配置是本机专属的，不放进数据目录。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/pelletier/go-toml/v2"
)

// TLS 设置。
type TLS struct {
	Mode        string   `toml:"mode"` // off | file | acme
	CertFile    string   `toml:"cert_file"`
	KeyFile     string   `toml:"key_file"`
	ACMEDomains []string `toml:"acme_domains"`
	ACMEEmail   string   `toml:"acme_email"`
}

// Bootstrap 是引导配置。
type Bootstrap struct {
	DataDir        string   `toml:"data_dir"`
	CacheDir       string   `toml:"cache_dir"`
	Listen         string   `toml:"listen"`
	BaseURL        string   `toml:"base_url"`
	DSN            string   `toml:"dsn"`
	TrustedProxies []string `toml:"trusted_proxies"`
	LogLevel       string   `toml:"log_level"`
	TLS            TLS      `toml:"tls"`

	path string // 从哪个文件读的
}

const DefaultPort = 8686

// Mode 区分以系统服务运行还是以当前用户运行，决定默认路径。
type Mode int

const (
	UserMode Mode = iota
	ServiceMode
)

func isRoot() bool { return runtime.GOOS != "windows" && os.Geteuid() == 0 }

// DefaultConfigPath 返回引导配置文件的默认路径。
func DefaultConfigPath(m Mode) string {
	switch runtime.GOOS {
	case "windows":
		if m == ServiceMode {
			return filepath.Join(programData(), "Lemon Note", "lemon-note.toml")
		}
		return filepath.Join(appData(), "Lemon Note", "lemon-note.toml")
	case "darwin":
		if m == ServiceMode && isRoot() {
			return "/Library/Application Support/Lemon Note/lemon-note.toml"
		}
		return filepath.Join(home(), "Library", "Application Support", "Lemon Note", "lemon-note.toml")
	default:
		if isRoot() || m == ServiceMode {
			return "/etc/lemon-note/lemon-note.toml"
		}
		return filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "lemon-note", "lemon-note.toml")
	}
}

// DefaultDataDir 返回默认数据目录。
func DefaultDataDir(m Mode) string {
	switch runtime.GOOS {
	case "windows":
		if m == ServiceMode {
			return filepath.Join(programData(), "Lemon Note", "data")
		}
		return filepath.Join(localAppData(), "Lemon Note", "data")
	case "darwin":
		if m == ServiceMode && isRoot() {
			return "/Library/Application Support/Lemon Note/data"
		}
		return filepath.Join(home(), "Library", "Application Support", "Lemon Note", "data")
	default:
		if isRoot() || m == ServiceMode {
			return "/var/lib/lemon-note"
		}
		return filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), "lemon-note")
	}
}

// DefaultCacheDir 返回默认缓存目录。
func DefaultCacheDir(m Mode) string {
	switch runtime.GOOS {
	case "windows":
		if m == ServiceMode {
			return filepath.Join(programData(), "Lemon Note", "cache")
		}
		return filepath.Join(localAppData(), "Lemon Note", "cache")
	case "darwin":
		if m == ServiceMode && isRoot() {
			return "/Library/Caches/Lemon Note"
		}
		return filepath.Join(home(), "Library", "Caches", "Lemon Note")
	default:
		if isRoot() || m == ServiceMode {
			return "/var/cache/lemon-note"
		}
		return filepath.Join(xdg("XDG_CACHE_HOME", ".cache"), "lemon-note")
	}
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func xdg(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return filepath.Join(home(), fallback)
}

func programData() string {
	if v := os.Getenv("ProgramData"); v != "" {
		return v
	}
	return `C:\ProgramData`
}

func appData() string {
	if v := os.Getenv("APPDATA"); v != "" {
		return v
	}
	return filepath.Join(home(), "AppData", "Roaming")
}

func localAppData() string {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return v
	}
	return filepath.Join(home(), "AppData", "Local")
}

// Load 读取引导配置；文件不存在时返回默认值（不报错）。
func Load(path string, m Mode) (*Bootstrap, error) {
	if path == "" {
		path = DefaultConfigPath(m)
	}
	b := &Bootstrap{path: path}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := toml.Unmarshal(data, b); err != nil {
			return nil, fmt.Errorf("引导配置 %s 格式错误: %w", path, err)
		}
	}
	b.applyEnv()
	b.fillDefaults(m)
	return b, nil
}

func (b *Bootstrap) applyEnv() {
	set := func(env string, dst *string) {
		if v := os.Getenv(env); v != "" {
			*dst = v
		}
	}
	set("LEMON_NOTE_DATA_DIR", &b.DataDir)
	set("LEMON_NOTE_CACHE_DIR", &b.CacheDir)
	set("LEMON_NOTE_LISTEN", &b.Listen)
	set("LEMON_NOTE_BASE_URL", &b.BaseURL)
	set("LEMON_NOTE_DSN", &b.DSN)
	set("LEMON_NOTE_LOG_LEVEL", &b.LogLevel)
	if v := os.Getenv("LEMON_NOTE_TRUSTED_PROXIES"); v != "" {
		b.TrustedProxies = strings.Split(v, ",")
	}
}

func (b *Bootstrap) fillDefaults(m Mode) {
	if b.DataDir == "" {
		b.DataDir = DefaultDataDir(m)
	}
	if b.CacheDir == "" {
		b.CacheDir = DefaultCacheDir(m)
	}
	if b.Listen == "" {
		b.Listen = fmt.Sprintf(":%d", DefaultPort)
	}
	if b.LogLevel == "" {
		b.LogLevel = "info"
	}
	if b.TLS.Mode == "" {
		b.TLS.Mode = "off"
	}
	b.DataDir = expand(b.DataDir)
	b.CacheDir = expand(b.CacheDir)
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") || p == "~" {
		p = filepath.Join(home(), strings.TrimPrefix(p, "~"))
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// Path 返回配置文件路径。
func (b *Bootstrap) Path() string { return b.path }

// Save 原子写回配置文件（权限 600，里面可能有数据库口令）。
func (b *Bootstrap) Save() error {
	data, err := toml.Marshal(b)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o755); err != nil {
		return err
	}
	return datadir.WriteFileAtomic(b.path, data, 0o600)
}

// HostID 返回本机稳定标识（存在缓存目录，首次生成）。
func HostID(cacheDir string) (string, error) {
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", err
	}
	p := filepath.Join(cacheDir, "host-id")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) >= 16 {
		return strings.TrimSpace(string(b)), nil
	}
	id := ids.RandomHex(16)
	if err := datadir.WriteFileAtomic(p, []byte(id), 0o600); err != nil {
		return "", err
	}
	return id, nil
}
