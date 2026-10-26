package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	"singbox-panel/internal/api"
	"singbox-panel/internal/model"
	"singbox-panel/internal/service/clash"
	"singbox-panel/internal/service/config"
	"singbox-panel/internal/service/supervisor"
	"singbox-panel/internal/service/tproxy"
	"singbox-panel/internal/store"
	"singbox-panel/web"
)

func main() {
	dataDir := flag.String("data", defaultDataDir(), "数据目录")
	listen := flag.String("listen", "", "监听地址（留空时取 panel.conf，再退回 127.0.0.1:9090）")
	sbBin := flag.String("sing-box", defaultSingBoxBin(), "sing-box 可执行文件")
	token := flag.String("token", os.Getenv("PANEL_TOKEN"), "静态 API Token（可选）")
	password := flag.String("password", os.Getenv("PANEL_PASSWORD"), "登录密码（非空即生效，与 panel.conf 的 password 等价）")
	autostart := flag.Bool("autostart", true, "启动时自动拉起 sing-box")
	flag.Parse()

	// 命令行显式指定 -listen 时，面板里的修改只能临时生效，重启后仍以参数为准。
	listenPinned := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "listen" && *listen != "" {
			listenPinned = true
		}
	})

	// 端口/账户/密码的来源优先级：命令行参数 > panel.conf > 内置默认。
	conf := loadPanelConf()
	listenAddr := *listen
	if listenAddr == "" {
		listenAddr = conf.Listen
	}
	if listenAddr == "" {
		listenAddr = "127.0.0.1:9090"
	}

	// 约束：常驻内存有上限。软限制交给 runtime，超限时 GC 更激进而不是被 OOM killer 杀掉。
	debug.SetMemoryLimit(96 << 20)
	debug.SetGCPercent(60)

	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	settings := st.Settings()
	settings.Listen = listenAddr
	applyLoginConf(&settings, conf, *password, *dataDir)
	// 已有配置但没有版本号（从旧版本升级）时，从 1 起算
	if settings.ConfigVersion == 0 {
		if raw, err := st.Config(); err == nil && len(raw) > 0 {
			settings.ConfigVersion = 1
		}
	}
	if err := st.SaveSettings(settings); err != nil {
		log.Fatalf("save settings: %v", err)
	}
	if settings.LoginDisabled || (settings.Username == "" && settings.PasswordHash == "") {
		log.Printf("警告: 面板登录已关闭，%s 可免密码访问", listenAddr)
	}

	sup := supervisor.New(*sbBin, st.ConfigPath())
	// 上次面板被强杀时，子核心会变成孤儿并一直占着 cache.db/9091，
	// 必须在探活和自动启动之前清掉，否则下面会被「Clash API 可用」误判成核心健在。
	supervisor.SweepOrphans(*sbBin)

	// TUN 入站需要管理员令牌。非管理员启动时若直接跑带 tun 的配置，
	// wintun 建卡失败会让核心 FATAL 崩溃循环（mixed 入站也起不来），
	// 这里自动降级为无 TUN 配置并关掉开关，下次以管理员启动可再打开。
	if settings.TunEnabled && !api.TunSupported() {
		if tpl, err := st.Template(); err == nil {
			raw, err := config.Render(tpl, st.AllNodes(), config.RenderOpts{
				DataDir:  st.Dir(),
				LanProxy: settings.LanProxy, SocksPort: settings.LanSocksPort, HTTPPort: settings.LanHTTPPort,
				User: settings.LanUser, Pass: settings.LanPass,
				RoutingMode: settings.RoutingMode,
			})
			if err == nil {
				_ = st.WriteConfig(raw)
				settings.TunEnabled = false
				_ = st.SaveSettings(settings)
				log.Printf("未以管理员身份运行，TUN 已临时关闭（以管理员重启面板后可再开启）")
			}
		}
	}

	cl := clash.New("127.0.0.1:9091")
	cl.SetTTL(time.Duration(settings.DelayTTL) * time.Second)

	tp := tproxy.New(defaultNftPath(), filepath.Join(*dataDir, "tproxy.nft"), settings.TproxyPort)
	srv := api.New(st, sup, cl, *sbBin, *token, tp)

	if *autostart {
		if raw, err := st.Config(); err == nil && len(raw) > 0 {
			// 命令行显式传 -autostart 时以 flag 为准；否则设置里的开关优先于默认值。
			autostartCore := *autostart
			flagExplicit := false
			flag.Visit(func(f *flag.Flag) {
				if f.Name == "autostart" {
					flagExplicit = true
				}
			})
			if !flagExplicit && settings.AutostartCore != nil {
				autostartCore = *settings.AutostartCore
			}
			if autostartCore {
				// 核心可能由上一个面板实例拉起且仍在运行，先探活再决定是否启动，
				// 避免端口冲突导致启动失败。
				probeCtx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
				_, probeErr := cl.Version(probeCtx)
				cancel()
				if probeErr == nil {
					log.Printf("检测到运行中的 sing-box（Clash API 可用），跳过自动启动")
				} else if err := sup.Start(); err != nil {
					log.Printf("autostart sing-box: %v", err)
				}
			} else {
				log.Printf("sing-box 开机自启已在面板里关闭，跳过自动启动")
			}
		}
	}

	// 规则必须在核心确认可运行之后再下发：否则流量会被重定向到一个没有监听的端口，
	// 整机（含局域网）会直接断网。nftables 仅存在于 Linux，其它平台直接跳过。
	if settings.TproxyEnabled && tp.Supported() {
		if sup.Running() {
			// 先确认已下发的配置里真有 redirect 入站：模板被重置或手动改过配置时，
			// 规则一下发就会把流量重定向到没人监听的端口，整网断掉。
			raw, _ := st.Config()
			if err := api.VerifyTproxyInbounds(raw, settings.TproxyPort, settings.TproxyDNS); err != nil {
				log.Printf("跳过恢复透明代理：%v（已撤销残留规则）", err)
				_ = tp.Disable()
				settings.TproxyEnabled = false
				_ = st.SaveSettings(settings)
			} else {
				cidrs := settings.LanCIDRs
				if len(cidrs) == 0 {
					cidrs = tproxy.DetectLAN()
				}
				if err := tp.Apply(cidrs, settings.TproxyLocal, settings.TproxyDNS); err != nil {
					log.Printf("恢复透明代理规则失败: %v", err)
				} else {
					log.Printf("透明代理已恢复: %v", cidrs)
				}
			}
		} else {
			log.Printf("核心未运行，跳过透明代理规则（避免断网）")
			_ = tp.Disable()
		}
	} else {
		_ = tp.Disable() // 清理上次遗留的表，避免关掉开关后仍被接管
	}

	go srv.StartScheduler(context.Background())

	lm, err := newListenManager(listenAddr, srv.Router(web.Handler()))
	if err != nil {
		log.Fatalf("listen %s: %v", listenAddr, err)
	}
	srv.SetListenChanger(lm.Change)
	srv.SetListenConf(writePanelConfListen)
	srv.SetListenPinned(listenPinned)
	log.Printf("sing-box panel listening on %s (data=%s)", listenAddr, *dataDir)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = lm.Shutdown(ctx)
	_ = sup.Stop()
	// 透明代理规则必须随面板退出一起撤掉：核心已死而规则残留时，
	// LAN 流量会被重定向到无人监听的端口，整网断网直到面板回来。
	// settings 是启动时的快照，运行期间可能已通过 API 开启 tproxy，故无条件清理。
	if tp.Supported() {
		if err := tp.Disable(); err != nil {
			log.Printf("退出时撤销透明代理规则失败: %v", err)
		}
	}
	log.Println("bye")
}

func randomPass() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func loginHash(user, pass string) string {
	sum := sha256.Sum256([]byte(user + ":" + pass))
	return hex.EncodeToString(sum[:])
}

// applyLoginConf 把 panel.conf/启动参数里的账户密码落到 settings。
// panel.conf 显式把 username 与 password 都留空表示关闭登录；只留空 password
// 则保持现有密码不变。首次启动且没有关闭登录时，未提供密码会随机生成并写 initial-password。
// 哈希以「用户名:密码」为基，所以改用户名必须同时提供密码，否则本次修改会被忽略。
func applyLoginConf(settings *model.Settings, conf panelConf, flagPass, dataDir string) {
	pass := flagPass
	if pass == "" {
		pass = conf.Password
	}
	user := settings.Username
	if conf.Username != "" {
		user = conf.Username
	}

	// 命令行/环境变量显式给了密码时仍以密码为准，不关闭登录。
	if flagPass == "" && conf.loginDisabled() {
		if settings.Username != "" || settings.PasswordHash != "" {
			log.Printf("面板登录已关闭（panel.conf 的 username 与 password 均为空）")
		}
		settings.Username = ""
		settings.PasswordHash = ""
		settings.LoginDisabled = true
		return
	}

	if settings.PasswordHash == "" {
		// 已处于免登录状态，本次又没提供密码：保持关闭，避免突然生成随机密码。
		if settings.Username == "" && pass == "" {
			settings.LoginDisabled = true
			if conf.Username != "" {
				log.Printf("panel.conf 只填写了用户名、未填写 password，登录保持关闭")
			}
			return
		}
		if pass == "" {
			pass = randomPass()
			_ = os.WriteFile(filepath.Join(dataDir, "initial-password"), []byte(pass+"\n"), 0o600)
			log.Printf("初始密码已写入 %s", filepath.Join(dataDir, "initial-password"))
		}
		if user == "" {
			user = "admin"
		}
		settings.Username = user
		settings.PasswordHash = loginHash(user, pass)
		settings.LoginDisabled = false
		return
	}
	if pass == "" {
		// 面板内关闭了登录时，panel.conf 只填用户名并不会把它重新打开；
		// 要恢复登录必须提供密码（panel.conf 或面板设置页）。
		if conf.Username != "" && user != settings.Username {
			log.Printf("panel.conf 修改了用户名但未填 password，已忽略（改用户名需同时填写 password）")
		}
		return
	}
	// 提供密码即视为要恢复登录，即使密码与原有哈希相同也要清掉开关。
	settings.LoginDisabled = false
	if hash := loginHash(user, pass); hash != settings.PasswordHash {
		settings.Username = user
		settings.PasswordHash = hash
		log.Printf("登录账户/密码已更新（来自 panel.conf 或启动参数）")
	}
}
