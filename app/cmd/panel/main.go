package main

import (
	"context"
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
	"singbox-panel/internal/service/engine"
	"singbox-panel/internal/service/supervisor"
	"singbox-panel/internal/service/tproxy"
	"singbox-panel/internal/store"
	"singbox-panel/web"
)

func main() {
	dataDir := flag.String("data", defaultDataDir(), "数据目录")
	listen := flag.String("listen", "", "监听地址（留空时取 panel.conf，再退回 127.0.0.1:9090）")
	sbBin := flag.String("sing-box", defaultSingBoxBin(), "sing-box 可执行文件")
	mihomoBin := flag.String("mihomo", defaultMihomoBin(), "mihomo 可执行文件")
	engineFlag := flag.String("engine", "", "默认内核：sing-box 或 mihomo（留空时取 settings.json）")
	clashAddr := flag.String("clash", "", "Clash API 监听地址（留空时取 settings.json，默认 127.0.0.1:9091）")
	token := flag.String("token", os.Getenv("PANEL_TOKEN"), "静态 API Token（可选）")
	password := flag.String("password", os.Getenv("PANEL_PASSWORD"), "登录密码（非空即生效，与 panel.conf 的 password 等价）")
	autostart := flag.Bool("autostart", true, "启动时自动拉起 sing-box")
	notray := flag.Bool("notray", false, "禁用 Windows 托盘图标")
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

	// 数据目录统一成绝对路径：渲染出的 cache_file.path、mihomo 的 -d、
	// 子进程工作目录都以它为准，相对路径在不同启动方式下会指向不同位置。
	if abs, err := filepath.Abs(*dataDir); err == nil {
		*dataDir = abs
	}
	st, err := store.Open(*dataDir)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	settings := st.Settings()
	settings.Listen = listenAddr
	// 内核选择：命令行 -engine > settings.json > sing-box。
	// 切换引擎会连带换掉 config/template 的文件名，所以必须在下面读取
	// st.ConfigPath() 之前完成。
	if *engineFlag != "" {
		if !engine.Valid(*engineFlag) {
			log.Fatalf("未知内核 %q：只支持 sing-box 或 mihomo", *engineFlag)
		}
		settings.Engine = engine.Normalize(*engineFlag)
	}
	if err := st.SetEngine(settings.Engine); err != nil {
		log.Fatalf("switch engine: %v", err)
	}
	eng := engine.Normalize(settings.Engine)
	bins := map[string]string{
		engine.SingBox: *sbBin,
		engine.Mihomo:  *mihomoBin,
	}
	coreBin := bins[eng]
	if *clashAddr != "" {
		settings.ClashAddr = *clashAddr
	}
	applyLoginConf(&settings, conf, *password)
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

	sup := supervisor.New(coreBin, st.ConfigPath(), eng)
	// 上次面板被强杀时，子核心会变成孤儿并一直占着 cache.db/9091，
	// 必须在探活和自动启动之前清掉，否则下面会被「Clash API 可用」误判成核心健在。
	// 两个内核都扫一遍：切换过内核的话，另一个内核也可能留了孤儿。
	// 传数据目录是为了不误杀用户手动起的同款二进制（见 SweepOrphans 的说明）。
	for _, b := range bins {
		if b != "" {
			supervisor.SweepOrphans(b, st.Dir())
		}
	}

	// TUN 入站需要管理员令牌。非管理员启动时若直接跑带 tun 的配置，
	// wintun 建卡失败会让核心 FATAL 崩溃循环（mixed 入站也起不来），
	// 这里自动降级为无 TUN 配置并关掉开关，下次以管理员启动可再打开。
	if settings.TunEnabled && !api.TunSupported() {
		if tplRaw, err := st.TemplateRaw(); err == nil {
			opts := config.FromSettings(settings, st.Dir())
			opts.TunEnabled = false // 本次就是要把 tun 关掉再渲染
			raw, err := config.RenderBytes(tplRaw, st.AllNodes(), opts)
			if err == nil {
				_ = st.WriteConfig(raw)
				settings.TunEnabled = false
				_ = st.SaveSettings(settings)
				log.Printf("未以管理员身份运行，TUN 已临时关闭（以管理员重启面板后可再开启）")
			}
		}
	}

	cl := clash.New(settings.ClashAddr)
	cl.SetTTL(time.Duration(settings.DelayTTL) * time.Second)

	tp := tproxy.New(defaultNftPath(), filepath.Join(*dataDir, "tproxy.nft"), settings.TproxyPort)
	srv := api.New(st, sup, cl, bins, *token, tp)
	srv.StartIdleTrim()

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
					log.Printf("检测到运行中的 %s（Clash API 可用），跳过自动启动", engine.Label(eng))
				} else {
					// 本机 DNS 入站的端口（默认 53）可能被 dnsmasq 之类的系统服务占着，
					// 直接启动会让核心 FATAL 崩溃循环。先把已存配置里的端口降级掉。
					if fixed, note := config.RepairLocalDNS(raw, 0); note != "" {
						log.Printf("%s", note)
						_ = st.WriteConfig(fixed)
					}
					if err := sup.Start(); err != nil {
						log.Printf("autostart %s: %v", engine.Label(eng), err)
					}
				}
			} else {
				log.Printf("%s 开机自启已在面板里关闭，跳过自动启动", engine.Label(eng))
			}
		}
	}

	// 在后台把 mihomo 的 geo 数据（约 12 MB）备好。mihomo 缺这两个文件会自己下载，
	// 实测要 31 秒；提前下完，切内核时就只剩启动时间。放在核心启动之后，
	// 避免和启动过程抢带宽。
	srv.PrewarmGeo()

	// 规则必须在核心确认可运行之后再下发：否则流量会被重定向到一个没有监听的端口，
	// 整机（含局域网）会直接断网。nftables 仅存在于 Linux，其它平台直接跳过。
	if settings.TproxyEnabled && tp.Supported() {
		if sup.Running() {
			// 先确认已下发的配置里真有 redirect 入站：模板被重置或手动改过配置时，
			// 规则一下发就会把流量重定向到没人监听的端口，整网断掉。
			raw, _ := st.Config()
			if err := config.VerifyTproxy(raw, eng, settings.TproxyPort, settings.TproxyDNS); err != nil {
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
	log.Printf("panel listening on %s (data=%s, engine=%s, bin=%s)", listenAddr, *dataDir, eng, coreBin)

	// 系统托盘（仅 Windows；-notray / 会话 0 SYSTEM 自启时自动跳过）。
	// 必须放在监听起来之后：托盘的 tooltip/菜单要展示真实端口。
	var tray *trayHandle
	if !*notray {
		panelAuto, err := api.PanelAutostartEnabled()
		tray = startTray(trayState{
			Listen:      listenAddr,
			CoreRunning: sup.Running(),
			Autostart:   err == nil && panelAuto,
		})
		if tray != nil {
			go syncTrayState(tray, sup, st)
		}
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	trayEvents := tray.Events()
trayLoop:
	for {
		select {
		case <-stop:
			break trayLoop
		case ev := <-trayEvents:
			switch ev {
			case trayEventExit:
				break trayLoop
			case trayEventCoreToggle:
				if sup.Running() {
					if err := sup.Stop(); err != nil {
						log.Printf("托盘停止核心失败: %v", err)
					}
				} else if err := sup.Start(); err != nil {
					log.Printf("托盘启动核心失败: %v", err)
				}
			case trayEventCoreRestart:
				if err := sup.Stop(); err != nil {
					log.Printf("托盘重启核心失败: %v", err)
				}
				if err := sup.Start(); err != nil {
					log.Printf("托盘重启核心失败: %v", err)
				}
			case trayEventAutostartToggle:
				on := false
				if en, err := api.PanelAutostartEnabled(); err == nil {
					on = en
				}
				if err := api.SetPanelAutostart(!on, *dataDir); err != nil {
					log.Printf("切换开机自启失败: %v", err)
				} else {
					log.Printf("开机自启已切换为 %v（%s）", !on, api.PanelAutostartNote())
				}
			}
			if tray != nil {
				tray.Update(func(s *trayState) { s.CoreRunning = sup.Running() })
			}
		}
	}
	tray.Remove()

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

func loginHash(user, pass string) string {
	sum := sha256.Sum256([]byte(user + ":" + pass))
	return hex.EncodeToString(sum[:])
}

// applyLoginConf 把 panel.conf/启动参数里的账户密码落到 settings。
// 全新面板（无密码哈希）且没有提供密码时保持「待初始化」状态，由用户
// 在网页上完成首次账户设置，不再自动生成随机密码；提供密码则直接应用
// （自动化部署路径）。关闭登录只能用面板内开关，panel.conf 不再承担。
// 哈希以「用户名:密码」为基，所以改用户名必须同时提供密码，否则本次修改会被忽略。
func applyLoginConf(settings *model.Settings, conf panelConf, flagPass string) {
	pass := flagPass
	if pass == "" {
		pass = conf.Password
	}
	user := settings.Username
	if conf.Username != "" {
		user = conf.Username
	}

	if settings.PasswordHash == "" {
		// 全新面板：没有密码就交给网页初始化流程（needs_setup），
		// conf/参数预填的用户名保留下来作为默认值。
		if pass == "" {
			settings.Username = user
			settings.LoginDisabled = false
			return
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

// syncTrayState 把核心运行态/监听地址/自启开关同步进托盘（tooltip 与菜单勾选态）。
// sup.Running 与 store 读都是内存操作，3 秒轮询零成本；自启查询要起
// schtasks/systemctl 子进程，放宽到 30 秒一次。
func syncTrayState(t *trayHandle, sup *supervisor.Supervisor, st *store.Store) {
	n := 0
	for {
		time.Sleep(3 * time.Second)
		listen := st.Settings().Listen
		checkAuto := n%10 == 0
		n++
		t.Update(func(s *trayState) {
			s.CoreRunning = sup.Running()
			if listen != "" {
				s.Listen = listen
			}
			if checkAuto {
				if en, err := api.PanelAutostartEnabled(); err == nil {
					s.Autostart = en
				}
			}
		})
	}
}
