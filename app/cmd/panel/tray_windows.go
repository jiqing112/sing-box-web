//go:build windows

// Windows 系统托盘：Shell_NotifyIconW + 隐藏窗口消息循环，独立锁定 OS 线程的 goroutine。
// 刻意用纯标准库 syscall 手写（与项目「极少数第三方依赖」的定位一致），
// 不引入 systray 之类的 GUI 框架。SYSTEM 计划任务自启时运行在会话 0，无桌面，自动跳过。

package main

import (
	_ "embed"
	"fmt"
	"log"
	"runtime"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

//go:embed tray.ico
var trayICO []byte

const (
	trayClassName     = "SingBoxPanelTrayWnd"
	trayCallbackMsg   = 0x8100 // WM_APP 区段：避开 TaskbarCreated 等注册消息
	trayRefreshMsg    = 0x8101
	trayID            = 1
	iconRGBA32Version = 0x00030000
)

const (
	mfString    = 0x00000000
	mfSeparator = 0x00000800
	mfChecked   = 0x00000008
	tpmRightBtn = 0x0002
	tpmReturnCm = 0x0100
	tpmNoNotify = 0x0080
	swShownorm  = 1
	idApple     = 32512 // IDI_APPLICATION

	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmNull        = 0x0000
	wmDblClick    = 0x0203
	wmRButtonUp   = 0x0205
	wmContextMenu = 0x007B

	nimAdd    = 0x00000000
	nimModify = 0x00000001
	nimDelete = 0x00000002

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifInfo    = 0x00000010
	niifInfo   = 0x00000001

	menuOpen      = 1
	menuToggle    = 2
	menuRestart   = 3
	menuAutostart = 4
	menuExit      = 5
)

// 托盘事件：菜单触发、需要主循环处理
type trayEvent int

const (
	trayEventExit trayEvent = iota
	trayEventCoreToggle
	trayEventCoreRestart
	trayEventAutostartToggle
)

// trayState 是托盘菜单/tooltip 依赖的共享快照。
type trayState struct {
	Listen      string
	CoreRunning bool
	Autostart   bool
}

type trayHandle struct {
	mu     sync.Mutex
	state  trayState
	events chan trayEvent
	hwnd   uintptr
	nid    *notifyIconData
	// taskbarCreated 是 RegisterWindowMessageW("TaskbarCreated") 的注册值，
	// explorer 重启时以此消息为信号重新放置图标。
	taskbarCreated uint32
}

// Update 修改共享状态；有变化时通知托盘线程刷新 tooltip。
func (t *trayHandle) Update(fn func(*trayState)) {
	t.mu.Lock()
	before := t.state
	fn(&t.state)
	changed := t.state != before
	hwnd := t.hwnd
	t.mu.Unlock()
	if changed && hwnd != 0 {
		procPostMessage.Call(hwnd, trayRefreshMsg, 0, 0)
	}
}

// Events 返回事件通道；托盘未启用时接收端为 nil（select 上天然永远阻塞，无需特判）。
func (t *trayHandle) Events() <-chan trayEvent {
	if t == nil {
		return nil
	}
	return t.events
}

// Remove 退出前移除图标：WM_CLOSE → DestroyWindow → WM_DESTROY 清理。
func (t *trayHandle) Remove() {
	if t == nil {
		return
	}
	t.mu.Lock()
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd != 0 {
		procPostMessage.Call(hwnd, wmClose, 0, 0)
	}
}

// activeTray 是当前进程的托盘单例：窗口过程只能通过包级变量拿到状态。
var (
	activeTrayMu sync.Mutex
	activeTray   *trayHandle
)

// startTray 创建托盘。返回 nil 表示托盘不可用（会话 0 / 创建失败 / 已存在）。
func startTray(initial trayState) *trayHandle {
	if sessionZero() {
		return nil
	}
	activeTrayMu.Lock()
	existing := activeTray
	activeTrayMu.Unlock()
	if existing != nil {
		return nil
	}
	h := &trayHandle{state: initial, events: make(chan trayEvent, 8)}
	ready := make(chan bool, 1)
	go func() {
		// Win32 窗口与消息循环必须在同一线程；syscall.Syscall 调用期间该
		// OS 线程不会被调度器复用，但整个生命周期仍要显式锁住。
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if !trayCreateWindow(h) {
			ready <- false
			return
		}
		ready <- true
		trayRunMessageLoop(h) // 阻塞到 WM_QUIT；之后窗口与图标已在 WM_DESTROY 清理
	}()
	if !<-ready {
		return nil
	}
	activeTrayMu.Lock()
	activeTray = h
	activeTrayMu.Unlock()
	return h
}

func sessionZero() bool {
	pid, _, _ := procGetCurrentProcessId.Call()
	var sid uint32
	r, _, _ := procProcessIdToSessionId.Call(pid, uintptr(unsafe.Pointer(&sid)))
	return r != 0 && sid == 0
}

// ------------------------------------------------------------ Win32 绑定（仅本文件用到的部分）

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassW        = user32.NewProc("RegisterClassW")
	procCreateWindowExW       = user32.NewProc("CreateWindowExW")
	procDefWindowProcW        = user32.NewProc("DefWindowProcW")
	procGetMessageW           = user32.NewProc("GetMessageW")
	procTranslateMessage      = user32.NewProc("TranslateMessage")
	procDispatchMessageW      = user32.NewProc("DispatchMessageW")
	procPostQuitMessage       = user32.NewProc("PostQuitMessage")
	procPostMessage           = user32.NewProc("PostMessageW")
	procRegisterWindowMessage = user32.NewProc("RegisterWindowMessageW")
	procSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	procCreatePopupMenu       = user32.NewProc("CreatePopupMenu")
	procAppendMenuW           = user32.NewProc("AppendMenuW")
	procTrackPopupMenu        = user32.NewProc("TrackPopupMenu")
	procDestroyMenu           = user32.NewProc("DestroyMenu")
	procGetCursorPos          = user32.NewProc("GetCursorPos")
	procLoadIconW             = user32.NewProc("LoadIconW")
	procCreateIconFromRes     = user32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon           = user32.NewProc("DestroyIcon")
	procShellNotifyIcon       = shell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW         = shell32.NewProc("ShellExecuteW")

	procGetModuleHandleW     = kernel32.NewProc("GetModuleHandleW")
	procGetCurrentProcessId  = kernel32.NewProc("GetCurrentProcessId")
	procProcessIdToSessionId = kernel32.NewProc("ProcessIdToSessionId")
)

type wndClassW struct {
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  uintptr
	LpszClassName uintptr
}

type msgW struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      pointW
}

type pointW struct{ X, Y int32 }

type point2 struct{ X, Y int32 }

// NOTIFYICONDATAW（到 uVersion 联合体 + 气泡字段为止；末尾 guidItem 不需要，
// CbSize 按实际 sizeof 传，legacy 行为兼容）。
type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
}

// wideStr 返回 nul 结尾的 UTF-16 缓冲指针。调用方必须在 syscall.Call 的参数
// 表达式里做 uintptr(unsafe.Pointer(p)) 转换（这样 GC keepalive 才生效，
// 经函数返回 uintptr 的写法会让缓冲在系统调用读取前被回收——实测踩过坑）。
func wideStr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

func setWideBuf(buf []uint16, s string) {
	if len(buf) == 0 {
		return
	}
	v := utf16.Encode([]rune(s))
	if len(v) > len(buf)-1 {
		v = v[:len(buf)-1]
	}
	copy(buf, v)
	for i := len(v); i < len(buf); i++ {
		buf[i] = 0
	}
}

// ------------------------------------------------------------ 消息循环

var trayWndProcPtr = syscall.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
	activeTrayMu.Lock()
	t := activeTray
	activeTrayMu.Unlock()
	if t == nil {
		return defWindowProc(hwnd, msg, wp, lp)
	}
	if tb := t.taskbarCreated; tb != 0 && msg == uintptr(tb) {
		// explorer 重启会收走图标，重新放回去
		t.mu.Lock()
		nid := t.nid
		t.nid = nil
		t.mu.Unlock()
		if nid != nil {
			procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(nid)))
			destroyIcon(nid.HIcon)
		}
		trayAddIcon(t)
		return 0
	}
	switch msg {
	case trayCallbackMsg:
		switch lp {
		case wmDblClick:
			trayOpenPanel(t)
		case wmRButtonUp, wmContextMenu:
			trayShowMenu(hwnd, t)
		}
		return 0
	case trayRefreshMsg:
		trayRefreshTooltip(t)
		return 0
	case wmDestroy:
		t.mu.Lock()
		nid := t.nid
		t.nid = nil
		t.mu.Unlock()
		if nid != nil {
			procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(nid)))
			destroyIcon(nid.HIcon)
		}
		procPostQuitMessage.Call(0)
		return 0
	}
	return defWindowProc(hwnd, msg, wp, lp)
})

func defWindowProc(hwnd, msg, wp, lp uintptr) uintptr {
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

// trayCreateWindow 注册类并创建消息窗口（不进入消息循环）。
func trayCreateWindow(t *trayHandle) bool {
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	clsName := wideStr(trayClassName)
	wc := wndClassW{
		LpfnWndProc:   trayWndProcPtr,
		HInstance:     hInstance,
		LpszClassName: uintptr(unsafe.Pointer(clsName)),
	}
	if r, _, _ := procRegisterClassW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		log.Println("托盘: 注册窗口类失败")
		return false
	}
	hwnd, _, _ := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(clsName)), 0, 0,
		0, 0, 0, 0, 0, 0, hInstance, 0,
	)
	if hwnd == 0 {
		log.Println("托盘: 创建窗口失败")
		return false
	}
	t.mu.Lock()
	t.hwnd = hwnd
	t.mu.Unlock()
	tbName := wideStr("TaskbarCreated")
	r, _, _ := procRegisterWindowMessage.Call(uintptr(unsafe.Pointer(tbName)))
	if r != 0 {
		t.mu.Lock()
		t.taskbarCreated = uint32(r)
		t.mu.Unlock()
	}

	trayAddIcon(t)
	t.mu.Lock()
	url := panelURL(t.state.Listen)
	t.mu.Unlock()
	trayBalloon(t, "sing-box 面板已启动", "双击托盘图标打开 "+url)
	return true
}

// trayRunMessageLoop 泵消息直到 WM_QUIT。
func trayRunMessageLoop(t *trayHandle) {
	var m msgW
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		// 0 = WM_QUIT，-1 = 错误
		if r == 0 || int64(r) == -1 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
	t.mu.Lock()
	t.hwnd = 0
	t.mu.Unlock()
}

func destroyIcon(hicon uintptr) {
	if hicon != 0 {
		procDestroyIcon.Call(hicon)
	}
}

// loadTrayIcon 解析内嵌 .ico 目录取最接近 32px 的条目建 HICON；失败退回系统默认图标。
func loadTrayIcon() uintptr {
	if len(trayICO) >= 6 {
		count := int(uint16(trayICO[4]) | uint16(trayICO[5])<<8)
		best := -1
		bestScore := int32(1 << 30)
		for i := 0; i < count; i++ {
			off := 6 + i*16
			if off+16 > len(trayICO) {
				break
			}
			e := trayICO[off : off+16]
			w := int32(e[0])
			if w == 0 {
				w = 256
			}
			dataLen := uint32(e[8]) | uint32(e[9])<<8 | uint32(e[10])<<16 | uint32(e[11])<<24
			imgOff := uint32(e[12]) | uint32(e[13])<<8 | uint32(e[14])<<16 | uint32(e[15])<<24
			score := w - 32
			if score < 0 {
				score = -score
			}
			if int64(imgOff)+int64(dataLen) <= int64(len(trayICO)) && score < bestScore {
				bestScore = score
				best = i
			}
		}
		if best >= 0 {
			off := 6 + best*16
			e := trayICO[off : off+16]
			dataLen := uint32(e[8]) | uint32(e[9])<<8 | uint32(e[10])<<16 | uint32(e[11])<<24
			imgOff := uint32(e[12]) | uint32(e[13])<<8 | uint32(e[14])<<16 | uint32(e[15])<<24
			h, _, _ := procCreateIconFromRes.Call(
				uintptr(unsafe.Pointer(&trayICO[imgOff])), uintptr(dataLen),
				1, iconRGBA32Version, 32, 32, 0,
			)
			if h != 0 {
				return h
			}
		}
	}
	h, _, _ := procLoadIconW.Call(0, uintptr(idApple))
	return h
}

func trayAddIcon(t *trayHandle) {
	t.mu.Lock()
	hwnd := t.hwnd
	t.mu.Unlock()
	if hwnd == 0 {
		return
	}
	nid := &notifyIconData{
		CbSize:           uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:             hwnd,
		UID:              trayID,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: trayCallbackMsg,
		HIcon:            loadTrayIcon(),
	}
	t.mu.Lock()
	setWideBuf(nid.SzTip[:], trayTipText(t.state))
	t.mu.Unlock()
	if r, _, _ := procShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(nid))); r != 0 {
		t.mu.Lock()
		t.nid = nid
		t.mu.Unlock()
	} else {
		log.Println("托盘: 图标添加失败（任务栏可能未就绪）")
	}
}

func trayRefreshTooltip(t *trayHandle) {
	t.mu.Lock()
	nid := t.nid
	if nid == nil {
		t.mu.Unlock()
		return
	}
	cp := *nid
	setWideBuf(cp.SzTip[:], trayTipText(t.state))
	t.mu.Unlock()
	if r, _, _ := procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&cp))); r != 0 {
		t.mu.Lock()
		t.nid = &cp
		t.mu.Unlock()
	}
}

func trayTipText(s trayState) string {
	state := "已停止"
	if s.CoreRunning {
		state = "运行中"
	}
	return fmt.Sprintf("sing-box 面板 %s\n核心: %s", panelURL(s.Listen), state)
}

func trayBalloon(t *trayHandle, title, text string) {
	t.mu.Lock()
	nid := t.nid
	t.mu.Unlock()
	if nid == nil {
		return
	}
	cp := *nid
	cp.UFlags = nifInfo
	setWideBuf(cp.SzInfo[:], text)
	setWideBuf(cp.SzInfoTitle[:], title)
	cp.DwInfoFlags = niifInfo
	procShellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&cp)))
}

// panelURL 归一化监听地址为浏览器可开的 URL（0.0.0.0/:: → 127.0.0.1）。
func panelURL(listen string) string {
	host, port := listen, ""
	for i := len(listen) - 1; i >= 0; i-- {
		if listen[i] == ':' {
			host, port = listen[:i], listen[i+1:]
			break
		}
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	if port == "" {
		return "http://" + host
	}
	return "http://" + host + ":" + port
}

func trayOpenPanel(t *trayHandle) {
	t.mu.Lock()
	url := panelURL(t.state.Listen)
	t.mu.Unlock()
	log.Println("托盘: 打开", url)
	verb, target := wideStr("open"), wideStr(url)
	procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)), uintptr(unsafe.Pointer(target)), 0, 0, uintptr(swShownorm))
}

func trayShowMenu(hwnd uintptr, t *trayHandle) {
	t.mu.Lock()
	st := t.state
	t.mu.Unlock()

	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	appendMenu := func(flags, id uintptr, text string) {
		p := wideStr(text)
		procAppendMenuW.Call(menu, flags, id, uintptr(unsafe.Pointer(p)))
	}
	appendMenu(mfString, menuOpen, "打开面板")
	coreLabel := "启动核心"
	if st.CoreRunning {
		coreLabel = "停止核心"
	}
	appendMenu(mfString, menuToggle, coreLabel)
	appendMenu(mfString, menuRestart, "重启核心")
	procAppendMenuW.Call(menu, uintptr(mfSeparator), 0, 0)
	autoFlags := uintptr(mfString)
	if st.Autostart {
		autoFlags |= mfChecked
	}
	appendMenu(autoFlags, menuAutostart, "开机自启")
	procAppendMenuW.Call(menu, uintptr(mfSeparator), 0, 0)
	appendMenu(mfString, menuExit, "退出")

	var pt point2
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// 经典焦点坑：不 SetForegroundWindow 菜单不响应点击、点别处也不消失
	procSetForegroundWindow.Call(hwnd)
	cmd, _, _ := procTrackPopupMenu.Call(
		menu, uintptr(tpmRightBtn|tpmReturnCm|tpmNoNotify),
		uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0,
	)
	procPostMessage.Call(hwnd, wmNull, 0, 0)
	procDestroyMenu.Call(menu)

	var ev trayEvent
	switch int(cmd) {
	case menuOpen:
		trayOpenPanel(t)
		return
	case menuToggle:
		ev = trayEventCoreToggle
	case menuRestart:
		ev = trayEventCoreRestart
	case menuAutostart:
		ev = trayEventAutostartToggle
	case menuExit:
		ev = trayEventExit
	default:
		return
	}
	select {
	case t.events <- ev:
	default: // 主循环繁忙时丢弃过剩事件，避免堆积
	}
}
