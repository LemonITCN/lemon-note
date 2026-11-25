//go:build gui

package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// 托盘菜单与系统对话框用到的文字（状态窗口里的文字在 ui/index.html）。
var texts = map[string]map[string]string{
	"zh": {
		"open":          "打开 Lemon Note",
		"showWindow":    "显示状态窗口",
		"start":         "启动服务",
		"stop":          "停止服务",
		"autostart":     "开机自启",
		"quit":          "退出",
		"chooseDataDir": "选择数据目录",
		"st.running":    "运行中",
		"st.setup":      "等待首次设置",
		"st.starting":   "正在启动",
		"st.stopping":   "正在停止",
		"st.switching":  "正在切换数据目录",
		"st.stopped":    "已停止",
		"st.error":      "出错",
	},
	"en": {
		"open":          "Open Lemon Note",
		"showWindow":    "Show Status Window",
		"start":         "Start Service",
		"stop":          "Stop Service",
		"autostart":     "Launch at Login",
		"quit":          "Quit",
		"chooseDataDir": "Choose Data Folder",
		"st.running":    "Running",
		"st.setup":      "Waiting for setup",
		"st.starting":   "Starting",
		"st.stopping":   "Stopping",
		"st.switching":  "Switching data folder",
		"st.stopped":    "Stopped",
		"st.error":      "Error",
	},
}

func tr(lang, key string) string {
	if s, ok := texts[lang][key]; ok {
		return s
	}
	return texts["en"][key]
}

// stateText 把服务状态（含正在进行的操作）翻成一句话。
func stateText(lang string, st viewStatus) string {
	if st.Busy != "" {
		return tr(lang, "st."+st.Busy)
	}
	return tr(lang, "st."+string(st.State))
}

// detectLang 取系统界面语言：中文用中文，其他一律英文。可用 LEMON_NOTE_LANG=zh|en 覆盖。
func detectLang() string {
	if v := os.Getenv("LEMON_NOTE_LANG"); v != "" {
		return normLang(v)
	}
	switch runtime.GOOS {
	case "darwin":
		// 从访达启动的程序没有 LANG，读系统的首选语言列表
		if out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.Trim(strings.TrimSpace(line), `"(),`)
				if line != "" {
					return normLang(line)
				}
			}
		}
	case "windows":
		if l := windowsUILang(); l != "" {
			return l
		}
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG", "LANGUAGE"} {
		if v := os.Getenv(k); v != "" {
			return normLang(v)
		}
	}
	return "en"
}

func normLang(s string) string {
	if strings.HasPrefix(strings.ToLower(s), "zh") {
		return "zh"
	}
	return "en"
}
