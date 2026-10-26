//go:build windows

package api

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
func setPanelAutostart(on bool, dataDir string) error {
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
