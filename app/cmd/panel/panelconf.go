package main

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// panelConf 承载 panel.conf 里与部署相关的三项：监听地址、登录账户、登录密码。
// UsernameSet/PasswordSet 用于区分「没写这一项」和「写了但留空」：
// 后者同时出现在两项上时表示明确关闭登录。
type panelConf struct {
	Listen      string
	Username    string
	Password    string
	UsernameSet bool
	PasswordSet bool
}

// loginDisabled 仅当两项都显式留空时成立。
func (c panelConf) loginDisabled() bool {
	return c.UsernameSet && c.PasswordSet && c.Username == "" && c.Password == ""
}

// panelConfPath 固定放在 exe 旁边，与 sing-box.exe / panel-data 的发现逻辑一致，
// 整个目录拷走即完成迁移。
func panelConfPath() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "panel.conf")
	}
	return "panel.conf"
}

// loadPanelConf 读取 panel.conf；文件不存在时先写入一份带注释的模板再返回空值。
func loadPanelConf() panelConf {
	path := panelConfPath()
	f, err := os.Open(path)
	if err != nil {
		writePanelConfTemplate(path)
		return panelConf{}
	}
	defer f.Close()

	var c panelConf
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "listen":
			c.Listen = v
		case "username":
			c.Username = v
			c.UsernameSet = true
		case "password":
			c.Password = v
			c.PasswordSet = true
		}
	}
	return c
}

// writePanelConfListen 把面板内修改的监听地址写回 panel.conf，
// 保留注释与其他配置，重启后仍使用新地址。
func writePanelConfListen(addr string) error {
	return updatePanelConfListen(panelConfPath(), addr)
}

func updatePanelConfListen(path, addr string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(raw)
	eol := "\n"
	if strings.Contains(text, "\r\n") {
		eol = "\r\n"
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}
	lines := strings.Split(text, "\n")
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		k, _, ok := strings.Cut(trimmed, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "listen") {
			lines[i] = "listen=" + addr
			found = true
			break
		}
	}
	if !found {
		if n := len(lines); n > 0 && lines[n-1] == "" {
			lines = append(lines[:n-1], "listen="+addr, "")
		} else {
			lines = append(lines, "listen="+addr)
		}
	}
	content := strings.Join(lines, eol)
	if !strings.HasSuffix(content, eol) {
		content += eol
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".panel.conf.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func writePanelConfTemplate(path string) {
	content := `# sing-box 面板配置（修改后重启面板生效；命令行参数优先于本文件）

# 面板监听地址，例如 127.0.0.1:9090（仅本机访问）或 0.0.0.0:9090（局域网可访问）
listen=127.0.0.1:9090

# 登录用户名/密码；两项都留空表示关闭登录，打开端口直接进入首页
# 只清空 password 会保持现有密码不变；修改用户名时必须同时填写 password
username=admin

# 此文件为明文，请注意保管
password=
`
	fd, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = fd.WriteString(content)
	_ = fd.Close()
	log.Printf("已生成配置模板 %s（可直接编辑监听地址/账户/密码）", path)
}
