// 自动更新（全平台）：从本仓库 GitHub Releases 检查/下载/替换面板二进制并重启。
//
// 流程（平台抽取与交换见 selfupdate_windows.go / selfupdate_unix.go）：
//   - 检查：GitHub API 优先（带资产列表）；API 不可达（国内路由器常态）时跟随
//     /releases/latest 重定向解析 tag；直连 GitHub 失败时自动走本机 mixed 入站
//     重试（面板自己就是代理工具，核心节点可达 GitHub 时更新即可用）。
//   - 下载：流式 tee sha256，与发布 .sha256 比对，不符即丢弃；Windows 解 zip、
//     Linux 流式抽 tar（路由器只落盘面板本身 ~9MB，不缓存整包）。
//   - 交换与重启：Windows 改名舞步 + 分离进程；Linux rename + 延迟 exec。
//
// 安全约束：更新源固定为本仓库 Releases，URL 全部由代码按平台拼接，不接受
// 任何外部传入地址；只替换面板二进制（Windows 顺带 wintun.dll），不动内核与
// panel.conf 等用户配置。

package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const updateRepo = "jiqing112/sing-box-web"

// updateHTTPClient 检查更新用（15s 短超时）。mixedPort > 0 时经本机 mixed 入站
// 代理（面板自己就是代理工具）。
func updateHTTPClient(mixedPort int) *http.Client {
	return updateHTTPClientTimeout(mixedPort, 15*time.Second)
}

// updateDownloadClient 下载更新包用（600s 长超时：55MB 经节点下载是常态）。
func updateDownloadClient(mixedPort int) *http.Client {
	return updateHTTPClientTimeout(mixedPort, 600*time.Second)
}

func updateHTTPClientTimeout(mixedPort int, timeout time.Duration) *http.Client {
	c := &http.Client{Timeout: timeout}
	if mixedPort > 0 {
		if pu, perr := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", mixedPort)); perr == nil {
			c.Transport = &http.Transport{Proxy: http.ProxyURL(pu)}
		}
	}
	return c
}

func verTuple(s string) [3]int64 {
	t := strings.TrimPrefix(s, "v")
	parts := strings.Split(t, ".")
	var out [3]int64
	for i := 0; i < 3 && i < len(parts); i++ {
		fmt.Sscanf(parts[i], "%d", &out[i])
	}
	return out
}

func verNewer(latest, current string) bool {
	l, c := verTuple(latest), verTuple(current)
	return l[0] > c[0] || (l[0] == c[0] && l[1] > c[1]) ||
		(l[0] == c[0] && l[1] == c[1] && l[2] > c[2])
}

// updateCheckLatest 检查最新版本。直连失败时由调用方用代理客户端重试
// （updateCheckLatestWith）。测试钩子：PANEL_UPDATE_VERSION 强制指定最新版本。
func (s *Server) updateCheckLatest() (tag, asset string, err error) {
	return s.updateCheckLatestWith(&http.Client{Timeout: 15 * time.Second})
}

func (s *Server) updateCheckLatestWith(client *http.Client) (tag, asset string, err error) {
	artifact, supported := platformArtifact()
	if !supported {
		return "", "", fmt.Errorf("当前平台不支持自动更新")
	}
	if forced := os.Getenv("PANEL_UPDATE_VERSION"); forced != "" {
		// 测试钩子：强制指定"最新版本"（tag 用 forced，资产从该版本真实列表里选）
		names, aerr := s.releaseAssetNamesWith(client, "releases/tags/"+forced)
		if aerr != nil {
			return "", "", fmt.Errorf("获取 %s 资产列表失败: %v", forced, aerr)
		}
		a := pickUpdateAsset(names, artifact)
		if a == "" {
			return "", "", fmt.Errorf("版本 %s 没有平台 %s 的发行包", forced, artifact)
		}
		return forced, a, nil
	}

	// 1) API 路径
	api := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", updateRepo)
	req, _ := http.NewRequest("GET", api, nil)
	req.Header.Set("User-Agent", "sing-box-web-panel")
	req.Header.Set("Accept", "application/vnd.github+json")
	if resp, rerr := client.Do(req); rerr == nil && resp.StatusCode == 200 {
		var v struct {
			TagName string `json:"tag_name"`
			Assets  []struct {
				Name string `json:"name"`
			} `json:"assets"`
		}
		derr := json.NewDecoder(resp.Body).Decode(&v)
		resp.Body.Close()
		if derr == nil && v.TagName != "" {
			names := make([]string, len(v.Assets))
			for i, a := range v.Assets {
				names[i] = a.Name
			}
			if a := pickUpdateAsset(names, artifact); a != "" {
				return v.TagName, a, nil
			}
		}
	}

	// 2) 兜底：/releases/latest 的 Location 头就是 /tag/vX.Y.Z（无 API 配额）。
	// 复用调用方的传输通道（直连或 mixed），只改重定向策略。
	req2, _ := http.NewRequest("GET", fmt.Sprintf("https://github.com/%s/releases/latest", updateRepo), nil)
	req2.Header.Set("User-Agent", "sing-box-web-panel")
	resp2, rerr := (&http.Client{
		Transport: client.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}).Do(req2)
	if rerr != nil {
		return "", "", fmt.Errorf("检查更新失败: %v", rerr)
	}
	resp2.Body.Close()
	loc := resp2.Header.Get("Location")
	tag = loc[strings.LastIndex(loc, "/")+1:]
	if !strings.HasPrefix(tag, "v") {
		return "", "", fmt.Errorf("检查更新失败：无法解析最新版本（%v）", loc)
	}
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return tag, fmt.Sprintf("sing-box-web-%s-%s.%s", tag, artifact, ext), nil
}

// updateCheckWithFallback 检查更新：直连优先，失败走本机 mixed 入站回落
// （面板自己就是代理工具，核心节点可达 GitHub 时更新即可用）。
func (s *Server) updateCheckWithFallback(mixedPort int) (tag, asset string, err error) {
	tag, asset, err = s.updateCheckLatestWith(updateHTTPClientTimeout(0, 15*time.Second))
	if err == nil {
		return tag, asset, nil
	}
	ptag, passet, perr := s.updateCheckLatestWith(updateHTTPClient(mixedPort))
	if perr != nil {
		return "", "", fmt.Errorf("%v；走本机代理重试仍失败: %v", err, perr)
	}
	return ptag, passet, nil
}

// pickUpdateAsset 优先 -dual（x86/arm64 有）；mipsle 无 dual → with-sing-box（panel 同物）
func pickUpdateAsset(names []string, artifact string) string {
	for _, n := range names {
		if strings.Contains(n, artifact) && strings.Contains(n, "-dual") {
			return n
		}
	}
	for _, n := range names {
		if strings.Contains(n, artifact) && strings.Contains(n, "with-sing-box") {
			return n
		}
	}
	for _, n := range names {
		if strings.Contains(n, artifact) && (strings.HasSuffix(n, ".tar.gz") || strings.HasSuffix(n, ".zip")) {
			return n
		}
	}
	return ""
}

// releaseAssetNamesWith 拉某个 release 的资产名列表（tags/<tag> 端点），
// 复用调用方传入的客户端（直连或 mixed）。
func (s *Server) releaseAssetNamesWith(client *http.Client, suffix string) ([]string, error) {
	api := fmt.Sprintf("https://api.github.com/repos/%s/%s", updateRepo, suffix)
	req, _ := http.NewRequest("GET", api, nil)
	req.Header.Set("User-Agent", "sing-box-web-panel")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var v struct {
		Assets []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	names := make([]string, len(v.Assets))
	for i, a := range v.Assets {
		names[i] = a.Name
	}
	return names, nil
}

// fetchUpdateSHA 取发布 .sha256 的期望哈希（拿不到返回空 → 跳过校验）
func fetchUpdateSHA(client *http.Client, assetURL string) string {
	req, _ := http.NewRequest("GET", assetURL+".sha256", nil)
	req.Header.Set("User-Agent", "sing-box-web-panel")
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return ""
	}
	hex := strings.SplitN(strings.TrimSpace(string(body)), " ", 2)[0]
	if len(hex) == 64 {
		return strings.ToLower(hex)
	}
	return ""
}

// ------------------------------------------------------------ 状态与执行

var (
	updStateMu sync.Mutex
	updState   updateStatus
	updCacheAt time.Time
)

type updateStatus struct {
	Current   string
	Latest    string
	HasUpdate bool
	Asset     string
}

func (s *Server) updateGet(w http.ResponseWriter, r *http.Request) {
	_, supported := platformArtifact()
	updStateMu.Lock()
	st, fresh := updState, time.Since(updCacheAt) < 10*time.Minute
	updStateMu.Unlock()

	if !fresh {
		tag, asset, err := s.updateCheckWithFallback(s.store.Settings().MixedPort)
		if err == nil {
			st = updateStatus{Current: Version, Latest: tag, HasUpdate: verNewer(tag, Version), Asset: asset}
			updStateMu.Lock()
			updState = st
			updCacheAt = time.Now()
			updStateMu.Unlock()
		} else {
			st = updateStatus{Current: Version, Asset: err.Error()}
		}
	}
	writeJSON(w, 200, map[string]any{
		"current":    st.Current,
		"latest":     st.Latest,
		"has_update": st.HasUpdate,
		"supported":  supported,
		"asset":      st.Asset,
	})
}

func (s *Server) updateSet(w http.ResponseWriter, r *http.Request) {
	mixedPort := s.store.Settings().MixedPort
	if mixedPort <= 0 {
		mixedPort = 7890
	}
	tag, err := s.updatePerform(mixedPort, s.store.Dir())
	if err != nil {
		writeErr(w, 400, "update_failed", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "restarting": true, "version": tag})
}

// updatePerform 完整流程：检查（直连→mixed 回落）→ 下载+校验 → 停核心 →
// 交换 → 调度重启（先回 HTTP 响应，重启延迟执行）。
func (s *Server) updatePerform(mixedPort int, dataDir string) (string, error) {
	tag, asset, err := s.updateCheckWithFallback(mixedPort)
	if err != nil {
		return "", err
	}
	if !verNewer(tag, Version) {
		return "", fmt.Errorf("已是最新版本（%v）", tag)
	}

	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", updateRepo, tag, asset)
	// .sha256 先取（小文件）：直连拿不到走 mixed 重试
	expect := fetchUpdateSHA(updateHTTPClientTimeout(0, 15*time.Second), url)
	if expect == "" {
		expect = fetchUpdateSHA(updateHTTPClient(mixedPort), url)
	}

	// 下载+抽取可能耗时较长，核心保持在跑（用户的代理不中断）

	exeDir := filepath.Dir(mustSelf())
	staged, err := stageUpdatePlatform(mixedPort, url, asset, exeDir, expect)
	if err != nil {
		return "", err
	}

	// 停核心（释放 cache.db / TUN / 端口），紧接着交换重启
	if s.sup.Running() {
		if err := s.sup.Stop(); err != nil {
			log.Printf("自动更新: 停止核心失败（继续替换）: %v", err)
		}
	}
	if err := swapAndRestartPanel(staged); err != nil {
		return "", err
	}
	log.Printf("自动更新: 面板已替换为 %v，将在 1 秒内重启（页面稍后自动恢复）", tag)
	return tag, nil
}

func mustSelf() string {
	p, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	return p
}

// sha256File 计算文件 sha256
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
