// Package version 记录编进程序的版本信息（构建时用 -ldflags -X 覆盖）。
package version

var (
	Version = "0.1.0-dev"
	Commit  = ""
	Date    = ""
)
