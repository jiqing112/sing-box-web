// Package engine 收敛「面板要管理的代理内核」之间的差异。
//
// 面板原本只认 sing-box，配置是 JSON、命令行是 `run -c`、校验是 `check -c`。
// 加入 mihomo 之后这些都不一样：配置是 YAML、命令行是 `-d <home> -f <file>`、
// 校验是 `-t`。把这些差异集中在这一个文件里，上层（store / supervisor /
// config / api）只问「当前引擎是谁」，不再各自写 if。
package engine

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// SingBox 是默认内核，保持与历史行为完全一致。
	SingBox = "sing-box"
	// Mihomo 是 Clash.Meta 的继任者，兼容 Clash 系 YAML 与 Clash API。
	Mihomo = "mihomo"
)

// Supported 列出面板能驱动的全部内核，顺序即前端下拉框的顺序。
func Supported() []string { return []string{SingBox, Mihomo} }

// Normalize 把历史值 / 别名收敛成受支持的引擎名。
// 空字符串视为 sing-box：老 settings.json 没有 engine 字段。
func Normalize(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case Mihomo, "clash", "clash-meta", "clashmeta", "meta":
		return Mihomo
	default:
		return SingBox
	}
}

// Valid 判断输入是否是面板认识的内核名（用于 API 校验，避免把错字静默当成 sing-box）。
func Valid(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case SingBox, Mihomo, "clash", "clash-meta", "clashmeta", "meta":
		return true
	}
	return false
}

func IsMihomo(s string) bool { return Normalize(s) == Mihomo }

// Label 是给人看的名字，用在日志与错误信息里。
func Label(s string) string {
	if IsMihomo(s) {
		return "mihomo"
	}
	return "sing-box"
}

// ConfigFile 返回该引擎在数据目录下的配置文件名。
func ConfigFile(s string) string {
	if IsMihomo(s) {
		return "config.yaml"
	}
	return "config.json"
}

// TemplateFile 返回该引擎在数据目录下的模板文件名。
func TemplateFile(s string) string {
	if IsMihomo(s) {
		return "template.yaml"
	}
	return "template.json"
}

// Ext 返回配置文件后缀，快照文件名用它，避免把 YAML 存成 .json 误导人。
func Ext(s string) string {
	if IsMihomo(s) {
		return ".yaml"
	}
	return ".json"
}

// Home 返回内核的工作目录。
//
// mihomo 的 -d 会把 cache.db 和 geo 数据落在这里，sing-box 的 cache_file 也在
// 数据目录下，两者都是 bbolt 文件、都叫 cache.db，共用同一个文件会互相锁死
// （表现为后启动的内核 initialize cache-file: timeout）。所以 mihomo 单独用一个子目录。
func Home(dataDir, s string) string {
	if IsMihomo(s) {
		return filepath.Join(dataDir, "mihomo")
	}
	return dataDir
}

// RunArgs 构造拉起内核的参数。
//
//	sing-box: run -c <conf>
//	mihomo:   -d <home> -f <conf>
//
// mihomo 的 -d 决定 cache.db / geo 数据库的落盘位置，必须显式给绝对路径，
// 否则会跟着进程工作目录跑。
func RunArgs(s, conf, home string) []string {
	if IsMihomo(s) {
		return []string{"-d", home, "-f", conf}
	}
	return []string{"run", "-c", conf}
}

// CheckArgs 构造校验参数。sing-box 用 `check -c`，mihomo 用 `-t`（test）。
func CheckArgs(s, conf, home string) []string {
	if IsMihomo(s) {
		return []string{"-t", "-d", home, "-f", conf}
	}
	return []string{"check", "-c", conf}
}

// VersionArgs 打印版本，用于探测二进制是否可执行。
func VersionArgs(s string) []string {
	if IsMihomo(s) {
		return []string{"-v"}
	}
	return []string{"version"}
}

// Probe 执行一次版本查询，确认二进制存在且能跑起来。
// 返回版本号（原始输出首行），失败时返回错误。
func Probe(ctx context.Context, bin, s string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, VersionArgs(s)...).CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			return "", err
		}
		return "", &ProbeError{Detail: text}
	}
	if i := strings.IndexByte(text, '\n'); i > 0 {
		text = text[:i]
	}
	return text, nil
}

// ProbeError 带上内核自己打印的错误信息，便于在面板里直接展示。
type ProbeError struct{ Detail string }

func (e *ProbeError) Error() string { return e.Detail }

// ShortVersion 把内核的完整版本串压成短标识，窄卡片里放得下。
// 两个内核的版本输出格式差别很大：
//
//	sing-box: "sing-box version 1.14.3"                          → "1.14.3"
//	mihomo:   "Mihomo Meta v1.19.33 windows amd64 with go1.26..." → "v1.19.33"
//
// 认不出来时原样返回，不猜。
func ShortVersion(s, ver string) string {
	ver = strings.TrimSpace(ver)
	if ver == "" {
		return ""
	}
	fields := strings.Fields(ver)
	// sing-box 的固定格式，取第三段最稳
	if !IsMihomo(s) && len(fields) >= 3 && fields[0] == "sing-box" && fields[1] == "version" {
		return fields[2]
	}
	// mihomo 习惯带 v 前缀
	if IsMihomo(s) {
		for _, f := range fields {
			if len(f) > 1 && (f[0] == 'v' || f[0] == 'V') && f[1] >= '0' && f[1] <= '9' {
				return f
			}
		}
	}
	// 兜底：第一个形如 x.y.z 的字段
	for _, f := range fields {
		if looksLikeVersion(f) {
			return f
		}
	}
	return ver
}

func looksLikeVersion(f string) bool {
	f = strings.TrimPrefix(strings.TrimPrefix(f, "v"), "V")
	parts := strings.SplitN(f, ".", 3)
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for i := 0; i < len(p); i++ {
			if p[i] < '0' || p[i] > '9' {
				return false
			}
		}
	}
	return true
}
