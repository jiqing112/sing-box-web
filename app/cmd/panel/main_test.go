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

func TestApplyLoginConfDisablesWhenBothBlank(t *testing.T) {
	settings := model.DefaultSettings()
	settings.PasswordHash = loginHash("admin", "old")
	conf := panelConf{UsernameSet: true, PasswordSet: true}
	applyLoginConf(&settings, conf, "", t.TempDir())
	if settings.Username != "" || settings.PasswordHash != "" {
		t.Fatalf("login not disabled: user=%q hash=%q", settings.Username, settings.PasswordHash)
	}
	if !settings.LoginDisabled {
		t.Fatal("LoginDisabled flag not set")
	}
}

func TestApplyLoginConfPanelSwitchOffPersists(t *testing.T) {
	hash := loginHash("admin", "old")
	settings := model.DefaultSettings()
	settings.PasswordHash = hash
	settings.LoginDisabled = true
	conf := panelConf{Username: "admin", UsernameSet: true, PasswordSet: true}
	applyLoginConf(&settings, conf, "", t.TempDir())
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
	applyLoginConf(&settings, conf, "", t.TempDir())
	if settings.LoginDisabled {
		t.Fatal("password in panel.conf did not re-enable login")
	}
}

func TestApplyLoginConfBlankPasswordKeepsExisting(t *testing.T) {
	hash := loginHash("admin", "old")
	settings := model.DefaultSettings()
	settings.PasswordHash = hash
	conf := panelConf{Username: "admin", UsernameSet: true, PasswordSet: true}
	applyLoginConf(&settings, conf, "", t.TempDir())
	if settings.PasswordHash != hash || settings.Username != "admin" {
		t.Fatalf("existing login changed: user=%q hash=%q", settings.Username, settings.PasswordHash)
	}
}

func TestApplyLoginConfFreshGeneratesPassword(t *testing.T) {
	dir := t.TempDir()
	settings := model.DefaultSettings()
	conf := panelConf{Username: "admin", UsernameSet: true, PasswordSet: true}
	applyLoginConf(&settings, conf, "", dir)
	if settings.PasswordHash == "" || settings.Username != "admin" {
		t.Fatalf("login not enabled: user=%q hash=%q", settings.Username, settings.PasswordHash)
	}
	if _, err := os.Stat(filepath.Join(dir, "initial-password")); err != nil {
		t.Fatalf("initial-password missing: %v", err)
	}
}

func TestApplyLoginConfReenable(t *testing.T) {
	settings := model.Settings{}
	conf := panelConf{Username: "admin", Password: "newpass", UsernameSet: true, PasswordSet: true}
	applyLoginConf(&settings, conf, "", t.TempDir())
	if settings.Username != "admin" || settings.PasswordHash != loginHash("admin", "newpass") {
		t.Fatalf("login not re-enabled: user=%q hash=%q", settings.Username, settings.PasswordHash)
	}
}

func TestApplyLoginConfUserOnlyStaysDisabled(t *testing.T) {
	settings := model.Settings{}
	conf := panelConf{Username: "admin", UsernameSet: true, PasswordSet: true}
	applyLoginConf(&settings, conf, "", t.TempDir())
	if settings.Username != "" || settings.PasswordHash != "" {
		t.Fatalf("login unexpectedly enabled: user=%q hash=%q", settings.Username, settings.PasswordHash)
	}
}
