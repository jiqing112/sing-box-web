package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"singbox-panel/internal/model"
	"singbox-panel/internal/store"
)

func TestNormalizeDelayTags(t *testing.T) {
	all := []model.Node{
		{Tag: "a", Enabled: true},
		{Tag: "b", Enabled: false},
		{Tag: "c", Enabled: true},
	}
	got, err := normalizeDelayTags(nil, all)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("enabled fallback = %v, want [a c]", got)
	}

	got, err = normalizeDelayTags([]string{" x ", "x", "", "y", "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("trim/dedupe = %v, want [x y]", got)
	}

	if _, err := normalizeDelayTags([]string{"  "}, nil); err == nil {
		t.Fatal("expected error for empty tag list")
	}
}

func TestNormalizeDelayTagsCap(t *testing.T) {
	tags := make([]string, maxDelayTags+10)
	for i := range tags {
		tags[i] = fmt.Sprintf("t%d", i)
	}
	got, err := normalizeDelayTags(tags, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxDelayTags {
		t.Fatalf("len = %d, want %d", len(got), maxDelayTags)
	}
}

func TestStoreTokenPrunesExpired(t *testing.T) {
	s := &Server{tokens: map[string]time.Time{
		"expired": time.Now().Add(-time.Minute),
	}}
	s.storeToken("fresh")
	if _, ok := s.tokens["expired"]; ok {
		t.Fatal("expired token was not pruned")
	}
	exp, ok := s.tokens["fresh"]
	if !ok || time.Until(exp) <= 0 {
		t.Fatal("fresh token missing or already expired")
	}
}

func TestSubInterval(t *testing.T) {
	cases := []struct {
		in   int
		want time.Duration
	}{
		{0, 24 * time.Hour},
		{-1, 24 * time.Hour},
		{1, time.Hour},
		{24, 24 * time.Hour},
		{720, 720 * time.Hour},
		{721, 24 * time.Hour},
	}
	for _, tc := range cases {
		if got := subInterval(model.Subscription{IntervalHours: tc.in}); got != tc.want {
			t.Fatalf("subInterval(%d) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestLoginRequired(t *testing.T) {
	cases := []struct {
		name string
		set  model.Settings
		want bool
	}{
		{"both empty", model.Settings{}, false},
		{"username only", model.Settings{Username: "admin"}, true},
		{"hash only", model.Settings{PasswordHash: "hash"}, true},
		{"panel switch off", model.Settings{Username: "admin", PasswordHash: "hash", LoginDisabled: true}, false},
	}
	for _, tc := range cases {
		if got := loginRequired(tc.set); got != tc.want {
			t.Fatalf("%s: loginRequired = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAuthBypassWhenLoginDisabled(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SaveSettings(model.Settings{})
	s := &Server{store: st, tokens: map[string]time.Time{}}

	called := false
	h := s.auth(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("disabled auth: code=%d called=%v, want 204 true", rec.Code, called)
	}

	_ = st.SaveSettings(model.Settings{Username: "admin", PasswordHash: "hash"})
	called = false
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if rec.Code != http.StatusUnauthorized || called {
		t.Fatalf("enabled auth: code=%d called=%v, want 401 false", rec.Code, called)
	}

	_ = st.SaveSettings(model.Settings{Username: "admin", PasswordHash: "hash", LoginDisabled: true})
	called = false
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("panel switch off: code=%d called=%v, want 204 true", rec.Code, called)
	}
}

func TestValidateListenAddr(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:9090", "0.0.0.0:1", ":8080", "[::1]:9090"} {
		if err := validateListenAddr(addr); err != nil {
			t.Fatalf("validateListenAddr(%q) = %v", addr, err)
		}
	}
	for _, addr := range []string{"", "9090", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:abc", "a b:80"} {
		if err := validateListenAddr(addr); err == nil {
			t.Fatalf("validateListenAddr(%q) expected error", addr)
		}
	}
}

func TestListenSet(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SaveSettings(model.Settings{Listen: "127.0.0.1:9090"})
	s := &Server{store: st, tokens: map[string]time.Time{}}

	var changed []string
	s.SetListenChanger(func(addr string) error {
		changed = append(changed, addr)
		return nil
	})
	conf := ""
	s.SetListenConf(func(addr string) error {
		conf = addr
		return nil
	})
	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.listenSet(rec, httptest.NewRequest(http.MethodPut, "/api/v1/settings/listen", strings.NewReader(body)))
		return rec
	}

	rec := put(`{"listen":" 0.0.0.0:9090 "}`)
	if rec.Code != 200 {
		t.Fatalf("set: code=%d body=%s", rec.Code, rec.Body)
	}
	if got := st.Settings().Listen; got != "0.0.0.0:9090" {
		t.Fatalf("settings listen = %q", got)
	}
	if conf != "0.0.0.0:9090" || len(changed) != 1 || changed[0] != "0.0.0.0:9090" {
		t.Fatalf("conf=%q changed=%v", conf, changed)
	}

	if rec := put(`{"listen":"nope"}`); rec.Code != 400 {
		t.Fatalf("invalid addr: code=%d, want 400", rec.Code)
	}

	s.SetListenChanger(func(string) error { return fmt.Errorf("boom") })
	if rec := put(`{"listen":"127.0.0.1:9091"}`); rec.Code != 400 {
		t.Fatalf("changer error: code=%d, want 400", rec.Code)
	}
	if got := st.Settings().Listen; got != "0.0.0.0:9090" {
		t.Fatalf("settings changed after failed switch: %q", got)
	}

	s.SetListenChanger(func(string) error { return nil })
	s.SetListenConf(func(string) error { return fmt.Errorf("read only") })
	rec = put(`{"listen":"127.0.0.1:9091"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "panel.conf") {
		t.Fatalf("conf write warning missing: code=%d body=%s", rec.Code, rec.Body)
	}

	s.SetListenConf(func(string) error { return nil })
	s.SetListenPinned(true)
	rec = put(`{"listen":"127.0.0.1:9092"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "-listen") {
		t.Fatalf("pinned warning missing: code=%d body=%s", rec.Code, rec.Body)
	}
}

func TestAuthAccessToggle(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SaveSettings(model.Settings{Username: "admin", PasswordHash: hashPassword("admin", "oldpass123")})
	s := &Server{store: st, tokens: map[string]time.Time{}}

	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/auth/access", strings.NewReader(body))
		s.authAccess(rec, req)
		return rec
	}

	if rec := put(`{"disabled":true}`); rec.Code != 200 {
		t.Fatalf("disable: code=%d body=%s", rec.Code, rec.Body)
	}
	set := st.Settings()
	if !set.LoginDisabled || loginRequired(set) {
		t.Fatalf("not disabled: %+v", set)
	}
	if set.PasswordHash != hashPassword("admin", "oldpass123") {
		t.Fatal("disable cleared stored credentials")
	}

	if rec := put(`{"disabled":false,"username":"admin","password":"short"}`); rec.Code != 400 {
		t.Fatalf("weak password: code=%d, want 400", rec.Code)
	}
	if rec := put(`{"disabled":false,"username":" ","password":"newpass123"}`); rec.Code != 400 {
		t.Fatalf("empty username: code=%d, want 400", rec.Code)
	}

	s.storeToken("stale")
	if rec := put(`{"disabled":false,"username":"ops","password":"newpass123"}`); rec.Code != 200 {
		t.Fatalf("enable: code=%d body=%s", rec.Code, rec.Body)
	}
	set = st.Settings()
	if set.LoginDisabled || !loginRequired(set) {
		t.Fatalf("not enabled: %+v", set)
	}
	if set.Username != "ops" || set.PasswordHash != hashPassword("ops", "newpass123") {
		t.Fatalf("credentials not updated: %+v", set)
	}
	if len(s.tokens) != 0 {
		t.Fatal("old sessions were not invalidated")
	}
}

func TestStoreTokenCapsMap(t *testing.T) {
	s := &Server{tokens: map[string]time.Time{}}
	base := time.Now().Add(time.Hour)
	for i := 0; i < maxTokens; i++ {
		s.tokens[fmt.Sprintf("old%d", i)] = base.Add(time.Duration(i) * time.Second)
	}
	s.storeToken("new")
	if len(s.tokens) != maxTokens {
		t.Fatalf("len = %d, want %d", len(s.tokens), maxTokens)
	}
	if _, ok := s.tokens["old0"]; ok {
		t.Fatal("oldest token was not evicted")
	}
	if _, ok := s.tokens["new"]; !ok {
		t.Fatal("new token missing")
	}
}

func TestLoginLimiterLocksAndResets(t *testing.T) {
	l := &loginLimiter{byIP: map[string]*loginAttempt{}}
	const ip = "192.168.1.9"

	for i := 0; i < loginMaxFails-1; i++ {
		l.fail(ip)
		if _, blocked := l.blocked(ip); blocked {
			t.Fatalf("第 %d 次失败就锁定了，阈值应为 %d", i+1, loginMaxFails)
		}
	}
	l.fail(ip)
	wait, blocked := l.blocked(ip)
	if !blocked {
		t.Fatalf("%d 次失败后应锁定", loginMaxFails)
	}
	if wait <= 0 || wait > loginBaseLock {
		t.Fatalf("锁定时长 = %v, want (0, %v]", wait, loginBaseLock)
	}
	// 其它来源不受影响
	if _, blocked := l.blocked("10.0.0.2"); blocked {
		t.Fatal("不同来源不应共享锁定")
	}
	// 登录成功后清零
	l.succeed(ip)
	if _, blocked := l.blocked(ip); blocked {
		t.Fatal("成功登录后应解除锁定")
	}
}

func TestLoginLimiterBacksOff(t *testing.T) {
	l := &loginLimiter{byIP: map[string]*loginAttempt{}}
	const ip = "192.168.1.10"
	for i := 0; i < loginMaxFails+2; i++ {
		l.fail(ip)
	}
	a := l.byIP[ip]
	if a == nil {
		t.Fatal("missing attempt record")
	}
	lock := time.Until(a.until)
	if lock < loginBaseLock*2 {
		t.Fatalf("连续失败后应延长锁定，实际剩余 %v", lock)
	}
	if lock > loginMaxLock {
		t.Fatalf("锁定不应超过上限 %v，实际 %v", loginMaxLock, lock)
	}
}

func TestVerifyTproxyInbounds(t *testing.T) {
	good := []byte(`{"inbounds":[
		{"type":"mixed","listen_port":7890},
		{"type":"redirect","listen_port":12345},
		{"type":"direct","listen_port":5354}]}`)
	if err := VerifyTproxyInbounds(good, 12345, true); err != nil {
		t.Fatalf("合法配置被拒绝: %v", err)
	}
	if err := VerifyTproxyInbounds(good, 12345, false); err != nil {
		t.Fatalf("不接管 DNS 时不应要求 5354: %v", err)
	}
	if err := VerifyTproxyInbounds(good, 12346, false); err == nil {
		t.Fatal("端口不匹配时应报错")
	}

	noDNS := []byte(`{"inbounds":[{"type":"redirect","listen_port":12345}]}`)
	if err := VerifyTproxyInbounds(noDNS, 12345, true); err == nil {
		t.Fatal("缺少 DNS 入站时应报错")
	}
	if err := VerifyTproxyInbounds(nil, 12345, false); err == nil {
		t.Fatal("空配置应报错")
	}
}
