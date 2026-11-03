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

const procdScript = "/etc/init.d/" + autostartUnit

// initKind 判定面板可用的初始化系统。
type initKind int

const (
	initSystemd initKind = iota
	initProcd
)

// detectInitKind：/run/systemd/system 是 systemd 的标准标志；
// 否则认 OpenWrt（procd）——只有它有 rc.common 体系，写 init 脚本才有意义。
func detectInitKind() (initKind, bool) {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return initSystemd, true
	}
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		return initProcd, true
	}
	if _, err := os.Stat("/lib/functions/procd.sh"); err == nil {
		return initProcd, true
	}
	return 0, false
}

func systemdEnabled() (bool, error) {
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

func systemdSet(on bool, dataDir string) error {
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

// procdEnabled 用 rc.common 的 enabled 动作判断：rc.d 里有 S??symlink 时退出 0。
func procdEnabled() (bool, error) {
	if _, err := os.Stat(procdScript); err != nil {
		return false, nil
	}
	err := exec.Command(procdScript, "enabled").Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, err
}

// procdSet 生成 /etc/init.d/sing-box-panel 并 enable/disable。
// 脚本已存在时不改写（保留手工维护的路径与参数）。
func procdSet(on bool, dataDir string) error {
	if os.Geteuid() != 0 {
		return errors.New("修改开机自启需要面板以 root 身份运行")
	}
	if !on {
		if _, err := os.Stat(procdScript); err != nil {
			return nil
		}
		if out, err := exec.Command(procdScript, "disable").CombinedOutput(); err != nil {
			return errors.New("disable 失败: " + strings.TrimSpace(string(out)))
		}
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.Abs(self)
	if _, err := os.Stat(procdScript); os.IsNotExist(err) {
		// procd 的 respawn：运行满 3600s 后崩溃则计数清零，5s 后重启，0=不限次数
		script := fmt.Sprintf(`#!/bin/sh /etc/rc.common
# sing-box panel 开机自启（由面板生成；改过路径后请在面板里重新开关一次自启）

START=99
STOP=10
USE_PROCD=1

PROG=%s
DATA=%s

start_service() {
	procd_open_instance
	procd_set_param command "$PROG" -data "$DATA"
	procd_set_param respawn 3600 5 0
	procd_set_param stdout 1
	procd_set_param stderr 1
	procd_close_instance
}
`, strconv.Quote(self), strconv.Quote(dataDir))
		if err := os.WriteFile(procdScript, []byte(script), 0o755); err != nil {
			return errors.New("写入 init 脚本失败: " + err.Error())
		}
	}
	if out, err := exec.Command(procdScript, "enable").CombinedOutput(); err != nil {
		return errors.New("enable 失败: " + strings.TrimSpace(string(out)))
	}
	return nil
}

func panelAutostartEnabled() (bool, error) {
	kind, ok := detectInitKind()
	if !ok {
		return false, errors.New("未检测到 systemd 或 OpenWrt procd，无法管理开机自启")
	}
	if kind == initProcd {
		return procdEnabled()
	}
	return systemdEnabled()
}

func setPanelAutostart(on bool, dataDir string) error {
	kind, ok := detectInitKind()
	if !ok {
		return errors.New("未检测到 systemd 或 OpenWrt procd，无法管理开机自启")
	}
	if kind == initProcd {
		return procdSet(on, dataDir)
	}
	return systemdSet(on, dataDir)
}

func panelAutostartNote() string {
	kind, ok := detectInitKind()
	if !ok {
		return "未检测到 systemd 或 OpenWrt procd，开机自启不可用"
	}
	if kind == initProcd {
		return "OpenWrt procd 服务 " + autostartUnit + "；开启即生成 /etc/init.d 脚本并 enable"
	}
	return "systemd 服务 " + autostartUnit + "；开启即 systemctl enable（unit 缺失时自动生成）"
}
