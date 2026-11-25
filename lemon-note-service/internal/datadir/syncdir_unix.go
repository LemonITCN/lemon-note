//go:build !windows

package datadir

import "os"

// SyncDir fsync 目录，确保改名持久化。
func SyncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}
