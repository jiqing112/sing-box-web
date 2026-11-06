//go:build windows

// Windows 系统代理实现：写注册表 Internet Settings + 广播刷新。
//
// 关键点——面板以 SYSTEM 计划任务运行时（主用场景），HKCU 指向 SYSTEM 自己的
// hive，直接写对登录用户完全无效。定位目标 hive 的顺序：
//  1. console 会话优先，console 无登录用户时枚举 WTSActive 会话（覆盖 RDP——
//     实测坑：WTSGetActiveConsoleSessionId 在纯 RDP 场景拿不到用户）；
//  2. WTSQueryUserToken → ImpersonateLoggedOnUser → RegOpenCurrentUser
//     打开真实用户 HKU\<SID>（句柄在还原身份后依然有效）；
//  3. 普通用户面板兜底直写 HKCU。
//
// 全部用 syscall + NewLazyDLL 实现，零新增第三方依赖。写完广播
// InternetSetOption(SETTINGS_CHANGED + REFRESH)，浏览器免重启生效。

package api

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	internetSettingsPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
	wtsCurrentServer     = 0
	wtsUserNameClass     = 5 // WTSUserName
	wtsDomainNameClass   = 7 // WTSDomainName
	wtsActiveState       = 0 // WTSActive
	// InternetSetOption 的两个刷新选项
	optSettingsChanged = 39 // INTERNET_OPTION_SETTINGS_CHANGED
	optRefresh         = 37 // INTERNET_OPTION_REFRESH
)

var (
	modwtsapi32 = syscall.NewLazyDLL("wtsapi32.dll")
	procWTSEnumerateSessionsW      = modwtsapi32.NewProc("WTSEnumerateSessionsW")
	procWTSQueryUserToken          = modwtsapi32.NewProc("WTSQueryUserToken")
	procWTSQuerySessionInformation = modwtsapi32.NewProc("WTSQuerySessionInformationW")
	procWTSFreeMemory              = modwtsapi32.NewProc("WTSFreeMemory")

	modadvapi32                 = syscall.NewLazyDLL("advapi32.dll")
	procImpersonateLoggedOnUser = modadvapi32.NewProc("ImpersonateLoggedOnUser")
	procRevertToSelf            = modadvapi32.NewProc("RevertToSelf")
	procRegOpenCurrentUser      = modadvapi32.NewProc("RegOpenCurrentUser")
	procRegOpenKeyExW           = modadvapi32.NewProc("RegOpenKeyExW")
	procRegQueryValueExW        = modadvapi32.NewProc("RegQueryValueExW")
	procRegSetValueExW          = modadvapi32.NewProc("RegSetValueExW")
	procRegCloseKey             = modadvapi32.NewProc("RegCloseKey")

	modwininet           = syscall.NewLazyDLL("wininet.dll")
	procInternetSetOptio = modwininet.NewProc("InternetSetOptionW")
)

// wtsSessionInfo 对应 WTS_SESSION_INFOW（字段布局必须与 Win32 一致）
type wtsSessionInfo struct {
	SessionID   uint32
	pServerName *uint16
	State       uint32
}

// SysProxyStatus 系统代理当前状态
type SysProxyStatus struct {
	Enabled bool
	Server  string
}

// sysProxySupported 是否支持（仅 Windows）
func sysProxySupported() bool { return true }

// sysProxyStatus 读目标 hive 当前状态（hive 打不开时返回未启用兜底）
func sysProxyStatus() SysProxyStatus {
	hk, _, ok := openHive()
	if !ok {
		return SysProxyStatus{}
	}
	defer syscall.RegCloseKey(hk)
	sub, err := openInternetSettings(hk)
	if err != nil {
		return SysProxyStatus{}
	}
	defer syscall.RegCloseKey(sub)
	enable, _ := regQueryDword(sub, "ProxyEnable")
	server, _ := regQuerySz(sub, "ProxyServer")
	return SysProxyStatus{Enabled: enable == 1, Server: server}
}

// sysProxySet 开/关系统代理。port 为面板 mixed 入站端口；dataDir 存
// 「开启前原始值」状态文件，关闭时还原。
func sysProxySet(on bool, port int, dataDir string) error {
	hk, who, ok := openHive()
	if !ok {
		return fmt.Errorf("打开用户注册表 hive 失败")
	}
	defer syscall.RegCloseKey(hk)
	sub, err := openInternetSettings(hk)
	if err != nil {
		return err
	}
	defer syscall.RegCloseKey(sub)

	if on {
		if err := saveOriginal(sub, dataDir); err != nil {
			return err
		}
		regSetDword(sub, "ProxyEnable", 1)
		regSetSz(sub, "ProxyServer", fmt.Sprintf("127.0.0.1:%d", port))
		cur, _ := regQuerySz(sub, "ProxyOverride")
		if !containsLocal(cur) {
			if cur == "" {
				cur = "localhost;127.0.0.1;<local>"
			} else {
				cur = cur + ";localhost;127.0.0.1;<local>"
			}
			regSetSz(sub, "ProxyOverride", cur)
		}
	} else if err := restoreOriginal(sub, dataDir); err != nil {
		return err
	}
	broadcast()
	action := "关闭"
	if on {
		action = "开启"
	}
	log.Printf("系统代理: 已%s（写入 %s 的 Internet Settings，代理指向 127.0.0.1:%d）", action, who, port)
	return nil
}

// ------------------------------------------------------------ hive 定位

// openHive 返回 (根句柄, 归属描述, 是否成功)。
// 候选会话：console 优先，其次所有 WTSActive 会话（RDP 场景）——
// console 会话可能存在但没有登录用户（实测 RDP 机器上 console 是空壳），
// 所以逐个候选尝试 WTSQueryUserToken，谁成功用谁。
func openHive() (syscall.Handle, string, bool) {
	var candidates []uint32
	if console := activeConsoleSession(); console != 0 {
		candidates = append(candidates, console)
	}
	for _, id := range enumerateActiveSessions() {
		dup := false
		for _, c := range candidates {
			if c == id {
				dup = true
				break
			}
		}
		if !dup {
			candidates = append(candidates, id)
		}
	}
	for _, session := range candidates {
		token, ok := queryUserToken(session)
		if !ok {
			continue
		}
		who := sessionUser(session)
		if r, _, _ := procImpersonateLoggedOnUser.Call(uintptr(token)); r != 0 {
			var hk syscall.Handle
			rr, _, _ := procRegOpenCurrentUser.Call(syscall.KEY_QUERY_VALUE|syscall.KEY_SET_VALUE, uintptr(unsafe.Pointer(&hk)))
			procRevertToSelf.Call()
			syscall.CloseHandle(token)
			if rr == 0 {
				return hk, who, true
			}
		} else {
			syscall.CloseHandle(token)
		}
	}
	// 兜底：面板自身账户（普通用户面板时即真实登录用户）
	return syscall.HKEY_CURRENT_USER, "面板自身账户（HKCU）", true
}

func activeConsoleSession() uint32 {
	// kernel32!WTSGetActiveConsoleSessionId
	modkernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := modkernel32.NewProc("WTSGetActiveConsoleSessionId")
	r, _, _ := proc.Call()
	return uint32(r)
}

// enumerateActiveSessions 枚举所有 WTSActive 会话（console 无登录用户时覆盖 RDP）
func enumerateActiveSessions() []uint32 {
	var ptr *wtsSessionInfo
	var count uint32
	r, _, _ := procWTSEnumerateSessionsW.Call(
		wtsCurrentServer, 0, 1,
		uintptr(unsafe.Pointer(&ptr)), uintptr(unsafe.Pointer(&count)),
	)
	if r == 0 || ptr == nil {
		return nil
	}
	defer procWTSFreeMemory.Call(uintptr(unsafe.Pointer(ptr)))
	var out []uint32
	slice := unsafe.Slice(ptr, count)
	for i := range slice {
		if slice[i].State == wtsActiveState && slice[i].SessionID != 0 {
			out = append(out, slice[i].SessionID)
		}
	}
	return out
}

func queryUserToken(session uint32) (syscall.Handle, bool) {
	var token syscall.Handle
	r, _, _ := procWTSQueryUserToken.Call(uintptr(session), uintptr(unsafe.Pointer(&token)))
	return token, r != 0
}

// sessionUser 查询会话归属用户（DOMAIN\user），用于日志定位写到了哪个 hive
func sessionUser(session uint32) string {
	name := ""
	for _, class := range []uint32{wtsDomainNameClass, wtsUserNameClass} {
		var buf *uint16
		var blen uint32
		r, _, _ := procWTSQuerySessionInformation.Call(
			wtsCurrentServer, uintptr(session), uintptr(class),
			uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&blen)),
		)
		if r == 0 || buf == nil {
			continue
		}
		s := pwstrToString(buf)
		procWTSFreeMemory.Call(uintptr(unsafe.Pointer(buf)))
		if s != "" {
			if name != "" {
				name += "\\"
			}
			name += s
		}
	}
	if name == "" {
		return fmt.Sprintf("会话 %d（用户名查询失败）", session)
	}
	return fmt.Sprintf("用户 %s（会话 %d）", name, session)
}

func pwstrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; ptr = unsafe.Pointer(uintptr(ptr) + 2) {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

// ------------------------------------------------------------ 注册表读写

func openInternetSettings(hk syscall.Handle) (syscall.Handle, error) {
	path, _ := syscall.UTF16PtrFromString(internetSettingsPath)
	var sub syscall.Handle
	r, _, _ := procRegOpenKeyExW.Call(
		uintptr(hk), uintptr(unsafe.Pointer(path)), 0,
		uintptr(syscall.KEY_QUERY_VALUE|syscall.KEY_SET_VALUE), uintptr(unsafe.Pointer(&sub)),
	)
	if r != 0 {
		return 0, fmt.Errorf("打开 Internet Settings 失败 (winerror %d)", r)
	}
	return sub, nil
}

func regQuerySz(h syscall.Handle, name string) (string, bool) {
	var typ uint32
	var need uint32
	n, _ := syscall.UTF16PtrFromString(name)
	if r, _, _ := procRegQueryValueExW.Call(uintptr(h), uintptr(unsafe.Pointer(n)), 0,
		uintptr(unsafe.Pointer(&typ)), 0, uintptr(unsafe.Pointer(&need))); r != 0 || need == 0 {
		return "", false
	}
	buf := make([]byte, need)
	if r, _, _ := procRegQueryValueExW.Call(uintptr(h), uintptr(unsafe.Pointer(n)), 0,
		uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&need))); r != 0 || typ != syscall.REG_SZ {
		return "", false
	}
	u16s := make([]uint16, need/2)
	for i := range u16s {
		u16s[i] = uint16(buf[2*i]) | uint16(buf[2*i+1])<<8
	}
	// 去掉结尾 nul
	for len(u16s) > 0 && u16s[len(u16s)-1] == 0 {
		u16s = u16s[:len(u16s)-1]
	}
	return syscall.UTF16ToString(u16s), true
}

func regQueryDword(h syscall.Handle, name string) (uint32, bool) {
	var typ uint32
	var v uint32
	var need uint32 = 4
	n, _ := syscall.UTF16PtrFromString(name)
	if r, _, _ := procRegQueryValueExW.Call(uintptr(h), uintptr(unsafe.Pointer(n)), 0,
		uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&v)), uintptr(unsafe.Pointer(&need))); r != 0 || typ != syscall.REG_DWORD {
		return 0, false
	}
	return v, true
}

func regSetSz(h syscall.Handle, name, val string) {
	n, _ := syscall.UTF16PtrFromString(name)
	v, _ := syscall.UTF16FromString(val)
	procRegSetValueExW.Call(uintptr(h), uintptr(unsafe.Pointer(n)), 0,
		uintptr(syscall.REG_SZ), uintptr(unsafe.Pointer(&v[0])), uintptr(len(v)*2))
}

func regSetDword(h syscall.Handle, name string, val uint32) {
	n, _ := syscall.UTF16PtrFromString(name)
	procRegSetValueExW.Call(uintptr(h), uintptr(unsafe.Pointer(n)), 0,
		uintptr(syscall.REG_DWORD), uintptr(unsafe.Pointer(&val)), 4)
}

func broadcast() {
	procInternetSetOptio.Call(0, uintptr(optSettingsChanged), 0, 0)
	procInternetSetOptio.Call(0, uintptr(optRefresh), 0, 0)
}

// ------------------------------------------------------------ 开关逻辑

// 「开启前的原始值」，关闭时还原。独立状态文件，不挤占 settings.json。
type sysProxySaved struct {
	Enable      uint32 `json:"enable"`
	Server      string `json:"server"`
	OverrideList string `json:"override"`
}

func statePath(dataDir string) string { return filepath.Join(dataDir, "sysproxy-state.json") }

func containsLocal(s string) bool { return stringContains(s, "<local>") }

func saveOriginal(sub syscall.Handle, dataDir string) error {
	enable, _ := regQueryDword(sub, "ProxyEnable")
	server, _ := regQuerySz(sub, "ProxyServer")
	override, _ := regQuerySz(sub, "ProxyOverride")
	b, err := json.Marshal(sysProxySaved{Enable: enable, Server: server, OverrideList: override})
	if err != nil {
		return err
	}
	if err := os.WriteFile(statePath(dataDir), b, 0o600); err != nil {
		return fmt.Errorf("写入系统代理状态文件失败: %v", err)
	}
	return nil
}

func restoreOriginal(sub syscall.Handle, dataDir string) error {
	b, err := os.ReadFile(statePath(dataDir))
	if err != nil {
		// 无状态文件（非本面板开启）：仅关掉开关
		regSetDword(sub, "ProxyEnable", 0)
		return nil
	}
	var saved sysProxySaved
	if err := json.Unmarshal(b, &saved); err != nil {
		regSetDword(sub, "ProxyEnable", 0)
		return nil
	}
	regSetDword(sub, "ProxyEnable", saved.Enable)
	regSetSz(sub, "ProxyServer", saved.Server)
	regSetSz(sub, "ProxyOverride", saved.OverrideList)
	_ = os.Remove(statePath(dataDir))
	return nil
}

func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
