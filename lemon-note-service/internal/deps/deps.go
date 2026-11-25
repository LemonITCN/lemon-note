//go:build tools

// Package deps 固定后续模块要用到的依赖，避免多人并行开发时争抢修改 go.mod。
package deps

import (
	_ "github.com/minio/minio-go/v7"
	_ "github.com/pkg/sftp"
	_ "github.com/studio-b12/gowebdav"
	_ "github.com/yuin/goldmark"
	_ "golang.org/x/crypto/ssh"
	_ "golang.org/x/net/webdav"
)
