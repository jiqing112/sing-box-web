package subscription

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"singbox-panel/internal/model"
)

const (
	// 必须伪装成 Clash 客户端：多数机场只对 Clash UA 返回 YAML。
	userAgent = "clash-verge/v1.7.7"
	maxBody   = 8 << 20 // 8MB 硬上限，防止恶意订阅打爆内存
)

type Service struct {
	client *http.Client
}

func New() *Service {
	return &Service{client: &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        4,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     60 * time.Second,
		},
	}}
}

type UserInfo struct {
	Upload   int64
	Download int64
	Total    int64
	Expire   int64
}

// Fetch 拉取订阅。原始响应只在调用栈上存活，解析完即释放，不落盘。
// 返回 notModified=true 表示机场回了 204（内容未变化/请求太频繁），不是错误。
func (s *Service) Fetch(ctx context.Context, rawURL string) (body []byte, info UserInfo, notModified bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, info, false, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, info, false, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil, info, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, info, false, fmt.Errorf("upstream returned %d", resp.StatusCode)
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, info, false, err
	}
	info = parseUserInfo(resp.Header.Get("subscription-userinfo"))
	return body, info, false, nil
}

// subscription-userinfo: upload=1; download=2; total=3; expire=1700000000
func parseUserInfo(h string) UserInfo {
	var info UserInfo
	for _, part := range strings.Split(h, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		v, _ := strconv.ParseInt(strings.TrimSpace(kv[1]), 10, 64)
		switch strings.TrimSpace(kv[0]) {
		case "upload":
			info.Upload = v
		case "download":
			info.Download = v
		case "total":
			info.Total = v
		case "expire":
			info.Expire = v
		}
	}
	return info
}

// Refresh 拉取 + 解析 + 保存，返回节点数与用量信息。
// notModified=true 时原样返回既有节点，调用方无需重新入库。
func (s *Service) Refresh(ctx context.Context, sub model.Subscription, prev []model.Node, reserved map[string]bool) (nodes []model.Node, info UserInfo, notModified bool, err error) {
	body, info, notModified, err := s.Fetch(ctx, sub.URL)
	if err != nil {
		return nil, info, false, err
	}
	if notModified {
		return prev, info, true, nil
	}
	nodes, err = Parse(body, sub.ID, prev, reserved)
	if err != nil {
		return nil, info, false, err
	}
	return nodes, info, false, nil
}
