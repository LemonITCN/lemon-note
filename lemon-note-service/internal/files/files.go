// Package files 管理附件：流式写入数据目录的内容寻址仓库，记录元数据。
package files

import (
	"bufio"
	"context"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/datadir"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/ids"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/store"
)

type Service struct {
	st     *store.Store
	dir    *datadir.Dir
	tmpDir string
}

func New(st *store.Store, dir *datadir.Dir, cacheDir string) *Service {
	return &Service{st: st, dir: dir, tmpDir: filepath.Join(cacheDir, "tmp")}
}

// CleanName 清理文件名：去路径、控制字符，限制长度。
func CleanName(n string) string {
	n = filepath.Base(strings.ReplaceAll(n, "\\", "/"))
	n = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, n)
	if n == "." || n == "/" || n == "" {
		n = "file"
	}
	if utf8.RuneCountInString(n) > 200 {
		r := []rune(n)
		n = string(r[:200])
	}
	return n
}

// Save 保存上传的文件。
func (s *Service) Save(ctx context.Context, actor, spaceID, pageID, name string, r io.Reader, maxSize int64) (*model.File, error) {
	br := bufio.NewReaderSize(r, 4096)
	head, _ := br.Peek(512)
	sniffed := http.DetectContentType(head)
	sha, size, err := s.dir.PutBlob(br, s.tmpDir, maxSize)
	if err != nil {
		return nil, err
	}
	name = CleanName(name)
	mt := sniffed
	if ext := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); ext != "" && (strings.HasPrefix(sniffed, "text/plain") || sniffed == "application/octet-stream" || strings.HasPrefix(sniffed, "application/zip")) {
		mt = ext
	}
	if strings.HasSuffix(strings.ToLower(name), ".svg") && strings.Contains(sniffed, "xml") || strings.HasPrefix(sniffed, "text/xml") && strings.HasSuffix(strings.ToLower(name), ".svg") {
		mt = "image/svg+xml"
	}
	if i := strings.Index(mt, ";"); i > 0 && !strings.HasPrefix(mt, "text/") {
		mt = mt[:i]
	}
	f := &model.File{ID: ids.New(), SpaceID: spaceID, PageID: model.StrPtr(pageID), SHA256: sha, Size: size, Mime: mt, Name: name, CreatedAt: model.Now(), CreatedBy: actor}
	if strings.HasPrefix(mt, "image/") && mt != "image/svg+xml" {
		if fh, err := s.dir.OpenBlob(sha); err == nil {
			if cfg, _, err := image.DecodeConfig(fh); err == nil {
				f.Width, f.Height = int64(cfg.Width), int64(cfg.Height)
			}
			fh.Close()
		}
	}
	_, err = s.st.Write(ctx, actor, func(tx *store.Tx) error { return tx.Put("files", f) })
	return f, err
}

// Get 读附件元数据。
func (s *Service) Get(ctx context.Context, id string) (*model.File, error) {
	var f model.File
	if err := s.st.Get(ctx, &f, "SELECT * FROM files WHERE id = ?", id); err != nil {
		return nil, errors.New("文件不存在")
	}
	return &f, nil
}

// Open 打开附件内容。
func (s *Service) Open(f *model.File) (*os.File, error) { return s.dir.OpenBlob(f.SHA256) }

// InlineSafe 判断这种类型能否在浏览器里直接打开（否则一律下载）。
func InlineSafe(mt string) bool {
	switch {
	case strings.HasPrefix(mt, "image/") && mt != "image/svg+xml":
		return true
	case strings.HasPrefix(mt, "video/"), strings.HasPrefix(mt, "audio/"):
		return true
	case mt == "application/pdf", mt == "image/svg+xml":
		return true
	}
	return false
}
