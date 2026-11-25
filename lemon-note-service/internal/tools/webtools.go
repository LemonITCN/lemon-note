package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	htmltomd "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/netsafe"
	"golang.org/x/net/html"
)

// stripNoise 去掉脚本、样式、导航等与正文无关的节点。
func stripNoise(doc string) string {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return doc
	}
	var drop func(n *html.Node)
	drop = func(n *html.Node) {
		for c := n.FirstChild; c != nil; {
			next := c.NextSibling
			if c.Type == html.ElementNode {
				switch c.Data {
				case "script", "style", "noscript", "iframe", "svg", "nav", "footer", "form", "button", "template", "header", "aside":
					n.RemoveChild(c)
					c = next
					continue
				}
			}
			drop(c)
			c = next
		}
	}
	drop(root)
	var buf bytes.Buffer
	_ = html.Render(&buf, root)
	return buf.String()
}

func init() {
	Default.Register(&Tool{
		Name:  "web_fetch",
		Title: "读取网页",
		Description: "抓取一个公开网页并转成 Markdown 返回（只支持 http/https，内网地址会被拦截）。适合读取用户给的链接或笔记里引用的网页。" +
			"内容很长时会截断，可用 offset 继续读。",
		Schema: obj(map[string]any{
			"url":       str("网页地址"),
			"offset":    num("从第几个字符开始返回（默认 0）"),
			"max_chars": num("最多返回多少字符（默认 20000）"),
		}, "url"),
		ReadOnly:  true,
		OpenWorld: true,
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				URL    string `json:"url"`
				Offset int    `json:"offset"`
				Max    int    `json:"max_chars"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if in.Max <= 0 || in.Max > 60000 {
				in.Max = 20000
			}
			ctx, cancel := context.WithTimeout(env.Ctx, 40*time.Second)
			defer cancel()
			resp, body, err := netsafe.Get(ctx, in.URL, 8<<20, "text/html,application/xhtml+xml,text/plain,application/json;q=0.9,*/*;q=0.5")
			if err != nil {
				return nil, fail("%v", err)
			}
			if resp.StatusCode >= 400 {
				return nil, fail("网页返回 %d", resp.StatusCode)
			}
			ct := resp.Header.Get("Content-Type")
			var text string
			switch {
			case strings.Contains(ct, "html"):
				md, err := htmltomd.ConvertString(stripNoise(string(body)))
				if err != nil {
					return nil, fail("网页转换失败：%v", err)
				}
				text = md
			case strings.HasPrefix(ct, "text/") || strings.Contains(ct, "json") || strings.Contains(ct, "xml"):
				text = string(body)
			default:
				return nil, fail("不支持的内容类型：%s", ct)
			}
			r := []rune(text)
			if in.Offset > len(r) {
				in.Offset = len(r)
			}
			end := in.Offset + in.Max
			more := ""
			if end < len(r) {
				more = fmt.Sprintf("\n\n（已截断，共 %d 字符，用 offset=%d 继续读）", len(r), end)
			} else {
				end = len(r)
			}
			final := resp.Request.URL.String()
			return &Result{Text: fmt.Sprintf("网址：%s\n\n%s%s", final, string(r[in.Offset:end]), more), Data: map[string]any{"url": final}}, nil
		},
	})

	Default.Register(&Tool{
		Name:        "web_search",
		Title:       "网页搜索",
		Description: "用管理员配置的搜索服务搜索互联网，返回标题、网址与摘要。需要读全文时再用 web_fetch。",
		Schema: obj(map[string]any{
			"query": str("搜索词"),
			"limit": num("结果数（默认 8，最多 20）"),
		}, "query"),
		ReadOnly:  true,
		OpenWorld: true,
		Available: func(svc *Services) bool {
			if svc.Settings == nil {
				return false
			}
			return svc.Settings.System(context.Background()).WebSearch.Provider != ""
		},
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				Query string `json:"query"`
				Limit int    `json:"limit"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			if in.Limit <= 0 || in.Limit > 20 {
				in.Limit = 8
			}
			cfg := env.Svc.Settings.System(env.Ctx).WebSearch
			key, _ := env.Svc.Box.Open(cfg.APIKey)
			ctx, cancel := context.WithTimeout(env.Ctx, 30*time.Second)
			defer cancel()
			results, err := webSearch(ctx, cfg.Provider, cfg.URL, key, in.Query, in.Limit)
			if err != nil {
				return nil, fail("搜索失败：%v", err)
			}
			if len(results) == 0 {
				return &Result{Text: "没有搜索结果。"}, nil
			}
			var b strings.Builder
			for i, r := range results {
				fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, r.Title, r.URL, truncate(r.Snippet, 300))
			}
			return &Result{Text: b.String()}, nil
		},
	})
}

type searchResult struct{ Title, URL, Snippet string }

// webSearch 调用搜索服务。SearXNG 一般部署在内网，管理员配置的地址按受信处理（不走内网拦截）。
func webSearch(ctx context.Context, provider, base, key, q string, limit int) ([]searchResult, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	var req *http.Request
	switch provider {
	case "searxng":
		u := strings.TrimRight(base, "/") + "/search?format=json&q=" + url.QueryEscape(q)
		req, _ = http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	case "tavily":
		body, _ := json.Marshal(map[string]any{"query": q, "max_results": limit})
		req, _ = http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
	case "bocha":
		body, _ := json.Marshal(map[string]any{"query": q, "count": limit, "summary": true})
		req, _ = http.NewRequestWithContext(ctx, http.MethodPost, "https://api.bochaai.com/v1/web-search", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
	default:
		return nil, fmt.Errorf("未配置搜索服务")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("搜索服务返回 %d", resp.StatusCode)
	}
	var out []searchResult
	switch provider {
	case "searxng":
		var v struct {
			Results []struct {
				Title, URL, Content string
			} `json:"results"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			return nil, err
		}
		for _, r := range v.Results {
			out = append(out, searchResult{r.Title, r.URL, r.Content})
		}
	case "tavily":
		var v struct {
			Results []struct {
				Title, URL, Content string
			} `json:"results"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			return nil, err
		}
		for _, r := range v.Results {
			out = append(out, searchResult{r.Title, r.URL, r.Content})
		}
	case "bocha":
		var v struct {
			Data struct {
				WebPages struct {
					Value []struct {
						Name    string `json:"name"`
						URL     string `json:"url"`
						Snippet string `json:"snippet"`
						Summary string `json:"summary"`
					} `json:"value"`
				} `json:"webPages"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			return nil, err
		}
		for _, r := range v.Data.WebPages.Value {
			s := r.Summary
			if s == "" {
				s = r.Snippet
			}
			out = append(out, searchResult{r.Name, r.URL, s})
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
