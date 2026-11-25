//go:build gui

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"time"
)

// handler 是状态窗口的资源服务器：页面本身与几个 JSON 接口。
// 只有状态窗口里的网页能访问（走 Wails 的资源协议，不监听网络端口）。
func (c *controller) handler(lang string) http.Handler {
	mux := http.NewServeMux()
	page := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write(indexHTML)
	}
	mux.HandleFunc("GET /{$}", page)
	mux.HandleFunc("GET /index.html", page)

	// 状态：带 since（上次拿到的版本号）时最多等 15 秒，有变化立即返回
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		var st viewStatus
		if s := r.URL.Query().Get("since"); s != "" {
			since, _ := strconv.ParseUint(s, 10, 64)
			st = c.waitStatus(r.Context(), since, 15*time.Second)
		} else {
			st = c.status()
		}
		st.Lang = lang
		writeJSON(w, http.StatusOK, st)
	})

	action := func(path string, fn func(r *http.Request) (any, error)) {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			res, err := fn(r)
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"error": err.Error()})
				return
			}
			if res == nil {
				res = map[string]any{"ok": true}
			}
			writeJSON(w, http.StatusOK, res)
		})
	}
	action("/api/start", func(*http.Request) (any, error) {
		go c.start(false)
		return nil, nil
	})
	action("/api/takeover", func(*http.Request) (any, error) {
		go c.start(true)
		return nil, nil
	})
	action("/api/stop", func(*http.Request) (any, error) {
		go c.stop()
		return nil, nil
	})
	action("/api/open", func(*http.Request) (any, error) {
		c.openInBrowser()
		return nil, nil
	})
	action("/api/copy", func(r *http.Request) (any, error) {
		var in struct {
			Text string `json:"text"`
		}
		if err := readJSON(r, &in); err != nil {
			return nil, err
		}
		if !c.wapp.Clipboard.SetText(in.Text) {
			return nil, errors.New("无法写入剪贴板")
		}
		return nil, nil
	})
	action("/api/open-data-dir", func(*http.Request) (any, error) {
		return nil, openPath(c.status().DataDir)
	})
	action("/api/open-logs", func(*http.Request) (any, error) {
		return nil, openPath(c.logDir)
	})
	action("/api/open-config-dir", func(*http.Request) (any, error) {
		return nil, openPath(filepath.Dir(c.status().ConfigPath))
	})
	// 选择新的数据目录：弹出系统的文件夹选择框，返回实际会用的位置，由页面确认后再调 apply
	action("/api/data-dir/choose", func(*http.Request) (any, error) {
		dlg := c.wapp.Dialog.OpenFile().
			CanChooseFiles(false).
			CanChooseDirectories(true).
			CanCreateDirectories(true).
			SetTitle(tr(lang, "chooseDataDir"))
		if c.window != nil {
			dlg = dlg.AttachToWindow(c.window)
		}
		dir, err := dlg.PromptForSingleSelection()
		if err != nil {
			return nil, err
		}
		if dir == "" {
			return map[string]any{"canceled": true}, nil
		}
		return resolveDataDir(dir, c.status())
	})
	action("/api/data-dir/apply", func(r *http.Request) (any, error) {
		var in struct {
			Path string `json:"path"`
		}
		if err := readJSON(r, &in); err != nil {
			return nil, err
		}
		if in.Path == "" {
			return nil, errors.New("缺少目录")
		}
		return nil, c.switchDataDir(in.Path)
	})
	action("/api/autostart", func(r *http.Request) (any, error) {
		var in struct {
			Enabled bool `json:"enabled"`
		}
		if err := readJSON(r, &in); err != nil {
			return nil, err
		}
		return nil, c.setAutostart(in.Enabled)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<16)).Decode(v); err != nil {
		return errors.New("请求格式错误")
	}
	return nil
}
