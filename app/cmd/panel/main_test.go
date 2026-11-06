package main

import (
	"os"
	"path/filepath"
	"testing"

	"singbox-panel/internal/model"
)

func TestUpdatePanelConfListen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.conf")
	content := "# 面板配置\r\nlisten=127.0.0.1:9090\r\nusername=admin\r\npassword=\r\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updatePanelConfListen(path, "0.0.0.0:9090"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# 面板配置\r\nlisten=0.0.0.0:9090\r\nusername=admin\r\npassword=\r\n"
	if string(raw) != want {
		t.Fatalf("content = %q, want %q", raw, want)
	}

	path2 := filepath.Join(dir, "no-listen.conf")
	if err := os.WriteFile(path2, []byte("username=admin\npassword=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updatePanelConfListen(path2, "0.0.0.0:9091"); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path2)
	if want := "username=admin\npassword=\nlisten=0.0.0.0:9091\n"; string(raw) != want {
		t.Fatalf("append content = %q, want %q", raw, want)
	}

	if err := updatePanelConfListen(filepath.Join(dir, "missing.conf"), "127.0.0.1:9090"); err == nil {
		t.Fatal("expected error for missing panel.conf")
	}
}

func TestApplyLoginConfBlankConfKeepsExisting(t *testing.T) {
	// panel.conf 两项留空 + 已有账户：保持现有凭据（关闭登录只能用面板内开关）
	hash := loginHash("admin", "old")
	settings := model.DefaultSettings()
	settings.PasswordHash = hash
	conf := panelConf{UsernameSet: true, PasswordSet: true}
	applyLoginConf(&settings, conf, "")
	if settings.Username != "admin" || settings.PasswordHash != hash {
		t.Fatalf("existing login changed: user=%q hash=%q", settings.Username, settings.PasswordHash)
	}
	if settings.LoginDisabled {
		t.Fatal("LoginDisabled unexpectedly set")
	}
}

func TestApplyLoginConfPanelSwitchOffPersists(t *testing.T) {
	hash := loginHash("admin", "old")
	settings := model.DefaultSettings()
	settings.PasswordHash = hash
	settings.LoginDisabled = true
	conf := panelConf{Username: "admin", UsernameSet: true, PasswordSet: true}
	applyLoginConf(&settings, conf, "")
	if !settings.LoginDisabled {
		t.Fatal("panel switch was reset by restart")
	}
	if settings.PasswordHash != hash || settings.Username != "admin" {
		t.Fatalf("stored credentials changed: user=%q", settings.Username)
	}
}

func TestApplyLoginConfPasswordReenablesPanelSwitch(t *testing.T) {
	settings := model.DefaultSettings()
	settings.PasswordHash = loginHash("admin", "samepass")
	settings.LoginDisabled = true
	conf := panelConf{Username: "admin", Password: "samepass", UsernameSet: true, PasswordSet: true}
	applyLoginConf(&settings, conf, "")
	if settings.LoginDisabled {
		t.Fatal("password in panel.conf did not re-enable login")
	}
}

func TestApplyLoginConfBlankPasswordKeepsExisting(t *testing.T) {
	hash := loginHash("admin", "old")
	settings := model.DefaultSettings()
	settings.PasswordHash = hash
	conf := panelConf{Username: "admin", UsernameSet: true, PasswordSet: true}
	applyLoginConf(&settings, conf, "")
	if settings.PasswordHash != hash || settings.Username != "admin" {
		t.Fatalf("existing login changed: user=%q hash=%q", settings.Username, settings.PasswordHash)
	}
}

func TestApplyLoginConfFreshStaysUninitialized(t *testing.T) {
	// 全新面板且未提供密码：保持待初始化（hash 空、免密开关关闭、不生成任何密码文件）
	dir := t.TempDir()
	settings := model.DefaultSettings()
	conf := panelConf{Username: "admin", UsernameSet: true, PasswordSet: true}
	applyLoginConf(&settings, conf, "")
	if settings.PasswordHash != "" {
		t.Fatalf("password hash generated for fresh panel: %q", settings.PasswordHash)
	}
	if settings.LoginDisabled {
		t.Fatal("LoginDisabled set; must stay eligible for web setup")
	}
	if _, err := os.Stat(filepath.Join(dir, "initial-password")); !os.IsNotExist(err) {
		t.Fatal("initial-password should no longer be generated")
	}
}

func TestApplyLoginConfReenable(t *testing.T) {
	settings := model.Settings{}
	conf := panelConf{Username: "admin", Password: "newpass", UsernameSet: true, PasswordSet: true}
	applyLoginConf(&settings, conf, "")
	if settings.Username != "admin" || settings.PasswordHash != loginHash("admin", "newpass") {
		t.Fatalf("login not re-enabled: user=%q hash=%q", settings.Username, settings.PasswordHash)
	}
}

func TestApplyLoginConfUserOnlyKeepsUninitialized(t *testing.T) {
	// 只填用户名不填密码：保留用户名作为初始化默认值，凭据仍待网页设置
	settings := model.Settings{}
	conf := panelConf{Username: "admin", UsernameSet: true, PasswordSet: true}
	applyLoginConf(&settings, conf, "")
	if settings.Username != "admin" || settings.PasswordHash != "" {
		t.Fatalf("unexpected state: user=%q hash=%q", settings.Username, settings.PasswordHash)
	}
	if settings.LoginDisabled {
		t.Fatal("LoginDisabled set; must stay eligible for web setup")
	}
}
