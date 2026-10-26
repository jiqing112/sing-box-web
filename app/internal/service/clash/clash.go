package clash

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Client struct {
	base string
	http *http.Client
	mu   sync.Mutex
	// 延迟结果只存内存，按 TTL 过期；进程重启即清零。
	delays map[string]delayEntry
	ttl    time.Duration
}

type delayEntry struct {
	MS int   `json:"ms"`
	At int64 `json:"at"`
}

func New(controller string) *Client {
	return &Client{
		base: "http://" + controller,
		http: &http.Client{
			Timeout: 8 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        8,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     60 * time.Second,
			},
		},
		delays: map[string]delayEntry{},
		ttl:    5 * time.Minute,
	}
}

func (c *Client) SetTTL(d time.Duration) {
	c.mu.Lock()
	c.ttl = d
	c.mu.Unlock()
}

func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := c.getJSON(ctx, "/version", &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

func (c *Client) getJSON(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s", resp.Status, string(b))
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// Delay 通过 Clash API 测一个节点的延迟。结果写内存缓存并带过期时间。
func (c *Client) Delay(ctx context.Context, tag string, timeoutMS int) (int, error) {
	q := url.Values{}
	q.Set("timeout", fmt.Sprint(timeoutMS))
	q.Set("url", "https://www.gstatic.com/generate_204")

	path := "/proxies/" + url.PathEscape(tag) + "/delay?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return -1, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.storeDelay(tag, -1)
		return -1, fmt.Errorf("delay %s: %s", tag, resp.Status)
	}
	var out struct {
		Delay int `json:"delay"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return -1, err
	}
	c.storeDelay(tag, out.Delay)
	return out.Delay, nil
}

func (c *Client) storeDelay(tag string, ms int) {
	c.mu.Lock()
	c.delays[tag] = delayEntry{MS: ms, At: time.Now().Unix()}
	c.mu.Unlock()
}

// Delays 返回未过期的延迟缓存。
func (c *Client) Delays() map[string]delayEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().Unix()
	out := make(map[string]delayEntry, len(c.delays))
	for k, v := range c.delays {
		if now-v.At < int64(c.ttl.Seconds()) {
			out[k] = v
		}
	}
	return out
}

func (c *Client) DelaysJSON() map[string]any {
	src := c.Delays()
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// ResolveActive 顺着 selector → urltest → 节点 一路解析，
// 返回真正承载流量的出站名（而不是中间的策略组名）。
func (c *Client) ResolveActive(ctx context.Context, group string) string {
	name := group
	for i := 0; i < 5; i++ {
		var p struct {
			Now  string `json:"now"`
			Type string `json:"type"`
		}
		if err := c.getJSON(ctx, "/proxies/"+url.PathEscape(name), &p); err != nil {
			return ""
		}
		if p.Now == "" {
			return name
		}
		name = p.Now
	}
	return name
}

// ProxyNow 返回当前 selector 选中的节点。
func (c *Client) ProxyNow(ctx context.Context, group string) (string, error) {
	var p struct {
		Now string `json:"now"`
	}
	if err := c.getJSON(ctx, "/proxies/"+url.PathEscape(group), &p); err != nil {
		return "", err
	}
	return p.Now, nil
}

/* ------------------------------------------------------------ websocket */

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// DialWS 连接到 Clash API 的 WebSocket 端点。
func (c *Client) DialWS(ctx context.Context, path string) (*websocket.Conn, error) {
	u := "ws://" + c.base[len("http://"):] + path
	d := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		NetDialContext:   (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	}
	conn, _, err := d.DialContext(ctx, u, nil)
	return conn, err
}

// ProxyConnections 每 2 秒从 Clash REST 拉一次连接列表，裁剪后推给浏览器。
//
// 不用上游 WebSocket：sing-box 的 /connections WS 在长连接下会静默停止推送，
// 表现为「WS 已连接但永远 0 条」。REST 轮询更稳，且同样只在服务端做一次裁剪。
func (c *Client) ProxyConnections(w http.ResponseWriter, r *http.Request) error {
	down, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return err
	}
	defer down.Close()

	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }

	// 下游读循环：只用于感知浏览器断开
	go func() {
		for {
			if _, _, err := down.ReadMessage(); err != nil {
				stop()
				return
			}
		}
	}()

	push := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		raw, err := c.getRaw(ctx, "/connections")
		if err != nil {
			return true // 核心可能正在重启，下一轮再试
		}
		frame := trimConnections(raw, 200)
		_ = down.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return down.WriteMessage(websocket.TextMessage, frame) == nil
	}

	// 先推一帧，避免页面空白等待 2 秒
	if !push() {
		return nil
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return nil
		case <-ticker.C:
			if !push() {
				return nil
			}
		}
	}
}

// getRaw 取原始响应体，供连接列表轮询使用。
func (c *Client) getRaw(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clash %s: %s", path, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

func trimConnections(raw []byte, limit int) []byte {
	var in struct {
		Connections []struct {
			ID       string `json:"id"`
			Metadata struct {
				Host            string `json:"host"`
				DestinationIP   string `json:"destinationIP"`
				DestinationPort string `json:"destinationPort"`
				Network         string `json:"network"`
				Process         string `json:"process"`
			} `json:"metadata"`
			Chains      []string `json:"chains"`
			Rule        string   `json:"rule"`
			RulePayload string   `json:"rulePayload"`
			Upload      int64    `json:"upload"`
			Download    int64    `json:"download"`
			Start       string   `json:"start"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		n := len(raw)
		if n > 160 {
			n = 160
		}
		log.Printf("trimConnections: %v (len=%d head=%q)", err, len(raw), string(raw[:n]))
		return []byte(`{"connections":[]}`)
	}
	if len(in.Connections) > limit {
		in.Connections = in.Connections[:limit]
	}
	out, _ := json.Marshal(in)
	return out
}

// ProxyTraffic 转发 /traffic（每秒一帧），同样在服务端节流到 2 秒。
func (c *Client) ProxyTraffic(w http.ResponseWriter, r *http.Request) error {
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	up, err := c.DialWS(dialCtx, "/traffic")
	cancel()
	if err != nil {
		return err
	}
	defer up.Close()

	down, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return err
	}
	defer down.Close()

	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }

	go func() {
		for {
			if _, _, err := down.ReadMessage(); err != nil {
				stop()
				return
			}
		}
	}()

	var upSum, downSum int64
	var mu sync.Mutex
	go func() {
		for {
			_, msg, err := up.ReadMessage()
			if err != nil {
				stop()
				return
			}
			var t struct {
				Up   int64 `json:"up"`
				Down int64 `json:"down"`
			}
			if json.Unmarshal(msg, &t) == nil {
				mu.Lock()
				upSum += t.Up
				downSum += t.Down
				mu.Unlock()
			}
		}
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return nil
		case <-ticker.C:
			mu.Lock()
			u, d := upSum/2, downSum/2
			upSum, downSum = 0, 0
			mu.Unlock()
			frame := fmt.Sprintf(`{"up":%d,"down":%d,"ts":%d}`, u, d, time.Now().UnixMilli())
			_ = down.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := down.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
				return nil
			}
		}
	}
}
