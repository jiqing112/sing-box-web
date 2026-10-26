//go:build !windows

package api

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const autostartUnit = "sing-box-panel"

func panelAutostartEnabled() (bool, error) {
	out, _ := exec.Command("systemctl", "is-enabled", autostartUnit).CombinedOutput()
	state := strings.TrimSpace(string(out))
	switch state {
	case "enabled":
		return true, nil
	case "disabled", "not-found", "static", "":
		return false, nil
	}
	return false, errors.New("无法查询 systemd 状态: " + state)
}

// setPanelAutostart 只负责 enable/disable；unit 已存在时不改写（保留手工维护的
// 环境变量与参数），缺失时按当前 exe/data 目录生成一份最小 unit。
func setPanelAutostart(on bool, dataDir string) error {
	if os.Geteuid() != 0 {
		return errors.New("修改开机自启需要面板以 root 身份运行")
	}
	if !on {
		if out, err := exec.Command("systemctl", "disable", autostartUnit).CombinedOutput(); err != nil {
			return errors.New("disable 失败: " + strings.TrimSpace(string(out)))
		}
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.Abs(self)
	unitPath := "/etc/systemd/system/" + autostartUnit + ".service"
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		unit := fmt.Sprintf(`[Unit]
Description=sing-box panel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStart=%s -data %s
Restart=on-failure
RestartSec=3
LimitNOFILE=65535
KillMode=control-group

[Install]
WantedBy=multi-user.target
`,
			strconv.Quote(filepath.Dir(self)),
			strconv.Quote(self),
			strconv.Quote(dataDir))
		if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
			return errors.New("写入 unit 失败: " + err.Error())
		}
		_ = exec.Command("systemctl", "daemon-reload").Run()
	}
	if out, err := exec.Command("systemctl", "enable", autostartUnit).CombinedOutput(); err != nil {
		return errors.New("enable 失败: " + strings.TrimSpace(string(out)))
	}
	return nil
}

func panelAutostartNote() string {
	return "systemd 服务 " + autostartUnit + "；开启即 systemctl enable（unit 缺失时自动生成）"
}
