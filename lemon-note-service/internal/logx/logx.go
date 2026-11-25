// Package logx 配置日志：同时写标准错误与缓存目录下的滚动日志文件。
package logx

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// rotatingFile 超过 maxSize 就把当前文件改名为 .1（保留 3 份）。
type rotatingFile struct {
	mu      sync.Mutex
	path    string
	f       *os.File
	size    int64
	maxSize int64
}

func openRotating(path string, maxSize int64) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	st, _ := f.Stat()
	r := &rotatingFile{path: path, f: f, maxSize: maxSize}
	if st != nil {
		r.size = st.Size()
	}
	return r, nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > r.maxSize {
		r.f.Close()
		for i := 2; i >= 1; i-- {
			os.Rename(r.path+"."+itoa(i), r.path+"."+itoa(i+1))
		}
		os.Rename(r.path, r.path+".1")
		f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return 0, err
		}
		r.f, r.size = f, 0
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

func itoa(i int) string { return string(rune('0' + i)) }

// ParseLevel 解析日志级别。
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// Setup 返回日志器；logDir 为空时只写标准错误。
func Setup(level string, logDir string, stderr bool) (*slog.Logger, io.Closer) {
	var writers []io.Writer
	if stderr {
		writers = append(writers, os.Stderr)
	}
	var closer io.Closer = io.NopCloser(nil)
	if logDir != "" {
		if f, err := openRotating(filepath.Join(logDir, "lemon-note.log"), 10<<20); err == nil {
			writers = append(writers, f)
			closer = f
		}
	}
	if len(writers) == 0 {
		writers = append(writers, io.Discard)
	}
	h := slog.NewTextHandler(io.MultiWriter(writers...), &slog.HandlerOptions{Level: ParseLevel(level)})
	return slog.New(h), closer
}
