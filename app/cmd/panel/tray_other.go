//go:build !windows

package main

// 非 Windows 平台没有托盘：空实现，保持 main.go 的调用面一致。

type trayEvent int

const (
	trayEventExit trayEvent = iota
	trayEventCoreToggle
	trayEventCoreRestart
	trayEventAutostartToggle
)

type trayState struct {
	Listen      string
	CoreRunning bool
	Autostart   bool
}

type trayHandle struct{}

func startTray(initial trayState) *trayHandle { return nil }

func (t *trayHandle) Update(fn func(*trayState)) {}

func (t *trayHandle) Events() <-chan trayEvent { return nil }

func (t *trayHandle) Remove() {}
