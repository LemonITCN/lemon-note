package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/llm"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
)

const (
	maxReadText  = 256 << 10 // 文本附件最多读 256 KB
	maxReadImage = 8 << 20   // 图片最多 8 MB
)

var textMimes = map[string]bool{"application/json": true, "application/xml": true, "application/x-yaml": true, "application/yaml": true,
	"application/javascript": true, "application/x-sh": true, "application/toml": true, "application/sql": true}

var imageMimes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

func init() {
	Default.Register(&Tool{
		Name:  "read_file",
		Title: "读取附件",
		Description: "读取页面里的附件（图片、文件块等，正文里写作 lemon://file/<id>）。文本类附件返回内容（可用 offset、limit 按行分段读）；" +
			"图片在模型支持看图时作为图片提供；其他类型只返回文件名、类型和大小。",
		Schema: obj(map[string]any{
			"file_id": str("附件 id，或 lemon://file/<id> 形式的地址"),
			"offset":  num("文本附件从第几行开始读（从 1 开始）"),
			"limit":   num("文本附件最多读多少行（默认 800）"),
		}, "file_id"),
		ReadOnly: true,
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				FileID string `json:"file_id"`
				Offset int    `json:"offset"`
				Limit  int    `json:"limit"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			id := strings.TrimSpace(in.FileID)
			id = strings.TrimPrefix(id, "lemon://file/")
			id = strings.TrimPrefix(id, "/api/files/")
			f, err := env.Svc.Files.Get(env.Ctx, id)
			if err != nil || f.DeletedAt != nil {
				return nil, fail("附件 %s 不存在", id)
			}
			allowed := false
			if f.PageID != nil {
				allowed = env.PageRole(*f.PageID) != model.RoleNone
			}
			if !allowed && env.SpaceRole(f.SpaceID) == model.RoleNone {
				return nil, fail("附件 %s 不存在或没有权限", id)
			}
			head := fmt.Sprintf("附件「%s」（%s，%d 字节）", f.Name, f.Mime, f.Size)
			mime := strings.ToLower(strings.TrimSpace(strings.SplitN(f.Mime, ";", 2)[0]))
			switch {
			case imageMimes[mime]:
				if f.Size > maxReadImage {
					return &Result{Text: head + "：图片太大（超过 8 MB），不提供给模型。"}, nil
				}
				fh, err := env.Svc.Files.Open(f)
				if err != nil {
					return nil, err
				}
				defer fh.Close()
				data, err := io.ReadAll(io.LimitReader(fh, maxReadImage))
				if err != nil {
					return nil, err
				}
				return &Result{Text: head + "，图片见下。", Images: []llm.Part{{Type: "image", MediaType: mime, Data: base64.StdEncoding.EncodeToString(data)}}}, nil
			case strings.HasPrefix(mime, "text/") || textMimes[mime]:
				fh, err := env.Svc.Files.Open(f)
				if err != nil {
					return nil, err
				}
				defer fh.Close()
				data, err := io.ReadAll(io.LimitReader(fh, maxReadText+1))
				if err != nil {
					return nil, err
				}
				truncated := len(data) > maxReadText
				if truncated {
					data = data[:maxReadText]
				}
				if !utf8.Valid(data) {
					return &Result{Text: head + "：不是 UTF-8 文本，无法直接读取。"}, nil
				}
				lines := strings.Split(string(data), "\n")
				start := in.Offset
				if start < 1 {
					start = 1
				}
				limit := in.Limit
				if limit <= 0 {
					limit = 800
				}
				if start > len(lines) {
					return &Result{Text: fmt.Sprintf("%s：共 %d 行，offset 超出范围。", head, len(lines))}, nil
				}
				end := min(len(lines), start-1+limit)
				var b strings.Builder
				fmt.Fprintf(&b, "%s，第 %d–%d 行（共 %d 行", head, start, end, len(lines))
				if truncated {
					b.WriteString("，文件较大只读了前 256 KB")
				}
				b.WriteString("）：\n")
				b.WriteString(strings.Join(lines[start-1:end], "\n"))
				if end < len(lines) {
					fmt.Fprintf(&b, "\n…（还有 %d 行，用 offset=%d 继续读）", len(lines)-end, end+1)
				}
				return &Result{Text: b.String()}, nil
			}
			return &Result{Text: head + "：这种类型暂不支持读取内容。"}, nil
		},
	})
}
