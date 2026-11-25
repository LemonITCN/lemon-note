package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/catalog"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/model"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/netsafe"
	"github.com/go-chi/chi/v5"
	"golang.org/x/net/html"
)

func (s *Server) routesSearch(r chi.Router) {
	r.Get("/api/search", s.h(s.handleSearch))
	r.Post("/api/unfurl", s.h(s.handleUnfurl))
}

// AccessibleSpaceIDs 当前请求可访问的空间。
func (s *Server) AccessibleSpaceIDs(r *http.Request) []string {
	var out []string
	for _, sp := range s.Cat.VisibleSpaces(UserOf(r).ID) {
		if s.SpaceRole(r, sp.ID) != model.RoleNone {
			out = append(out, sp.ID)
		}
	}
	return out
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := atoiDefault(r.URL.Query().Get("limit"), 30)
	if q == "" || len(q) > 200 {
		return ok(w, map[string]any{"results": []any{}})
	}
	// 「与我共享」的页面不在我的空间里，搜索时不限定空间再按权限过滤
	hits, err := s.Search.Search(r.Context(), q, nil, limit*3)
	if err != nil {
		return err
	}
	type res struct {
		Meta    catalog.PageMeta `json:"meta"`
		Snippet string           `json:"snippet"`
	}
	out := []res{}
	for _, h := range hits {
		m, ok := s.Cat.Page(h.PageID)
		if !ok || m.TrashedAt != 0 || s.PageRole(r, h.PageID) == model.RoleNone {
			continue
		}
		out = append(out, res{Meta: m, Snippet: h.Snippet})
		if len(out) >= limit {
			break
		}
	}
	return ok(w, map[string]any{"results": out})
}

// handleUnfurl 抓取网页标题、描述、预览图，给书签块用。
func (s *Server) handleUnfurl(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		URL string `json:"url"`
	}
	if err := readJSONLimit(r, &in, 4096); err != nil {
		return err
	}
	if !s.limiter.Allow("unfurl:"+UserOf(r).ID, 1, 20) {
		return &APIError{Status: 429, Code: "too_many_attempts", Message: "请求过于频繁"}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	resp, body, err := netsafe.Get(ctx, in.URL, 2<<20, "text/html,application/xhtml+xml")
	if err != nil {
		return errBad(err.Error())
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "html") {
		return ok(w, map[string]any{"title": "", "description": ""})
	}
	meta := parseMeta(string(body))
	return ok(w, meta)
}

func parseMeta(doc string) map[string]string {
	out := map[string]string{"title": "", "description": "", "image": ""}
	z := html.NewTokenizer(strings.NewReader(doc))
	inTitle := false
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			if tag == "title" {
				inTitle = true
			}
			if tag == "body" {
				return out
			}
			if tag == "meta" && hasAttr {
				attrs := map[string]string{}
				for {
					k, v, more := z.TagAttr()
					attrs[string(k)] = string(v)
					if !more {
						break
					}
				}
				key := attrs["property"]
				if key == "" {
					key = attrs["name"]
				}
				c := strings.TrimSpace(attrs["content"])
				switch key {
				case "og:title":
					out["title"] = c
				case "og:description", "description":
					if out["description"] == "" || key == "og:description" {
						out["description"] = c
					}
				case "og:image":
					if strings.HasPrefix(c, "https://") {
						out["image"] = c
					}
				}
			}
		case html.TextToken:
			if inTitle && out["title"] == "" {
				out["title"] = strings.TrimSpace(string(z.Text()))
			}
			inTitle = false
		}
	}
}
