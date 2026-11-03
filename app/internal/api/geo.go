package api

import (
	"context"
	"net/http"
	"time"

	"singbox-panel/internal/service/engine"
)

// geoPrefetchTimeout 是一次完整预取的超时。
// 两个文件合计约 12 MB，实测 jsDelivr 下完约 8 秒；留 3 分钟给慢线路。
const geoPrefetchTimeout = 3 * time.Minute

// PrewarmGeo 在后台准备 mihomo 的 geo 数据，不阻塞调用方。
//
// 面板启动后调用一次：这时用户通常在用 sing-box，正好把 12 MB 下完，
// 等真正切到 mihomo 时就不用再等 mihomo 自己下载（实测要 31 秒）。
func (s *Server) PrewarmGeo() {
	if s.geo == nil || s.bins[engine.Mihomo] == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), geoPrefetchTimeout)
		defer cancel()
		s.geo.Prewarm(ctx)
	}()
}

// geoStatus 返回 geo 数据就绪情况，前端用它决定要不要提示「正在准备」。
func (s *Server) geoStatus(w http.ResponseWriter, r *http.Request) {
	if s.geo == nil {
		writeErr(w, 404, "not_supported", "当前平台不支持 geo 数据管理")
		return
	}
	writeJSON(w, 200, map[string]any{"geo": s.geo.Status()})
}

// geoPrefetch 手动触发一次预取，立刻返回，进度看 GET /api/v1/geodata。
func (s *Server) geoPrefetch(w http.ResponseWriter, r *http.Request) {
	if s.geo == nil {
		writeErr(w, 404, "not_supported", "当前平台不支持 geo 数据管理")
		return
	}
	if s.geo.Ready() {
		writeJSON(w, 200, map[string]any{"ok": true, "geo": s.geo.Status(), "message": "geo 数据已就绪"})
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), geoPrefetchTimeout)
		defer cancel()
		_, _ = s.geo.Ensure(ctx)
	}()
	writeJSON(w, 200, map[string]any{"ok": true, "geo": s.geo.Status(), "message": "已开始后台下载"})
}
