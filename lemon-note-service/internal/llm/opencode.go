package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/version"
)

// OpenCodeGo 是 OpenCode Go 套餐供应商：模型列表里每个模型自带推荐协议与地址。
type OpenCodeGo struct {
	APIKey    string
	ModelsURL string // 默认 https://opencode.ai/zen/go/v1/models

	mu      sync.Mutex
	models  []Model
	fetched time.Time
}

const DefaultOpenCodeModelsURL = "https://opencode.ai/zen/go/v1/models"

func (p *OpenCodeGo) userAgent() string { return "lemon-note/" + version.Version }

// Models 返回可用模型（缓存一小时）。
func (p *OpenCodeGo) Models(ctx context.Context) ([]Model, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.models != nil && time.Since(p.fetched) < time.Hour {
		return p.models, nil
	}
	u := p.ModelsURL
	if u == "" {
		u = DefaultOpenCodeModelsURL
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+p.APIKey)
	req.Header.Set("User-Agent", p.userAgent())
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		if p.models != nil {
			return p.models, nil
		}
		return nil, fmt.Errorf("获取模型列表失败：%v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != 200 {
		return nil, &APIError{Status: resp.StatusCode, Message: errorMessage(data)}
	}
	var raw struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Type        string `json:"type"`
			Limit       struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limit"`
			Modalities struct {
				Input []string `json:"input"`
			} `json:"modalities"`
			Capabilities struct {
				Tools struct {
					Supported bool `json:"supported"`
				} `json:"tools"`
				Reasoning struct {
					Supported bool     `json:"supported"`
					Effort    []string `json:"effort"`
					Toggle    bool     `json:"toggle"`
					Default   *struct {
						Value string `json:"value"`
					} `json:"default"`
				} `json:"reasoning"`
				Temperature bool `json:"temperature"`
			} `json:"capabilities"`
			API map[string]struct {
				BaseURL       string `json:"base_url"`
				Compatibility struct {
					ReasoningField string `json:"reasoning_field"`
				} `json:"compatibility"`
			} `json:"api"`
			RecommendedAPI string `json:"recommended_api"`
			Go             struct {
				Featured bool `json:"featured"`
				Usage    struct {
					Go int `json:"go"`
				} `json:"monthly_usage"`
			} `json:"go"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("模型列表格式无法识别：%v", err)
	}
	var out []Model
	for _, m := range raw.Data {
		api := m.RecommendedAPI
		spec, ok := m.API[api]
		if !ok {
			continue
		}
		switch api {
		case "chat-completions", "messages", "responses", "embeddings":
		default:
			continue // 其他协议暂不支持
		}
		typ := "chat"
		if api == "embeddings" || m.Type == "embedding" {
			typ = "embedding"
		} else if !m.Capabilities.Tools.Supported {
			continue // 智能体必须能调工具
		}
		md := Model{ID: m.ID, Name: m.Name, Description: m.Description, Context: m.Limit.Context, MaxOutput: m.Limit.Output, API: api,
			BaseURL: spec.BaseURL, ReasoningField: spec.Compatibility.ReasoningField, Temperature: m.Capabilities.Temperature, Featured: m.Go.Featured, Quota: m.Go.Usage.Go, Type: typ}
		for _, in := range m.Modalities.Input {
			if strings.HasPrefix(in, "image/") {
				md.Vision = true
			}
			if in == "application/pdf" {
				md.PDF = true
			}
		}
		if m.Capabilities.Reasoning.Supported {
			md.Efforts = m.Capabilities.Reasoning.Effort
			if len(md.Efforts) == 0 {
				if m.Capabilities.Reasoning.Toggle {
					md.Efforts = []string{"none", "medium"}
				} else {
					md.Efforts = []string{"default"}
				}
			}
			if m.Capabilities.Reasoning.Default != nil {
				md.DefaultEffort = m.Capabilities.Reasoning.Default.Value
			}
		}
		out = append(out, md)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Featured != out[j].Featured {
			return out[i].Featured
		}
		return out[i].Name < out[j].Name
	})
	p.models = out
	p.fetched = time.Now()
	return out, nil
}

// Model 查一个模型。
func (p *OpenCodeGo) Model(ctx context.Context, id string) (Model, error) {
	ms, err := p.Models(ctx)
	if err != nil {
		return Model{}, err
	}
	for _, m := range ms {
		if m.ID == id {
			return m, nil
		}
	}
	return Model{}, fmt.Errorf("模型 %s 不可用", id)
}

// Stream 按模型推荐的协议发起流式请求。
func (p *OpenCodeGo) Stream(ctx context.Context, req Request) (Stream, error) {
	m, err := p.Model(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	session := req.SessionID
	if session == "" {
		session = "lemon-" + randID()
	}
	headers := map[string]string{"Authorization": "Bearer " + p.APIKey, "x-api-key": p.APIKey, "User-Agent": p.userAgent(), "x-opencode-session": asciiOnly(session)}
	effort := pickEffort(m, req.Effort)
	req.Effort = effort
	if req.MaxTokens <= 0 || (m.MaxOutput > 0 && req.MaxTokens > m.MaxOutput) {
		req.MaxTokens = min(32000, max(4096, m.MaxOutput))
	}
	switch m.API {
	case "messages":
		return (&anthropicAdapter{baseURL: m.BaseURL, headers: headers, thinking: len(m.Efforts) > 0, temperature: m.Temperature}).stream(ctx, req)
	case "responses":
		return (&responsesAdapter{baseURL: m.BaseURL, headers: headers, supportsEffort: len(m.Efforts) > 0}).stream(ctx, req)
	default:
		return (&chatAdapter{baseURL: m.BaseURL, headers: headers, reasoningField: m.ReasoningField, supportsEffort: len(m.Efforts) > 0 && effort != "default", temperature: m.Temperature}).stream(ctx, req)
	}
}

// pickEffort 把通用档位映射到模型实际支持的取值。
func pickEffort(m Model, want string) string {
	if len(m.Efforts) == 0 || want == "" {
		return ""
	}
	if m.Efforts[0] == "default" {
		return ""
	}
	for _, e := range m.Efforts {
		if e == want {
			return e
		}
	}
	order := []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	rank := func(s string) int {
		for i, x := range order {
			if x == s {
				return i
			}
		}
		return 3
	}
	best, bestD := m.Efforts[0], 100
	for _, e := range m.Efforts {
		d := rank(e) - rank(want)
		if d < 0 {
			d = -d
		}
		if d < bestD {
			best, bestD = e, d
		}
	}
	return best
}

func asciiOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 128 && r > 31 {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "lemon-note"
	}
	return b.String()
}
