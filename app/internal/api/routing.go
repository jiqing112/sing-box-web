package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"singbox-panel/internal/service/config"
)

var routingModeNames = map[string]string{
	"rule":   "规则分流",
	"global": "全局代理",
	"direct": "全局直连",
}

func routingModeName(mode string) string {
	if name, ok := routingModeNames[mode]; ok {
		return name
	}
	return routingModeNames["rule"]
}

func (s *Server) routingGet(w http.ResponseWriter, r *http.Request) {
	mode := s.store.Settings().RoutingMode
	if _, ok := routingModeNames[mode]; !ok {
		mode = "rule"
	}
	writeJSON(w, 200, map[string]any{
		"mode": mode,
		"name": routingModeName(mode),
		"note": "规则分流：国内域名/IP 直连；全局代理：包括国内流量在内全部走节点；全局直连：全部不经过节点",
	})
}

func (s *Server) routingSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", "请求体不合法")
		return
	}
	if _, ok := routingModeNames[in.Mode]; !ok {
		writeErr(w, 400, "bad_mode", "路由模式只能是 rule / global / direct")
		return
	}

	set := s.store.Settings()
	prev := set
	if set.RoutingMode == in.Mode {
		writeJSON(w, 200, map[string]any{"ok": true, "mode": in.Mode, "name": routingModeName(in.Mode)})
		return
	}
	set.RoutingMode = in.Mode

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
	if err := s.app.Apply(ctx, raw, "路由模式切换为"+routingModeName(in.Mode)); err != nil {
		_ = s.store.SaveSettings(prev)
		s.failSafeNetwork()
		writeErr(w, 400, "apply_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "mode": set.RoutingMode, "name": routingModeName(set.RoutingMode)})
}
