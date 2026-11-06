//go:build windows

package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
)

const autostartTask = "sing-box-panel"

func panelAutostartEnabled() (bool, error) {
	if err := exec.Command("schtasks", "/Query", "/TN", autostartTask).Run(); err == nil {
		return true, nil
	}
	return false, nil
}

// setPanelAutostart 用完整任务 XML 注册：SYSTEM + BootTrigger + 失败 1 分钟后重启
// （等价于 README 里的 Register-ScheduledTask 命令，不依赖 PowerShell 模块加载）。
// setPanelAutostart 双路径：先直跑（管理员/SYSTEM 面板零改动、不弹窗）；
// 直跑失败（普通用户必然「拒绝访问」）→ UAC 提权闭环。普通用户连 /Query
// 这个 SYSTEM 任务都被拒，成败判定必须在提权进程内部完成后写结果文件。
func setPanelAutostart(on bool, dataDir string) error {
	if err := setAutostartDirect(on, dataDir); err == nil {
		return nil
	}
	log.Printf("面板开机自启: 直接执行失败，尝试 UAC 提权——请在弹出的授权窗口中点「是」")
	return elevateSetAutostart(on, dataDir)
}

// setAutostartDirect 管理员/SYSTEM 面板的直跑路径（零改动、不弹窗）。
func setAutostartDirect(on bool, dataDir string) error {
	if !on {
		if err := exec.Command("schtasks", "/Delete", "/TN", autostartTask, "/F").Run(); err != nil {
			return errors.New("删除计划任务失败（可能不存在或权限不足）")
		}
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.Abs(self)
	args := ""
	if dataDir != "" {
		args = `-data "` + dataDir + `"`
	}
	tmp := filepath.Join(os.TempDir(), "sing-box-panel.task.xml")
	if err := os.WriteFile(tmp, utf16leBytes(taskXML(escapeXML(self), escapeXML(args))), 0o600); err != nil {
		return err
	}
	out, err := exec.Command("schtasks", "/Create", "/TN", autostartTask, "/XML", tmp, "/F").CombinedOutput()
	_ = os.Remove(tmp)
	if err != nil {
		return errors.New("注册计划任务失败（需要管理员权限）: " + strings.TrimSpace(string(out)))
	}
	return nil
}

// elevateSetAutostart 弹 UAC：提权脚本用 PowerShell -EncodedCommand
// （UTF-16LE base64，无编码/引号坑），任务定义复用 taskXML 的 /XML 方式；
// 脚本内部完成 schtasks + /Query 验证并把结果写入结果文件闭环。
// 安全约束：只服务固定任务名 + 本 exe/数据目录，参数写死，不做通用提权接口。
func elevateSetAutostart(on bool, dataDir string) error {
	result := filepath.Join(os.TempDir(), fmt.Sprintf("sing-box-panel-%d.result", os.Getpid()))
	_ = os.Remove(result)
	var ps string
	psQuote := func(x string) string { return strings.ReplaceAll(x, "'", "''") }
	if on {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		self, _ = filepath.Abs(self)
		args := ""
		if dataDir != "" {
			args = `-data "` + dataDir + `"`
		}
		xml := filepath.Join(os.TempDir(), fmt.Sprintf("sing-box-panel-%d.task.xml", os.Getpid()))
		if err := os.WriteFile(xml, utf16leBytes(taskXML(escapeXML(self), escapeXML(args))), 0o600); err != nil {
			return fmt.Errorf("写入计划任务 XML 失败: %v", err)
		}
		defer os.Remove(xml)
		ps = fmt.Sprintf(
			"schtasks /Create /TN %s /XML '%s' /F\r\n"+
				"if ($LASTEXITCODE -ne 0) { Set-Content -LiteralPath '%s' -Value 'FAIL_CREATE' -Encoding ascii; exit }\r\n"+
				"schtasks /Query /TN %s > $null\r\n"+
				"if ($LASTEXITCODE -eq 0) { Set-Content -LiteralPath '%s' -Value 'OK' -Encoding ascii } else { Set-Content -LiteralPath '%s' -Value 'FAIL_QUERY' -Encoding ascii }",
			autostartTask, psQuote(xml), psQuote(result),
			autostartTask, psQuote(result), psQuote(result))
	} else {
		ps = fmt.Sprintf(
			"schtasks /Delete /TN %s /F\r\n"+
				"if ($LASTEXITCODE -eq 0) { Set-Content -LiteralPath '%s' -Value 'OK' -Encoding ascii; exit }\r\n"+
				"schtasks /Query /TN %s > $null 2>&1\r\n"+
				"if ($LASTEXITCODE -ne 0) { Set-Content -LiteralPath '%s' -Value 'OK' -Encoding ascii } else { Set-Content -LiteralPath '%s' -Value 'FAIL' -Encoding ascii }",
			autostartTask, psQuote(result),
			autostartTask, psQuote(result), psQuote(result))
	}
	enc := base64.StdEncoding.EncodeToString(utf16leNoBom(ps))
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf("Start-Process powershell -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList '-NoProfile','-EncodedCommand','%s'", enc))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	err := cmd.Run()

	raw, _ := os.ReadFile(result)
	_ = os.Remove(result)
	if strings.TrimSpace(string(raw)) == "OK" {
		log.Printf("面板开机自启: UAC 提权执行成功")
		return nil
	}
	detail := fmt.Sprintf("（提权脚本报告: %s）", strings.TrimSpace(string(raw)))
	if err != nil {
		detail = fmt.Sprintf("（提权进程异常退出: %v）", err)
	}
	return fmt.Errorf(
		"需要管理员权限：UAC 未获授权或操作失败%s。可在本机重新点击并在弹窗中确认，或运行 panel.bat autostart-on|off",
		detail)
}

// utf16leNoBom PowerShell -EncodedCommand 约定：UTF-16LE 且无 BOM。
func utf16leNoBom(s string) []byte {
	codes := utf16.Encode([]rune(s))
	out := make([]byte, 2*len(codes))
	for i, c := range codes {
		out[2*i], out[2*i+1] = byte(c), byte(c>>8)
	}
	return out
}

func panelAutostartNote() string {
	return "SYSTEM 计划任务 " + autostartTask + "（开机启动 + 失败 1 分钟后自动重启，以管理员权限运行可带 TUN）"
}

func taskXML(command, args string) string {
	argLine := ""
	if args != "" {
		argLine = "<Arguments>" + args + "</Arguments>"
	}
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers><BootTrigger><Enabled>true</Enabled></BootTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>S-1-5-18</UserId></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure><Interval>PT1M</Interval><Count>3</Count></RestartOnFailure>
  </Settings>
  <Actions Context="Author"><Exec><Command>` + command + `</Command>` + argLine + `</Exec></Actions>
</Task>`
}

func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

func utf16leBytes(s string) []byte {
	codes := utf16.Encode([]rune(s))
	out := make([]byte, 2+2*len(codes))
	out[0], out[1] = 0xFF, 0xFE
	for i, c := range codes {
		out[2+2*i] = byte(c)
		out[3+2*i] = byte(c >> 8)
	}
	return out
}
