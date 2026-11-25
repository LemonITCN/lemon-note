//go:build gui

package main

import (
	"runtime"
	"sync"

	"github.com/LemonITCN/lemon-note/lemon-note-service/internal/app"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// trayUI 是托盘图标与菜单。
type trayUI struct {
	lang string
	tray *application.SystemTray

	header, open, show, start, stop, autostart, quit *application.MenuItem

	mu      sync.Mutex
	iconOff *bool // 当前用的是不是灰色图标（只有 Windows / Linux 换图标）
}

func newTray(wapp *application.App, ctl *controller, lang string, showWindow func()) *trayUI {
	t := &trayUI{lang: lang, tray: wapp.SystemTray.New()}
	menu := wapp.NewMenu()
	t.header = menu.Add("Lemon Note").SetEnabled(false)
	menu.AddSeparator()
	t.open = menu.Add(tr(lang, "open")).OnClick(func(*application.Context) { ctl.openInBrowser() })
	t.show = menu.Add(tr(lang, "showWindow")).OnClick(func(*application.Context) { showWindow() })
	menu.AddSeparator()
	t.start = menu.Add(tr(lang, "start")).OnClick(func(*application.Context) { go ctl.start(false) })
	t.stop = menu.Add(tr(lang, "stop")).OnClick(func(*application.Context) { go ctl.stop() })
	menu.AddSeparator()
	t.autostart = menu.AddCheckbox(tr(lang, "autostart"), false).OnClick(func(cx *application.Context) {
		on := cx.ClickedMenuItem().Checked()
		go ctl.setAutostart(on)
	})
	menu.AddSeparator()
	t.quit = menu.Add(tr(lang, "quit")).OnClick(func(*application.Context) { ctl.quit() })

	if runtime.GOOS == "darwin" {
		// 菜单栏用模板图标，系统按深浅色自动着色；点一下弹出菜单
		t.tray.SetTemplateIcon(trayTemplateIcon)
	} else {
		// Windows / Linux：左键打开状态窗口，右键弹出菜单
		t.tray.SetIcon(trayIconOff)
		t.tray.OnClick(showWindow)
	}
	t.tray.SetTooltip("Lemon Note")
	t.tray.SetMenu(menu)
	t.apply(ctl.status())
	return t
}

// update 状态变化时刷新菜单（在界面主线程上执行）。
func (t *trayUI) update(st viewStatus) {
	application.InvokeAsync(func() { t.apply(st) })
}

func (t *trayUI) apply(st viewStatus) {
	busy := st.Busy != ""
	up := st.State == app.StateRunning || st.State == app.StateSetup
	label := "Lemon Note · " + stateText(t.lang, st)
	t.header.SetLabel(label)
	t.open.SetEnabled(!busy)
	t.start.SetEnabled(!busy && (st.State == app.StateStopped || st.State == app.StateError))
	t.stop.SetEnabled(!busy && (up || st.State == app.StateStarting))
	t.autostart.SetChecked(st.Autostart)
	t.tray.SetTooltip(label)
	if runtime.GOOS != "darwin" {
		off := !up
		t.mu.Lock()
		changed := t.iconOff == nil || *t.iconOff != off
		t.iconOff = &off
		t.mu.Unlock()
		if changed {
			if off {
				t.tray.SetIcon(trayIconOff)
			} else {
				t.tray.SetIcon(trayIcon)
			}
		}
	}
}
