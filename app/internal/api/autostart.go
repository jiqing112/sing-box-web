package api

import (
	"encoding/json"
	"io"
	"net/http"
	"runtime"
)

// GET /api/v1/autostart 返回两级开机自启的状态：
//   - panel：面板进程随系统启动（Linux=systemd enable；Windows=SYSTEM 计划任务）
//   - core：面板启动时自动拉起 sing-box（核心是面板的子进程，所以「开机自启」= 面板自启 + 这里）
func (s *Server) autostartGet(w http.ResponseWriter, r *http.Request) {
	set := s.store.Settings()
	core := true
	if set.AutostartCore != nil {
		core = *set.AutostartCore
	}
	resp := map[string]any{
		"core":      core,
		"platform":  runtime.GOOS,
		"core_note": "面板启动时自动拉起 sing-box 核心；关闭后需在「服务」页手动启动",
	}
	panel, err := panelAutostartEnabled()
	if err != nil {
		resp["panel"] = false
		resp["panel_supported"] = false
		resp["panel_note"] = err.Error()
	} else {
		resp["panel"] = panel
		resp["panel_supported"] = true
		resp["panel_note"] = panelAutostartNote()
	}
	writeJSON(w, 200, resp)
}

func (s *Server) autostartSet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Panel *bool `json:"panel"`
		Core  *bool `json:"core"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&in); err != nil {
		writeErr(w, 400, "bad_request", "请求体不合法")
		return
	}
	if in.Panel != nil {
		if err := setPanelAutostart(*in.Panel, s.store.Dir()); err != nil {
			writeErr(w, 400, "autostart_failed", err.Error())
			return
		}
	}
	if in.Core != nil {
		set := s.store.Settings()
		set.AutostartCore = in.Core
		if err := s.store.SaveSettings(set); err != nil {
			writeErr(w, 500, "save_failed", err.Error())
			return
		}
	}
	s.autostartGet(w, r)
}
