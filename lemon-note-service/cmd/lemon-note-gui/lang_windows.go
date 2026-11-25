//go:build gui && windows

package main

import "syscall"

// windowsUILang 读用户界面语言（GetUserDefaultUILanguage），主语言是中文（0x04）时返回 zh。
func windowsUILang() string {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage")
	if proc.Find() != nil {
		return ""
	}
	id, _, _ := proc.Call()
	if id&0x3ff == 0x04 {
		return "zh"
	}
	return "en"
}
