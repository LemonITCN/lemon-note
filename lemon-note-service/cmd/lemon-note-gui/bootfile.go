//go:build gui

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
	"github.com/pelletier/go-toml/v2"
)

// writeDataDir 只改引导配置文件里的 data_dir，其余设置原样保留（不把默认值和环境变量写进去）。
func writeDataDir(path, dir string) error {
	m := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := toml.Unmarshal(data, &m); err != nil {
			return fmt.Errorf("引导配置 %s 格式错误: %w", path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	m["data_dir"] = dir
	out, err := toml.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// 里面可能有数据库口令，权限 600
	return datadir.WriteFileAtomic(path, out, 0o600)
}
