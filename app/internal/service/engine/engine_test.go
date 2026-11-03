package engine

import "testing"

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"":            SingBox,
		"sing-box":    SingBox,
		"singbox":     SingBox,
		"  SING-BOX ": SingBox,
		"mihomo":      Mihomo,
		"clash":       Mihomo,
		"clash-meta":  Mihomo,
		"meta":        Mihomo,
		"nonsense":    SingBox,
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValid(t *testing.T) {
	// 别名也放行：请求体里写 clash / meta 同样是「切到 mihomo」，由 Normalize 归一
	for _, s := range []string{"sing-box", "mihomo", "clash", "clash-meta", "meta"} {
		if !Valid(s) {
			t.Errorf("Valid(%q) = false", s)
		}
	}
	for _, s := range []string{"", "v2ray", "xray", "nope"} {
		if Valid(s) {
			t.Errorf("Valid(%q) = true", s)
		}
	}
}

// 两个内核的配置文件名、扩展名不能撞车，否则切换会互相覆盖。
func TestFilesDiffer(t *testing.T) {
	if ConfigFile(SingBox) == ConfigFile(Mihomo) {
		t.Fatal("config file names must differ")
	}
	if TemplateFile(SingBox) == TemplateFile(Mihomo) {
		t.Fatal("template file names must differ")
	}
	if Ext(SingBox) == Ext(Mihomo) {
		t.Fatal("extensions must differ")
	}
	if Home("/data", SingBox) == Home("/data", Mihomo) {
		t.Fatal("mihomo needs its own home dir (cache.db lock)")
	}
}

func TestRunArgs(t *testing.T) {
	sb := RunArgs(SingBox, "/d/config.json", "/d")
	if len(sb) != 3 || sb[0] != "run" || sb[1] != "-c" || sb[2] != "/d/config.json" {
		t.Errorf("sing-box run args = %v", sb)
	}
	mh := RunArgs(Mihomo, "/d/config.yaml", "/d/mihomo")
	if len(mh) != 4 || mh[0] != "-d" || mh[1] != "/d/mihomo" || mh[2] != "-f" || mh[3] != "/d/config.yaml" {
		t.Errorf("mihomo run args = %v", mh)
	}
}

func TestShortVersion(t *testing.T) {
	cases := []struct{ engine, ver, want string }{
		{SingBox, "sing-box version 1.14.3", "1.14.3"},
		{SingBox, "sing-box version 1.11.0-beta.5", "1.11.0-beta.5"},
		{Mihomo, "Mihomo Meta v1.19.33 windows amd64 with go1.26.10 Wed Oct 21 16:48:02 UTC 2026", "v1.19.33"},
		{Mihomo, "Mihomo Meta alpha-gabcdef", "Mihomo Meta alpha-gabcdef"}, // 认不出来就原样返回
		{SingBox, "", ""},
		{SingBox, "weird", "weird"},
	}
	for _, c := range cases {
		if got := ShortVersion(c.engine, c.ver); got != c.want {
			t.Errorf("ShortVersion(%q, %q) = %q, want %q", c.engine, c.ver, got, c.want)
		}
	}
}

func TestLooksLikeVersion(t *testing.T) {
	for _, s := range []string{"1.14.3", "v1.19.33", "0.9"} {
		if !looksLikeVersion(s) {
			t.Errorf("looksLikeVersion(%q) = false", s)
		}
	}
	for _, s := range []string{"amd64", "go1.26.10", "with", "1", "v"} {
		if looksLikeVersion(s) {
			t.Errorf("looksLikeVersion(%q) = true", s)
		}
	}
}
