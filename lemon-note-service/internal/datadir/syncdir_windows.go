//go:build windows

package datadir

// SyncDir 在 Windows 上无需也无法 fsync 目录，MoveFileEx 本身是持久的。
func SyncDir(dir string) {}
