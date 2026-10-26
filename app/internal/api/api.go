package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"singbox-panel/internal/model"
	"singbox-panel/internal/service/clash"
	"singbox-panel/internal/service/config"
	"singbox-panel/internal/service/subscription"
	"singbox-panel/internal/service/supervisor"
	"singbox-panel/internal/service/tproxy"
	"singbox-panel/internal/store"
)

// Version 面板版本。前端从 /status 读取，避免页面里再写死一份。
// 用 var 而不是 const：发布流水线通过 -ldflags "-X ...Version=x.y.z" 注入 tag 版本。
var Version = "0.9.5"

const (
	tokenTTL  = 7 * 24 * time.Hour
	maxTokens = 64
	// maxDelayTags 限制单次测速的节点数，防止超大订阅把面板拖死。
	maxDelayTags = 2000
)

type Server struct {
	store *store.Store
	sup   *supervisor.Supervisor
	clash *clash.Client
	app   *config.Applier
	subs  *subscription.Service
	bin   string
	tp    *tproxy.Manager
	start time.Time

	tokenMu sync.RWMutex
	tokens  map[string]time.Time

	throttle loginLimiter

	staticToken string
	lastCheck   config.CheckResult
	checkMu     sync.RWMutex

	listenMu      sync.Mutex
	listenChanger func(string) error
	listenConf    func(string) error
	listenPinned  bool
}

func New(st *store.Store, sup *supervisor.Supervisor, cl *clash.Client, bin, staticToken string, tp *tproxy.Manager) *Server {
	s := &Server{
		tp:          tp,
		store:       st,
		sup:         sup,
		clash:       cl,
		app:         config.NewApplier(st, bin),
		subs:        subscription.New(),
		bin:         bin,
		start:       time.Now(),
		tokens:      map[string]time.Time{},
		throttle:    loginLimiter{byIP: map[string]*loginAttempt{}},
		staticToken: staticToken,
	}
	s.app.SetReload(func() error {
		if !sup.Running() {
			return sup.Start()
		}
		return sup.Restart()
	})
	// 探活要重试：sing-box 从 start 到 Clash API 可服务有几百毫秒的空窗，
	// 只探一次会把成功启动误判为失败并触发回滚。
	// 核心连续崩溃时撤销规则，让主机与局域网回落到直连，而不是死等一个不存在的端口
	sup.SetOnGiveUp(func() {
		log.Printf("核心连续崩溃，撤销透明代理规则以恢复直连")
		_ = tp.Disable()
	})

	s.app.SetProbe(func(ctx context.Context) error {
		var err error
		for {
			if _, err = cl.Version(ctx); err == nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return err
			case <-time.After(200 * time.Millisecond):
			}
		}
	})
	return s
}

/* ------------------------------------------------------------ router */

func (s *Server) Router(static http.Handler) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("GET /api/v1/auth/status", s.authStatus)
	mux.HandleFunc("PUT /api/v1/auth/access", s.auth(s.authAccess))
	mux.HandleFunc("GET /api/v1/auth/check", s.auth(s.check))
	mux.HandleFunc("PUT /api/v1/settings/listen", s.auth(s.listenSet))

	mux.HandleFunc("GET /api/v1/status", s.auth(s.status))
	mux.HandleFunc("GET /api/v1/logs", s.auth(s.logs))
	mux.HandleFunc("POST /api/v1/service/{action}", s.auth(s.service))

	mux.HandleFunc("GET /api/v1/subscriptions", s.auth(s.listSubs))
	mux.HandleFunc("POST /api/v1/subscriptions", s.auth(s.createSub))
	mux.HandleFunc("PUT /api/v1/subscriptions/{id}", s.auth(s.updateSub))
	mux.HandleFunc("DELETE /api/v1/subscriptions/{id}", s.auth(s.deleteSub))
	mux.HandleFunc("POST /api/v1/subscriptions/{id}/refresh", s.auth(s.refreshSub))

	mux.HandleFunc("GET /api/v1/nodes", s.auth(s.listNodes))
	mux.HandleFunc("POST /api/v1/nodes/delay", s.auth(s.delayNodes))
	mux.HandleFunc("PUT /api/v1/nodes/{id}", s.auth(s.toggleNode))

	mux.HandleFunc("GET /api/v1/config", s.auth(s.getConfig))
	mux.HandleFunc("POST /api/v1/config/render", s.auth(s.renderConfig))
	mux.HandleFunc("POST /api/v1/config/validate", s.auth(s.validateConfig))
	mux.HandleFunc("POST /api/v1/config/apply", s.auth(s.applyConfig))
	mux.HandleFunc("GET /api/v1/config/snapshots", s.auth(s.snapshots))
	mux.HandleFunc("POST /api/v1/config/rollback", s.auth(s.rollback))

	mux.HandleFunc("GET /api/v1/tproxy", s.auth(s.tproxyStatus))
	mux.HandleFunc("PUT /api/v1/tproxy", s.auth(s.tproxySet))
	mux.HandleFunc("GET /api/v1/lanproxy", s.auth(s.lanProxyGet))
	mux.HandleFunc("PUT /api/v1/lanproxy", s.auth(s.lanProxySet))
	mux.HandleFunc("GET /api/v1/tun", s.auth(s.tunGet))
	mux.HandleFunc("PUT /api/v1/tun", s.auth(s.tunSet))
	mux.HandleFunc("GET /api/v1/autostart", s.auth(s.autostartGet))
	mux.HandleFunc("PUT /api/v1/autostart", s.auth(s.autostartSet))
	mux.HandleFunc("GET /api/v1/routing", s.auth(s.routingGet))
	mux.HandleFunc("PUT /api/v1/routing", s.auth(s.routingSet))

	mux.HandleFunc("GET /api/v1/ws/connections", s.authWS(s.wsConnections))
	mux.HandleFunc("GET /api/v1/ws/traffic", s.authWS(s.wsTraffic))

	mux.HandleFunc("/api/v1/clash/", s.auth(s.clashProxy))

	// 未知 API 路径返回 JSON 404，而不是掉进静态文件处理器返回 index.html
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, 404, "not_found", "接口不存在: "+r.Method+" "+r.URL.Path)
	})

	mux.Handle("/", static)
	return mux
}

/* ------------------------------------------------------------ auth */

// loginRequired 判断面板是否启用登录。面板内开关关闭登录，或用户名和
// 密码哈希都为空（panel.conf 明确关闭）时，API 与 WebSocket 均不校验 Token。
func loginRequired(set model.Settings) bool {
	if set.LoginDisabled {
		return false
	}
	return set.Username != "" || set.PasswordHash != ""
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	set := s.store.Settings()
	writeJSON(w, 200, map[string]any{
		"required":       loginRequired(set),
		"username":       set.Username,
		"login_disabled": set.LoginDisabled,
	})
}

// authAccess 面板内的登录开关。关闭时保留现有账号密码哈希，重新启用时
// 必须提交新的用户名与密码，避免恢复出一个无人记得的旧密码。
func (s *Server) authAccess(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Disabled bool   `json:"disabled"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", "无法解析请求体")
		return
	}
	set := s.store.Settings()
	if in.Disabled {
		set.LoginDisabled = true
		if err := s.store.SaveSettings(set); err != nil {
			writeErr(w, 500, "save_failed", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "required": false})
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if in.Username == "" {
		writeErr(w, 400, "bad_request", "用户名不能为空")
		return
	}
	if len(in.Password) < 8 {
		writeErr(w, 400, "weak_password", "密码至少 8 位")
		return
	}
	set.Username = in.Username
	set.PasswordHash = hashPassword(in.Username, in.Password)
	set.LoginDisabled = false
	if err := s.store.SaveSettings(set); err != nil {
		writeErr(w, 500, "save_failed", err.Error())
		return
	}
	// 凭据已变更，旧会话一律作废；当前浏览器随后会用新密码重新登录。
	s.tokenMu.Lock()
	s.tokens = map[string]time.Time{}
	s.tokenMu.Unlock()
	writeJSON(w, 200, map[string]any{"ok": true, "required": true})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", "无法解析请求体")
		return
	}
	set := s.store.Settings()
	if !loginRequired(set) {
		writeErr(w, 400, "auth_disabled", "面板未启用登录")
		return
	}
	ip := clientIP(r)
	if wait, blocked := s.throttle.blocked(ip); blocked {
		writeErr(w, 429, "too_many_attempts",
			fmt.Sprintf("登录失败次数过多，请 %d 秒后再试", int(wait.Seconds())+1))
		return
	}
	if in.Username != set.Username || hashPassword(in.Username, in.Password) != set.PasswordHash {
		s.throttle.fail(ip)
		time.Sleep(300 * time.Millisecond)
		writeErr(w, 401, "unauthorized", "用户名或密码错误")
		return
	}
	s.throttle.succeed(ip)
	tok := randomToken()
	s.storeToken(tok)
	writeJSON(w, 200, map[string]any{"token": tok, "user": set.Username})
}

/* ------------------------------------------------------------ login throttle */

// 面板常挂在 0.0.0.0 上，如果不限速，局域网内可以无成本地爆破口令。
// 策略：同一来源连续失败 5 次后锁定 30 秒，之后每多失败一次翻倍，最长 15 分钟。
const (
	loginMaxFails = 5
	loginBaseLock = 30 * time.Second
	loginMaxLock  = 15 * time.Minute
)

type loginAttempt struct {
	fails int
	until time.Time
	last  time.Time
}

type loginLimiter struct {
	mu   sync.Mutex
	byIP map[string]*loginAttempt
}

// blocked 返回是否处于锁定期，以及剩余等待时间。
func (l *loginLimiter) blocked(ip string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.byIP[ip]
	if a == nil || a.until.IsZero() {
		return 0, false
	}
	if left := time.Until(a.until); left > 0 {
		return left, true
	}
	return 0, false
}

func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byIP == nil {
		l.byIP = map[string]*loginAttempt{}
	}
	if len(l.byIP) > 1024 {
		now := time.Now()
		for k, v := range l.byIP {
			if now.Sub(v.last) > time.Hour {
				delete(l.byIP, k)
			}
		}
	}
	a := l.byIP[ip]
	if a == nil {
		a = &loginAttempt{}
		l.byIP[ip] = a
	}
	a.fails++
	a.last = time.Now()
	if a.fails >= loginMaxFails {
		lock := loginBaseLock
		for i := loginMaxFails; i < a.fails; i++ {
			lock *= 2
			if lock >= loginMaxLock {
				lock = loginMaxLock
				break
			}
		}
		a.until = time.Now().Add(lock)
	}
}

func (l *loginLimiter) succeed(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byIP, ip)
}

// clientIP 只取 TCP 对端地址。面板通常直连，不信任 X-Forwarded-For，
// 否则攻击者可以伪造来源绕过限速。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// storeToken 记录登录令牌：先清过期项，再按最旧过期时间淘汰，保证 map 有界。
func (s *Server) storeToken(tok string) {
	now := time.Now()
	s.tokenMu.Lock()
	defer s.tokenMu.Unlock()
	for k, exp := range s.tokens {
		if !now.Before(exp) {
			delete(s.tokens, k)
		}
	}
	for len(s.tokens) >= maxTokens {
		oldest := ""
		var oldestExp time.Time
		for k, exp := range s.tokens {
			if oldest == "" || exp.Before(oldestExp) {
				oldest, oldestExp = k, exp
			}
		}
		if oldest == "" {
			break
		}
		delete(s.tokens, oldest)
	}
	s.tokens[tok] = now.Add(tokenTTL)
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if loginRequired(s.store.Settings()) && !s.validToken(r) {
			writeErr(w, 401, "unauthorized", "未登录或登录已过期")
			return
		}
		next(w, r)
	}
}

func (s *Server) authWS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !loginRequired(s.store.Settings()) {
			next(w, r)
			return
		}
		if !s.validToken(r) && r.URL.Query().Get("token") == "" {
			writeErr(w, 401, "unauthorized", "未登录")
			return
		}
		if r.URL.Query().Get("token") != "" && !s.validTokenString(r.URL.Query().Get("token")) {
			writeErr(w, 401, "unauthorized", "token 无效")
			return
		}
		next(w, r)
	}
}

func (s *Server) validToken(r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return s.validTokenString(strings.TrimPrefix(h, "Bearer "))
	}
	return false
}

func (s *Server) validTokenString(tok string) bool {
	if tok == "" {
		return false
	}
	if s.staticToken != "" && tok == s.staticToken {
		return true
	}
	s.tokenMu.RLock()
	defer s.tokenMu.RUnlock()
	exp, ok := s.tokens[tok]
	return ok && time.Now().Before(exp)
}

func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true})
}

/* ------------------------------------------------------------ listen */

// SetListenChanger 由 main 注入热切换实现；测试环境可以留空。
func (s *Server) SetListenChanger(fn func(string) error) { s.listenChanger = fn }

// SetListenConf 由 main 注入 panel.conf 写回实现，保证重启后仍生效。
func (s *Server) SetListenConf(fn func(string) error) { s.listenConf = fn }

// SetListenPinned 标记启动参数是否显式指定了 -listen（优先级最高）。
func (s *Server) SetListenPinned(v bool) { s.listenPinned = v }

func validateListenAddr(addr string) error {
	if addr == "" {
		return fmt.Errorf("监听地址不能为空")
	}
	if len(addr) > 255 {
		return fmt.Errorf("监听地址过长")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("格式应为 host:port，例如 0.0.0.0:9090")
	}
	if strings.ContainsAny(host, " \t\r\n") {
		return fmt.Errorf("监听地址不能包含空白字符")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("端口必须在 1-65535 之间")
	}
	return nil
}

func (s *Server) listenSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Listen string `json:"listen"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", "无法解析请求体")
		return
	}
	addr := strings.TrimSpace(in.Listen)
	if err := validateListenAddr(addr); err != nil {
		writeErr(w, 400, "bad_listen", err.Error())
		return
	}

	s.listenMu.Lock()
	defer s.listenMu.Unlock()
	set := s.store.Settings()
	warning := ""
	if s.listenPinned {
		warning = "启动参数 -listen 已指定监听地址，重启后以该参数为准"
	}
	if addr == set.Listen {
		writeJSON(w, 200, map[string]any{"ok": true, "listen": addr, "warning": warning})
		return
	}
	if s.listenChanger == nil {
		writeErr(w, 500, "unsupported", "当前进程不支持热切换监听地址")
		return
	}
	prev := set.Listen
	if err := s.listenChanger(addr); err != nil {
		writeErr(w, 400, "listen_failed", "切换监听地址失败: "+err.Error())
		return
	}
	set.Listen = addr
	if err := s.store.SaveSettings(set); err != nil {
		_ = s.listenChanger(prev)
		writeErr(w, 500, "save_failed", err.Error())
		return
	}
	if s.listenConf != nil {
		if err := s.listenConf(addr); err != nil {
			if warning != "" {
				warning += "；"
			}
			warning += "panel.conf 写入失败：" + err.Error() + "，重启后监听地址可能回退"
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "listen": addr, "warning": warning})
}

func hashPassword(user, pass string) string {
	sum := sha256.Sum256([]byte(user + ":" + pass))
	return hex.EncodeToString(sum[:])
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

/* ------------------------------------------------------------ status */

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	running, pid, started, lastErr := s.sup.Status()
	nodes := s.store.AllNodes()
	enabled := 0
	for _, n := range nodes {
		if n.Enabled {
			enabled++
		}
	}

	rules := 0
	cfgVer := s.store.Settings().ConfigVersion
	raw, _ := s.store.Config()
	if len(raw) > 0 {
		var doc struct {
			Route struct {
				Rules []any `json:"rules"`
			} `json:"route"`
		}
		if json.Unmarshal(raw, &doc) == nil {
			rules = len(doc.Route.Rules)
		}
	}

	version := ""
	active := ""
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if running {
		if v, err := s.clash.Version(ctx); err == nil {
			version = v
		}
		if now := s.clash.ResolveActive(ctx, "proxy"); now != "" {
			active = now
		}
	}

	s.checkMu.RLock()
	last := s.lastCheck
	s.checkMu.RUnlock()
	set := s.store.Settings()
	if set.LastCheckAt != "" {
		last.OK = set.LastCheckOK
		last.Output = "" // 详情只在当次校验时展示
		last.Elapsed = 0
	}

	uptime := int64(0)
	if !started.IsZero() {
		uptime = int64(time.Since(started).Seconds())
	}

	writeJSON(w, 200, map[string]any{
		"running":        running,
		"pid":            pid,
		"started_at":     started,
		"uptime":         uptime,
		"version":        version,
		"active":         active,
		"panel_version":  Version,
		"arch":           runtime.GOOS + "/" + runtime.GOARCH,
		"mixed_port":     detectMixedPort(raw),
		"config_version": cfgVer,
		"nodes":          len(nodes),
		"enabled":        enabled,
		"rules":          rules,
		"rss":            procRSS(pid),
		"panel_rss":      procRSS(os.Getpid()),
		"listen":         s.store.Settings().Listen,
		"listen_pinned":  s.listenPinned,
		"data_dir":       s.store.Dir(),
		"login_required": loginRequired(set),
		"login_disabled": set.LoginDisabled,
		"username":       set.Username,
		"keep_snapshots": set.KeepSnapshots,
		"delay_workers":  set.DelayWorkers,
		"delay_ttl":      set.DelayTTL,
		"tproxy":         s.store.Settings().TproxyMode,
		"subs":           len(s.store.Subscriptions()),
		"last_check":     last.Output,
		"last_check_ok":  last.OK,
		"last_check_at":  set.LastCheckAt,
		"last_error":     lastErr,
		"panel_uptime":   int64(time.Since(s.start).Seconds()),
		"platform":       runtime.GOOS,
		"now":            time.Now(),
	})
}

// recordCheck 把最近一次校验结果落到 settings.json，重启后仍能显示。
func (s *Server) recordCheck(ok bool) {
	s.store.SetLastCheck(ok, time.Now().Format(time.RFC3339))
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"lines": s.sup.Logs()})
}

func (s *Server) service(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	var err error
	set := s.store.Settings()
	switch action {
	case "start":
		s.sup.ResetCrashes()
		err = s.sup.Start()
		if errors.Is(err, supervisor.ErrRunning) {
			// 已经在跑：当成成功，避免「启动」按钮弹出无意义的错误
			err = nil
		}
		if err == nil && set.TproxyEnabled {
			cidrs := set.LanCIDRs
			if len(cidrs) == 0 {
				cidrs = tproxy.DetectLAN()
			}
			if e := s.tp.Apply(cidrs, set.TproxyLocal, set.TproxyDNS); e != nil {
				log.Printf("恢复透明代理规则失败: %v", e)
			}
		}
	case "stop":
		err = s.sup.Stop()
		// 核心停了就撤掉规则，否则流量会被重定向到没人监听的端口
		if err == nil && set.TproxyEnabled {
			_ = s.tp.Disable()
		}
	case "restart":
		err = s.sup.Restart()
	default:
		writeErr(w, 400, "bad_action", "未知操作")
		return
	}
	if err != nil {
		s.failSafeNetwork()
		writeErr(w, 500, "service_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// failSafeNetwork 在核心确认已停止时撤掉透明代理规则。
// 核心不在、规则还在时，主机与局域网的流量会被重定向到一个没人监听的端口，
// 表现为整网断网（例如「重启」时新核心起不来）。
func (s *Server) failSafeNetwork() {
	if !s.tp.Supported() || s.sup.Running() {
		return
	}
	if !s.store.Settings().TproxyEnabled {
		return
	}
	if err := s.tp.Disable(); err != nil {
		log.Printf("核心已停止，撤销透明代理规则失败: %v", err)
		return
	}
	log.Printf("核心已停止，已撤销透明代理规则（避免整网断网）")
}

/* ------------------------------------------------------------ subscriptions */

const (
	defaultSubIntervalHours = 24
	maxSubIntervalHours     = 720 // 30 天
)

func validSubInterval(h int) bool { return h >= 1 && h <= maxSubIntervalHours }

// subInterval 返回订阅的实际更新间隔；旧版 subs.json 没有该字段，按 24 小时处理。
func subInterval(sub model.Subscription) time.Duration {
	h := sub.IntervalHours
	if !validSubInterval(h) {
		h = defaultSubIntervalHours
	}
	return time.Duration(h) * time.Hour
}

func (s *Server) listSubs(w http.ResponseWriter, r *http.Request) {
	subs := s.store.Subscriptions()
	if subs == nil {
		subs = []model.Subscription{}
	}
	writeJSON(w, 200, map[string]any{"subscriptions": subs})
}

func (s *Server) createSub(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          string `json:"name"`
		URL           string `json:"url"`
		Auto          *bool  `json:"auto"`
		IntervalHours int    `json:"interval_hours"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in); err != nil || in.URL == "" {
		writeErr(w, 400, "bad_request", "订阅链接不能为空")
		return
	}
	if in.Name == "" {
		in.Name = "订阅 " + time.Now().Format("01-02 15:04")
	}
	if in.IntervalHours == 0 {
		in.IntervalHours = defaultSubIntervalHours
	}
	if !validSubInterval(in.IntervalHours) {
		writeErr(w, 400, "bad_interval", "更新间隔需在 1-720 小时之间")
		return
	}
	auto := true
	if in.Auto != nil {
		auto = *in.Auto
	}
	sub := model.Subscription{
		ID:            fmt.Sprintf("s%d", time.Now().UnixNano()/1e6),
		Name:          in.Name,
		URL:           in.URL,
		Auto:          auto,
		IntervalHours: in.IntervalHours,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	nodes, info, err := s.subs.Refresh(ctx, sub, nil, s.reservedTags(""))
	if err != nil {
		writeErr(w, 502, "fetch_failed", err.Error())
		return
	}
	if err := s.store.SaveNodes(sub.ID, nodes); err != nil {
		writeErr(w, 500, "save_failed", err.Error())
		return
	}
	sub.NodeCount = len(nodes)
	sub.UpdatedAt = time.Now()
	sub.UsageUsed = info.Upload + info.Download
	sub.UsageTotal = info.Total
	sub.Expire = info.Expire
	if err := s.store.UpsertSubscription(sub); err != nil {
		writeErr(w, 500, "save_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"subscription": sub, "nodes": len(nodes)})
}

// reservedTags 收集其他订阅已占用的 tag，避免跨订阅重名。
func (s *Server) reservedTags(exceptSubID string) map[string]bool {
	out := map[string]bool{}
	for _, n := range s.store.AllNodes() {
		if n.SubID != exceptSubID {
			out[n.Tag] = true
		}
	}
	return out
}

func (s *Server) updateSub(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sub, err := s.store.Subscription(id)
	if err != nil {
		writeErr(w, 404, "not_found", "订阅不存在")
		return
	}
	var in struct {
		Name          string `json:"name"`
		URL           string `json:"url"`
		Auto          *bool  `json:"auto"`
		IntervalHours *int   `json:"interval_hours"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", "请求体不合法")
		return
	}
	if in.Name != "" {
		sub.Name = in.Name
	}
	if in.URL != "" {
		sub.URL = in.URL
	}
	if in.Auto != nil {
		sub.Auto = *in.Auto
	}
	if in.IntervalHours != nil {
		if !validSubInterval(*in.IntervalHours) {
			writeErr(w, 400, "bad_interval", "更新间隔需在 1-720 小时之间")
			return
		}
		sub.IntervalHours = *in.IntervalHours
	}
	if err := s.store.UpsertSubscription(sub); err != nil {
		writeErr(w, 500, "save_failed", err.Error())
		return
	}
	writeJSON(w, 200, sub)
}

func (s *Server) deleteSub(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSubscription(r.PathValue("id")); err != nil {
		writeErr(w, 500, "delete_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) refreshSub(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sub, err := s.store.Subscription(id)
	if err != nil {
		writeErr(w, 404, "not_found", "订阅不存在")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	n, err := s.refreshOne(ctx, sub)
	if err != nil {
		writeErr(w, 502, "fetch_failed", err.Error())
		return
	}
	sub, _ = s.store.Subscription(id)
	writeJSON(w, 200, map[string]any{"subscription": sub, "nodes": n})
}

// refreshOne 拉取并保存单个订阅，失败时只记录错误，不影响其它订阅。
func (s *Server) refreshOne(ctx context.Context, sub model.Subscription) (int, error) {
	prev, _ := s.store.Nodes(sub.ID)
	nodes, info, err := s.subs.Refresh(ctx, sub, prev, s.reservedTags(sub.ID))
	if err != nil {
		sub.LastError = err.Error()
		_ = s.store.UpsertSubscription(sub)
		return 0, err
	}
	if err := s.store.SaveNodes(sub.ID, nodes); err != nil {
		return 0, err
	}
	sub.NodeCount = len(nodes)
	sub.UpdatedAt = time.Now()
	sub.LastError = ""
	if info.Total > 0 {
		sub.UsageUsed = info.Upload + info.Download
		sub.UsageTotal = info.Total
		sub.Expire = info.Expire
	}
	_ = s.store.UpsertSubscription(sub)
	return len(nodes), nil
}

// StartScheduler 每分钟检查一次，按每个订阅自己的间隔重新拉取；
// 旧数据没有 interval_hours 时按 24 小时处理。
func (s *Server) StartScheduler(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	// 失败重试下限：订阅源故障时不至于每分钟重试、刷日志。
	const retryFloor = 10 * time.Minute
	lastFail := map[string]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, sub := range s.store.Subscriptions() {
				if !sub.Auto {
					continue
				}
				interval := subInterval(sub)
				if time.Since(sub.UpdatedAt) < interval {
					continue
				}
				if t, ok := lastFail[sub.ID]; ok && time.Since(t) < retryFloor {
					continue
				}
				rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				n, err := s.refreshOne(rctx, sub)
				cancel()
				if err != nil {
					lastFail[sub.ID] = time.Now()
					log.Printf("自动更新 %s 失败: %v", sub.Name, err)
				} else {
					delete(lastFail, sub.ID)
					log.Printf("自动更新 %s 完成，%d 个节点", sub.Name, n)
				}
			}
		}
	}
}

/* ------------------------------------------------------------ nodes */

func (s *Server) listNodes(w http.ResponseWriter, r *http.Request) {
	nodes := s.store.AllNodes()
	if nodes == nil {
		nodes = []model.Node{}
	}
	writeJSON(w, 200, map[string]any{
		"nodes":   nodes,
		"delays":  s.clash.DelaysJSON(),
		"running": s.sup.Running(),
	})
}

// delayNodes 并发测速。worker 数由 settings.DelayWorkers 控制（上限 16），
// 用固定 worker 池而不是「每个 tag 一个 goroutine」，避免大订阅瞬间起上千协程。
func (s *Server) delayNodes(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Tags []string `json:"tags"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in)
	if !s.sup.Running() {
		writeErr(w, 409, "core_stopped", "sing-box 未运行，无法测速")
		return
	}

	tags, err := normalizeDelayTags(in.Tags, s.store.AllNodes())
	if err != nil {
		writeErr(w, 400, "no_tags", err.Error())
		return
	}
	workers := s.store.Settings().DelayWorkers
	if workers <= 0 {
		workers = 3
	}
	if workers > 16 {
		workers = 16
	}
	if workers > len(tags) {
		workers = len(tags)
	}

	type result struct {
		Tag string `json:"tag"`
		MS  int    `json:"ms"`
	}
	results := make([]result, len(tags))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
				ms, err := s.clash.Delay(ctx, tags[idx], 5000)
				cancel()
				if err != nil {
					ms = -1
				}
				results[idx] = result{Tag: tags[idx], MS: ms}
			}
		}()
	}
	for i := range tags {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	writeJSON(w, 200, map[string]any{"results": results, "delays": s.clash.DelaysJSON()})
}

// normalizeDelayTags 去空白、去重并按上限截断；空列表时取全部启用节点。
func normalizeDelayTags(tags []string, all []model.Node) ([]string, error) {
	if len(tags) == 0 {
		for _, n := range all {
			if n.Enabled {
				tags = append(tags, n.Tag)
			}
		}
	}
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
		if len(out) >= maxDelayTags {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有可测速的节点")
	}
	return out, nil
}

// detectMixedPort 从已下发配置里读 mixed 入站端口，供前端展示真实入站。
func detectMixedPort(raw []byte) int {
	var doc struct {
		Inbounds []struct {
			Type       string `json:"type"`
			ListenPort int    `json:"listen_port"`
		} `json:"inbounds"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &doc) != nil {
		return 0
	}
	for _, in := range doc.Inbounds {
		if in.Type == "mixed" && in.ListenPort > 0 {
			return in.ListenPort
		}
	}
	return 0
}

func (s *Server) toggleNode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", "请求体不合法")
		return
	}
	if err := s.store.SetNodeEnabled(r.PathValue("id"), in.Enabled); err != nil {
		writeErr(w, 404, "not_found", "节点不存在")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

/* ------------------------------------------------------------ config */

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	raw, err := s.store.Config()
	if err != nil {
		writeErr(w, 500, "read_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"raw": string(raw), "snapshots": s.store.Snapshots()})
}

func (s *Server) renderConfig(w http.ResponseWriter, r *http.Request) {
	tpl, err := s.store.Template()
	if err != nil {
		writeErr(w, 500, "template_failed", err.Error())
		return
	}
	set := s.store.Settings()
	raw, err := config.Render(tpl, s.store.AllNodes(), config.RenderOpts{
		DataDir:  s.store.Dir(),
		LanProxy: set.LanProxy, SocksPort: set.LanSocksPort, HTTPPort: set.LanHTTPPort,
		User: set.LanUser, Pass: set.LanPass,
		TunEnabled:  set.TunEnabled,
		RoutingMode: set.RoutingMode,
	})
	if err != nil {
		writeErr(w, 400, "render_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"raw": string(raw)})
}

func (s *Server) validateConfig(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Raw string `json:"raw"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&in); err != nil || in.Raw == "" {
		writeErr(w, 400, "bad_request", "缺少配置内容")
		return
	}
	res := config.Check(r.Context(), s.bin, []byte(in.Raw))
	s.checkMu.Lock()
	s.lastCheck = res
	s.checkMu.Unlock()
	s.recordCheck(res.OK)
	writeJSON(w, 200, res)
}

func (s *Server) applyConfig(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Raw  string `json:"raw"`
		Note string `json:"note"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&in); err != nil || in.Raw == "" {
		writeErr(w, 400, "bad_request", "缺少配置内容")
		return
	}
	if in.Note == "" {
		in.Note = "手动保存"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	if err := s.app.Apply(ctx, []byte(in.Raw), in.Note); err != nil {
		s.failSafeNetwork()
		status := 400
		if ce, ok := err.(*config.CheckError); ok {
			s.checkMu.Lock()
			s.lastCheck = config.CheckResult{OK: false, Output: ce.Detail}
			s.checkMu.Unlock()
			status = 422
		}
		writeErr(w, status, "apply_failed", err.Error())
		return
	}
	s.recordCheck(true)
	writeJSON(w, 200, map[string]any{"ok": true, "snapshots": s.store.Snapshots()})
}

func (s *Server) snapshots(w http.ResponseWriter, r *http.Request) {
	list := s.store.Snapshots()
	if list == nil {
		list = []model.Snapshot{}
	}
	writeJSON(w, 200, map[string]any{"snapshots": list})
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version int `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&in); err != nil || in.Version == 0 {
		writeErr(w, 400, "bad_request", "缺少版本号")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	if err := s.app.Rollback(ctx, in.Version); err != nil {
		s.failSafeNetwork()
		writeErr(w, 400, "rollback_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) tproxyStatus(w http.ResponseWriter, r *http.Request) {
	set := s.store.Settings()
	cidrs := set.LanCIDRs
	if len(cidrs) == 0 {
		cidrs = tproxy.DetectLAN()
	}
	st := s.tp.Status(cidrs)
	writeJSON(w, 200, map[string]any{
		"enabled":         st.Enabled,
		"supported":       s.tp.Supported(),
		"local":           st.Local,
		"dns":             st.DNS,
		"local_supported": s.tp.LocalSupported(),
		"mode":            set.TproxyMode,
		"port":            st.Port,
		"rules":           st.Rules,
		"cidrs":           cidrs,
		"detected":        tproxy.DetectLAN(),
		"note":            "redirect 仅接管 TCP；目标为内网的流量直接放行",
	})
}

// tproxySet 打开/关闭透明代理。打开时下发 nftables，关闭时删表。
func (s *Server) tproxySet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool     `json:"enabled"`
		Local   *bool    `json:"local"`
		DNS     *bool    `json:"dns"`
		CIDRs   []string `json:"cidrs"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", "请求体不合法")
		return
	}

	set := s.store.Settings()
	prev := set
	cidrs := in.CIDRs
	if len(cidrs) == 0 {
		cidrs = set.LanCIDRs
	}
	if len(cidrs) == 0 {
		cidrs = tproxy.DetectLAN()
	}

	local := set.TproxyLocal
	if in.Local != nil {
		local = *in.Local
	}
	hijackDNS := set.TproxyDNS
	if in.DNS != nil {
		hijackDNS = *in.DNS
	}

	if in.Enabled {
		if !s.tp.Supported() {
			writeErr(w, 400, "unsupported", "透明代理依赖 nftables，仅支持 Linux")
			return
		}
		if !s.sup.Running() {
			writeErr(w, 409, "core_stopped", "请先启动 sing-box 核心，再开启透明代理")
			return
		}
		if err := s.tp.Preflight(set.TproxyPort); err != nil {
			writeErr(w, 400, "preflight_failed", err.Error())
			return
		}
		if err := s.verifyTproxyInbounds(set.TproxyPort, hijackDNS); err != nil {
			writeErr(w, 400, "inbound_missing", err.Error())
			return
		}
		if err := s.tp.Apply(cidrs, local, hijackDNS); err != nil {
			writeErr(w, 500, "nft_failed", err.Error())
			return
		}
	} else if s.tp.Supported() {
		// 不支持的平台（Windows）本来就没有 nftables 规则，
		// 直接当作「已关闭」处理，否则前端关开关会收到 500。
		if err := s.tp.Disable(); err != nil {
			writeErr(w, 500, "nft_failed", err.Error())
			return
		}
	}

	set.TproxyEnabled = in.Enabled
	set.TproxyLocal = local
	set.TproxyDNS = hijackDNS
	set.LanCIDRs = cidrs
	if err := s.store.SaveSettings(set); err != nil {
		// 规则已变更但设置没落盘时，把 nftables 恢复到改动前的状态
		if prev.TproxyEnabled {
			prevCIDRs := prev.LanCIDRs
			if len(prevCIDRs) == 0 {
				prevCIDRs = cidrs
			}
			_ = s.tp.Apply(prevCIDRs, prev.TproxyLocal, prev.TproxyDNS)
		} else {
			_ = s.tp.Disable()
		}
		writeErr(w, 500, "save_failed", err.Error())
		return
	}
	st := s.tp.Status(cidrs)
	writeJSON(w, 200, map[string]any{"ok": true, "enabled": st.Enabled, "local": st.Local, "dns": st.DNS, "rules": st.Rules, "cidrs": cidrs})
}

// verifyTproxyInbounds 确认已下发的配置里确实监听了规则要重定向到的端口。
// 否则 nftables 一生效，局域网流量就会被重定向到无人监听的端口而断网。
func (s *Server) verifyTproxyInbounds(port int, hijackDNS bool) error {
	raw, err := s.store.Config()
	if err != nil || len(raw) == 0 {
		return fmt.Errorf("当前没有已下发的配置")
	}
	return VerifyTproxyInbounds(raw, port, hijackDNS)
}

// VerifyTproxyInbounds 是 verifyTproxyInbounds 的纯函数版本，供启动时自检复用。
func VerifyTproxyInbounds(raw []byte, port int, hijackDNS bool) error {
	var doc struct {
		Inbounds []struct {
			Type       string `json:"type"`
			ListenPort int    `json:"listen_port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("配置解析失败: %w", err)
	}
	hasRedirect, hasDNS := false, false
	for _, in := range doc.Inbounds {
		if in.Type == "redirect" && in.ListenPort == port {
			hasRedirect = true
		}
		if in.ListenPort == 5354 {
			hasDNS = true
		}
	}
	if !hasRedirect {
		return fmt.Errorf("当前配置没有监听 :%d 的 redirect 入站，请先应用包含该入站的配置", port)
	}
	if hijackDNS && !hasDNS {
		return fmt.Errorf("当前配置没有监听 :5354 的 DNS 入站，无法接管局域网 DNS")
	}
	return nil
}

/* ------------------------------------------------------------ lan proxy */

func (s *Server) lanProxyGet(w http.ResponseWriter, r *http.Request) {
	set := s.store.Settings()
	host, _, _ := net.SplitHostPort(r.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	socksURL, httpURL := lanProxyURLs(host, set.LanSocksPort, set.LanHTTPPort, set.LanUser, set.LanPass)
	writeJSON(w, 200, map[string]any{
		"enabled":      set.LanProxy,
		"socks_port":   set.LanSocksPort,
		"http_port":    set.LanHTTPPort,
		"username":     set.LanUser,
		"password":     set.LanPass,
		"has_password": set.LanPass != "",
		"open":         set.LanProxy && set.LanUser == "" && set.LanPass == "",
		"socks_url":    socksURL,
		"http_url":     httpURL,
	})
}

// lanProxySet 保存并下发局域网代理配置。改端口/账号需要重启核心。
func (s *Server) lanProxySet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled   bool   `json:"enabled"`
		SocksPort int    `json:"socks_port"`
		HTTPPort  int    `json:"http_port"`
		Username  string `json:"username"`
		Password  string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", "请求体不合法")
		return
	}

	set := s.store.Settings()
	prev := set
	if in.SocksPort == 0 {
		in.SocksPort = set.LanSocksPort
	}
	if in.HTTPPort == 0 {
		in.HTTPPort = set.LanHTTPPort
	}
	// 用户名/密码直接采用前端传入值，不再用历史值隐式填充：
	// 两者都留空 = 不鉴权（开放代理）；都填写 = 鉴权；只填一个 = 报错。

	if in.Enabled {
		for _, p := range []int{in.SocksPort, in.HTTPPort} {
			if p < 1024 || p > 65535 {
				writeErr(w, 400, "bad_port", "端口需在 1024-65535 之间")
				return
			}
			switch p {
			case 9090, 9091, 7890, 12345, 5354, 53:
				writeErr(w, 400, "port_conflict", fmt.Sprintf("端口 %d 已被面板或核心占用", p))
				return
			}
		}
		if in.SocksPort == in.HTTPPort {
			writeErr(w, 400, "port_conflict", "SOCKS5 与 HTTP 端口不能相同")
			return
		}
		switch {
		case in.Username == "" && in.Password == "":
			// 开放代理（无鉴权）
		case in.Username != "" && in.Password != "":
			if len(in.Password) < 8 {
				writeErr(w, 400, "weak_password", "密码至少 8 位")
				return
			}
		default:
			writeErr(w, 400, "bad_creds", "用户名和密码必须同时填写，或同时留空（留空=不鉴权）")
			return
		}
	}

	set.LanProxy = in.Enabled
	set.LanSocksPort = in.SocksPort
	set.LanHTTPPort = in.HTTPPort
	// 关闭开关时前端可能提交空账号（例如页面刚加载）：此时保留原凭据，
	// 避免「关一下再开」就把密码清空，还得重新填一遍。
	if in.Enabled || in.Username != "" || in.Password != "" {
		set.LanUser = in.Username
		set.LanPass = in.Password
	}

	// 先渲染校验，成功后再落盘：否则会出现「接口返回 400 但设置已生效」，
	// 下次任意配置渲染都会带上这份未真正下发的局域网代理参数。
	tpl, err := s.store.Template()
	if err != nil {
		writeErr(w, 500, "template_failed", err.Error())
		return
	}
	raw, err := config.Render(tpl, s.store.AllNodes(), config.RenderOpts{
		DataDir:  s.store.Dir(),
		LanProxy: set.LanProxy, SocksPort: set.LanSocksPort, HTTPPort: set.LanHTTPPort,
		User: set.LanUser, Pass: set.LanPass,
		TunEnabled:  set.TunEnabled,
		RoutingMode: set.RoutingMode,
	})
	if err != nil {
		writeErr(w, 400, "render_failed", err.Error())
		return
	}
	if err := s.store.SaveSettings(set); err != nil {
		writeErr(w, 500, "save_failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	if err := s.app.Apply(ctx, raw, "局域网代理设置变更"); err != nil {
		// 下发失败时恢复设置，保持 settings.json 与实际生效配置一致
		_ = s.store.SaveSettings(prev)
		s.failSafeNetwork()
		writeErr(w, 400, "apply_failed", err.Error())
		return
	}

	host, _, _ := net.SplitHostPort(r.Host)
	if host == "" {
		host = "127.0.0.1"
	}
	socksURL, httpURL := lanProxyURLs(host, set.LanSocksPort, set.LanHTTPPort, set.LanUser, set.LanPass)
	writeJSON(w, 200, map[string]any{
		"ok":        true,
		"enabled":   set.LanProxy,
		"socks_url": socksURL,
		"http_url":  httpURL,
		"username":  set.LanUser,
		"password":  set.LanPass,
		"open":      set.LanProxy && set.LanUser == "" && set.LanPass == "",
	})
}

// lanProxyURLs 生成局域网代理的连接串；用户名/密码都为空时生成无鉴权的开放代理地址。
func lanProxyURLs(host string, socksPort, httpPort int, user, pass string) (string, string) {
	auth := ""
	if user != "" && pass != "" {
		auth = user + ":" + pass + "@"
	}
	return fmt.Sprintf("socks5://%s%s:%d", auth, host, socksPort),
		fmt.Sprintf("http://%s%s:%d", auth, host, httpPort)
}

/* ------------------------------------------------------------ tun */

// TunSupported 供启动降级判断：面板进程是否具备创建 TUN 的权限。
func TunSupported() bool { return tunSupported() }

// tunRunning 判断当前下发的配置里是否真的带 TUN 入站。
func (s *Server) tunRunning() bool {
	raw, err := s.store.Config()
	if err != nil {
		return false
	}
	var doc struct {
		Inbounds []struct {
			Type string `json:"type"`
			Tag  string `json:"tag"`
		} `json:"inbounds"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	for _, in := range doc.Inbounds {
		if in.Type == "tun" && in.Tag == "tun-in" {
			return true
		}
	}
	return false
}

func (s *Server) tunGet(w http.ResponseWriter, r *http.Request) {
	set := s.store.Settings()
	writeJSON(w, 200, map[string]any{
		"enabled":   set.TunEnabled,
		"running":   s.tunRunning() && s.sup.Running(),
		"supported": tunSupported(),
		"platform":  runtime.GOOS,
		"note":      "TUN 通过虚拟网卡接管整机流量（含不走系统代理的程序）；开关会重启核心",
	})
}

// tunSet 开/关 TUN 入站。TUN 在配置里，所以开/关都走渲染+Apply（重启核心）。
// 管理员校验在保存之前做：面板进程必须提权，其子进程 sing-box 才能建虚拟网卡。
func (s *Server) tunSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", "请求体不合法")
		return
	}

	if in.Enabled && !tunSupported() {
		writeErr(w, 400, "unsupported", tunUnsupportedReason())
		return
	}

	set := s.store.Settings()
	prev := set
	set.TunEnabled = in.Enabled

	tpl, err := s.store.Template()
	if err != nil {
		writeErr(w, 500, "template_failed", err.Error())
		return
	}
	raw, err := config.Render(tpl, s.store.AllNodes(), config.RenderOpts{
		DataDir:  s.store.Dir(),
		LanProxy: set.LanProxy, SocksPort: set.LanSocksPort, HTTPPort: set.LanHTTPPort,
		User: set.LanUser, Pass: set.LanPass,
		TunEnabled:  set.TunEnabled,
		RoutingMode: set.RoutingMode,
	})
	if err != nil {
		writeErr(w, 400, "render_failed", err.Error())
		return
	}
	if err := s.store.SaveSettings(set); err != nil {
		writeErr(w, 500, "save_failed", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	if err := s.app.Apply(ctx, raw, "TUN 模式 "+map[bool]string{true: "开启", false: "关闭"}[in.Enabled]); err != nil {
		_ = s.store.SaveSettings(prev)
		s.failSafeNetwork()
		writeErr(w, 400, "apply_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "enabled": in.Enabled, "running": s.tunRunning()})
}

/* ------------------------------------------------------------ ws */

func (s *Server) wsConnections(w http.ResponseWriter, r *http.Request) {
	if err := s.clash.ProxyConnections(w, r); err != nil {
		writeErr(w, 502, "clash_unreachable", err.Error())
	}
}

func (s *Server) wsTraffic(w http.ResponseWriter, r *http.Request) {
	if err := s.clash.ProxyTraffic(w, r); err != nil {
		writeErr(w, 502, "clash_unreachable", err.Error())
	}
}

/* ------------------------------------------------------------ clash rest proxy */

func (s *Server) clashProxy(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/clash")
	allowed := []string{"/proxies", "/connections", "/traffic", "/version", "/configs", "/rules"}
	ok := false
	for _, p := range allowed {
		if path == p || strings.HasPrefix(path, p+"/") {
			ok = true
			break
		}
	}
	if !ok {
		writeErr(w, 403, "forbidden", "该路径不在白名单内")
		return
	}
	target := "http://127.0.0.1:9091" + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		writeErr(w, 500, "proxy_failed", err.Error())
		return
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		writeErr(w, 502, "clash_unreachable", err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<20))
}

/* ------------------------------------------------------------ helpers */

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": code, "message": msg})
}
