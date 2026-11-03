package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"singbox-panel/internal/model"
	"singbox-panel/internal/service/config"
	"singbox-panel/internal/service/engine"
)

// engineGet 列出面板能驱动的内核，以及各自的二进制是否可用。
// 前端「设置 → 内核」用它渲染下拉框，并解释某个内核为什么不能选。
func (s *Server) engineGet(w http.ResponseWriter, r *http.Request) {
	cur := s.engineName()
	list := make([]any, 0, len(engine.Supported()))
	for _, name := range engine.Supported() {
		bin := s.bins[name]
		item := map[string]any{
			"name":   name,
			"label":  engine.Label(name),
			"bin":    bin,
			"active": name == cur,
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		ver, err := engine.Probe(ctx, bin, name)
		cancel()
		if err != nil {
			item["ok"] = false
			item["error"] = err.Error()
		} else {
			item["ok"] = true
			item["version"] = ver
			item["version_short"] = engine.ShortVersion(name, ver)
		}
		list = append(list, item)
	}
	writeJSON(w, 200, map[string]any{
		"engine":    cur,
		"label":     engine.Label(cur),
		"config":    engine.ConfigFile(cur),
		"available": list,
	})
}

// engineSet 在面板内切换内核：sing-box ↔ mihomo。
//
// 两个内核的配置格式完全不同，所以切换 = 换一份配置 + 换一个子进程。
// 顺序很重要：先确认目标内核能跑 → 切 store 的路径 → 渲染并完整走一遍
// check/写入/启动/探活 → 失败则把设置、路径、二进制、旧配置全部恢复。
func (s *Server) engineSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Engine string `json:"engine"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", "无法解析请求体")
		return
	}
	if !engine.Valid(in.Engine) {
		writeErr(w, 400, "bad_engine", "内核只能是 sing-box 或 mihomo")
		return
	}
	target := engine.Normalize(in.Engine)
	cur := s.engineName()
	if target == cur {
		writeJSON(w, 200, map[string]any{"ok": true, "engine": cur, "changed": false})
		return
	}

	bin := s.bins[target]
	if bin == "" {
		writeErr(w, 400, "engine_missing", "没有为 "+engine.Label(target)+" 配置可执行文件路径")
		return
	}
	pctx, pcancel := context.WithTimeout(r.Context(), 8*time.Second)
	ver, err := engine.Probe(pctx, bin, target)
	pcancel()
	if err != nil {
		writeErr(w, 400, "engine_unavailable", engine.Label(target)+" 不可用: "+err.Error())
		return
	}

	// 切到 mihomo 之前把 geo 数据备好：mihomo 发现缺文件会自己下载，实测 31 秒，
	// 这段时间切换请求一直挂着。面板先下完（通常已由 PrewarmGeo 完成），
	// 切换就只剩启动时间。下载失败也不拦着切换，交给 mihomo 自己重试。
	if engine.IsMihomo(target) && s.geo != nil {
		gctx, gcancel := context.WithTimeout(r.Context(), geoPrefetchTimeout)
		if _, gerr := s.geo.Ensure(gctx); gerr != nil {
			log.Printf("geo 数据准备失败，交给 mihomo 自行下载: %v", gerr)
		}
		gcancel()
	}

	prevSet := s.store.Settings()
	prevCfg, _ := s.store.Config()
	set := prevSet
	set.Engine = target

	// 先切 store 的引擎：ConfigPath / TemplatePath 会跟着指向新内核的文件，
	// 两个内核的配置互不覆盖，切回去还能用原来那份。
	if err := s.store.SetEngine(target); err != nil {
		writeErr(w, 500, "save_failed", err.Error())
		return
	}
	raw, err := s.renderConfig(set)
	if err != nil {
		s.restoreEngine(cur, prevSet)
		writeErr(w, 400, "render_failed", err.Error())
		return
	}
	if err := s.store.SaveSettings(set); err != nil {
		s.restoreEngine(cur, prevSet)
		writeErr(w, 500, "save_failed", err.Error())
		return
	}

	// 旧核心必须先停：sing-box 与 mihomo 会抢同一个 Clash API 端口与 mixed 端口。
	_ = s.sup.Stop()
	s.sup.SetEngine(bin, s.store.ConfigPath(), target)
	s.app.SetEngine(bin, target)

	ctx, cancel := context.WithTimeout(r.Context(), applyTimeout)
	defer cancel()
	if err := s.app.Apply(ctx, raw, "切换内核为 "+engine.Label(target)); err != nil {
		_ = s.sup.Stop()
		s.restoreEngine(cur, prevSet)
		if len(prevCfg) > 0 {
			_ = s.store.WriteConfig(prevCfg)
			_ = s.sup.Start()
		}
		s.failSafeNetwork()
		writeErr(w, 400, "apply_failed", err.Error())
		return
	}

	// 两个内核对 /proxies/{name}/delay 的测量口径不同（同一节点实测差 4~6 倍），
	// 旧内核的延迟值继续显示会让人误判线路质量，切完必须清掉。
	s.clash.ResetDelays()

	// 新内核的入站布局与旧的不同（例如 mihomo 用 redir-port 而非 redirect 入站），
	// 透明代理规则还在但目标端口没人监听时会整网断网，这里必须重新核对。
	if prevSet.TproxyEnabled && s.tp.Supported() {
		if err := config.VerifyTproxy(raw, target, prevSet.TproxyPort, prevSet.TproxyDNS); err != nil {
			_ = s.tp.Disable()
			set.TproxyEnabled = false
			_ = s.store.SaveSettings(set)
			writeJSON(w, 200, map[string]any{
				"ok": true, "engine": target, "label": engine.Label(target),
				"version": engine.ShortVersion(target, ver), "version_full": ver,
				"changed": true,
				"warning": "新内核的配置不满足透明代理接管条件，已自动关闭透明代理：" + err.Error(),
			})
			return
		}
	}

	// 现在跑的是 sing-box，正好在后台把 mihomo 的 geo 数据备好，
	// 下次切过去就不用等。异步执行，不影响这次切换的响应。
	if !engine.IsMihomo(target) {
		s.PrewarmGeo()
	}

	writeJSON(w, 200, map[string]any{
		"ok": true, "engine": target, "label": engine.Label(target),
		"version": engine.ShortVersion(target, ver), "version_full": ver, "changed": true,
	})
}

// restoreEngine 把 store / supervisor / applier 全部退回切换前的状态。
func (s *Server) restoreEngine(name string, set model.Settings) {
	_ = s.store.SetEngine(name)
	set.Engine = name
	_ = s.store.SaveSettings(set)
	bin := s.bins[name]
	s.sup.SetEngine(bin, s.store.ConfigPath(), name)
	s.app.SetEngine(bin, name)
}
