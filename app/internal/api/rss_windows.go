//go:build windows

package api

import (
	"syscall"
	"unsafe"
)

var (
	modkernel32              = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess          = modkernel32.NewProc("OpenProcess")
	procCloseHandle          = modkernel32.NewProc("CloseHandle")
	modpsapi                 = syscall.NewLazyDLL("psapi.dll")
	procGetProcessMemoryInfo = modpsapi.NewProc("GetProcessMemoryInfo")
)

const (
	processQueryLimitedInformation = 0x1000
	processVmRead                  = 0x0010
)

// processMemoryCounters 对应 Win32 PROCESS_MEMORY_COUNTERS；
// 数值字段用 uintptr 以同时兼容 386（SIZE_T=4）与 amd64（SIZE_T=8）。
type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// procRSS 通过 psapi 读取进程工作集（Working Set），即任务管理器里的「内存」。
func procRSS(pid int) int64 {
	if pid <= 0 {
		return 0
	}
	h, _, _ := procOpenProcess.Call(processQueryLimitedInformation|processVmRead, 0, uintptr(pid))
	if h == 0 {
		return 0
	}
	defer procCloseHandle.Call(h)

	var pmc processMemoryCounters
	pmc.CB = uint32(unsafe.Sizeof(pmc))
	r, _, _ := procGetProcessMemoryInfo.Call(h, uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.CB))
	if r == 0 {
		return 0
	}
	return int64(pmc.WorkingSetSize)
}
