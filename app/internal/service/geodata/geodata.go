// Package geodata 负责 mihomo 的 geo 数据库（GeoSite.dat / geoip.metadb）。
//
// mihomo 在工作目录里找不到这两个文件时会自己去 geox-url 下载，实测要 31 秒
// （GeoSite 5 s + MMDB 25 s）。这段时间面板的「切换内核」一直卡着，用户会以为
// 程序挂了；离线或镜像抽风时还会直接失败，表现为 mihomo 起不来。
//
// 所以这里提前准备好，三层兜底：
//
//  1. 编译期：把文件放进 assets/ 就会 embed 进二进制，离线也能用；
//  2. 运行期：面板启动、以及切到 sing-box 时后台预取，不占用户等待时间；
//  3. 切换前：Ensure 兜底，文件已在就直接返回，实测 0 ms。
//
// 只准备 mihomo 真正落盘的两个文件。实测 country.mmdb 不会被写成独立文件
// （内容已合进 geoip.metadb），预置它没有意义。
package geodata

import (
	"compress/gzip"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 文件状态。
const (
	StateIdle        = "idle"
	StateDownloading = "downloading"
	StateReady       = "ready"
	StateFailed      = "failed"
)

//go:embed assets
var assetsFS embed.FS

// File 描述一个需要就位的 geo 数据文件。
//
// Name 是 mihomo 期望的落盘文件名（注意 GeoSite.dat 的大小写），URL 是下载源。
// 两者并不总是一致：mihomo 会把 geox-url.geoip 指向的 geoip.dat 存成
// geoip.metadb（Metadb 格式），这里跟随它的实际行为。
type File struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	MinSize int64  `json:"min_size"`
}

// mirrors 是每个文件的可选下载源，按顺序尝试。
// jsDelivr 实测约 0.9 MB/s 且境内可达；gh-proxy 作为兜底。
const (
	jsdelivr = "https://cdn.jsdelivr.net/gh/MetaCubeX/meta-rules-dat@release/"
	ghproxy  = "https://gh-proxy.com/https://github.com/MetaCubeX/meta-rules-dat/raw/release/"
)

// DefaultFiles 返回 mihomo 启动前必须具备的 geo 数据。
//
// 注意第二个文件的映射关系（实测出来的，不要想当然）：
// mihomo 的工作目录里叫 geoip.metadb，但它装的其实是 country.mmdb 的内容
// （7,651,117 字节，与 meta-rules-dat 的 country.mmdb 完全一致）。
// 如果按名字下载 geoip.dat（16.5 MB，v2ray 格式）塞进去，mihomo 会报
// `MMDB invalid, remove and download` 然后重新下 12 秒。
func DefaultFiles() []File {
	return []File{
		{Name: "GeoSite.dat", URL: jsdelivr + "geosite.dat", MinSize: 1 << 20},
		{Name: "geoip.metadb", URL: jsdelivr + "country.mmdb", MinSize: 1 << 20},
	}
}

// mirrorsOf 返回某个文件的候选下载地址。
//
// URL 里用空格分隔多个地址时按顺序使用（方便用户换成自己的镜像列表）；
// 否则默认 jsDelivr，失败再退到 gh-proxy。
func mirrorsOf(f File) []string {
	if fields := strings.Fields(f.URL); len(fields) > 1 {
		return fields
	}
	base := strings.TrimPrefix(f.URL, jsdelivr)
	if base != f.URL {
		return []string{f.URL, ghproxy + base}
	}
	return []string{f.URL}
}

// Status 是给 API / 前端看的状态快照。
type Status struct {
	State      string   `json:"state"`
	Dir        string   `json:"dir"`
	Done       int      `json:"done"`
	Total      int      `json:"total"`
	Current    string   `json:"current"`
	Bytes      int64    `json:"bytes"`
	TotalBytes int64    `json:"total_bytes"`
	Missing    []string `json:"missing"`
	Embedded   bool     `json:"embedded"`
	Error      string   `json:"error,omitempty"`
	At         time.Time `json:"at"`
}

// Manager 串行化 geo 数据的准备过程：并发调用共享同一次下载。
type Manager struct {
	mu    sync.Mutex
	dir   string
	files []File
	st    Status
	done  chan struct{} // 非 nil 表示有下载正在进行，close 表示结束
	err   error
	http  *http.Client

	// 慢速切源阈值，测试里会调小
	slowWindow time.Duration
	slowSpeed  float64

	// noEmbed 让 Manager 忽略编译期内置副本（测试用）。仓库里不带 geo 数据时
	// 它没有意义，但打包过的二进制会让「下载路径」的测试全部走内置分支而失效。
	noEmbed bool
}

// New 创建一个 Manager。dir 是 mihomo 的工作目录（engine.Home 的返回值）。
func New(dir string) *Manager { return NewWith(dir, DefaultFiles()) }

// newForTest 关掉内置副本，强制走下载路径。
func newForTest(dir string, files []File) *Manager {
	m := NewWith(dir, files)
	m.noEmbed = true
	return m
}

// NewWith 用自定义文件列表创建 Manager（测试用）。
func NewWith(dir string, files []File) *Manager {
	return &Manager{
		dir:   dir,
		files: files,
		st: Status{
			State: StateIdle,
			Dir:   dir,
			Total: len(files),
			At:    time.Now(),
		},
		http: &http.Client{
			Timeout: 5 * time.Minute,
			Transport: &http.Transport{
				Proxy:               nil, // 预取不走用户配置的代理，避免循环依赖
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     30 * time.Second,
			},
		},
		slowWindow: defaultSlowWindow,
		slowSpeed:  defaultSlowSpeed,
	}
}

// Status 返回当前状态（不触发任何工作）。
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked()
}

func (m *Manager) snapshotLocked() Status {
	st := m.st
	st.Missing = m.missingLocked()
	st.Embedded = m.hasEmbedded()
	// 状态以磁盘为准，不能只信历史：用户手动拷进文件、或反过来删掉文件，
	// 都要立刻反映出来。只有「正在下载」和「上次失败」保留原状态，
	// 否则前端会把失败原因覆盖掉。
	if st.State != StateDownloading {
		if len(st.Missing) == 0 {
			st.State = StateReady
			st.Done = st.Total
			st.Current = ""
			st.Error = ""
		} else if st.State == StateReady {
			st.State = StateIdle
			st.Done = 0
		}
	}
	return st
}

func (m *Manager) missingLocked() []string {
	var out []string
	for _, f := range m.files {
		if !fileOK(filepath.Join(m.dir, f.Name), f.MinSize) {
			out = append(out, f.Name)
		}
	}
	return out
}

// Ready 报告文件是否已全部就位（不下载）。
func (m *Manager) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.missingLocked()) == 0
}

// Ensure 保证 geo 文件就位，必要时下载。
// 并发调用会等待同一次下载，不会重复拉取 12 MB。
func (m *Manager) Ensure(ctx context.Context) (Status, error) {
	m.mu.Lock()
	if len(m.missingLocked()) == 0 {
		m.st.State = StateReady
		m.st.Done = m.st.Total
		m.st.Current = ""
		m.st.At = time.Now()
		st := m.snapshotLocked()
		m.mu.Unlock()
		return st, nil
	}
	if m.done != nil {
		// 已有下载在跑，等它结束再判断
		wait := m.done
		m.mu.Unlock()
		select {
		case <-wait:
			m.mu.Lock()
			err := m.err
			st := m.snapshotLocked()
			m.mu.Unlock()
			if err != nil {
				return st, err
			}
			return m.Ensure(ctx)
		case <-ctx.Done():
			return m.Status(), ctx.Err()
		}
	}
	done := make(chan struct{})
	m.done = done
	m.err = nil
	m.st.State = StateDownloading
	m.st.Done = 0
	m.st.Total = len(m.files)
	m.st.Bytes = 0
	m.st.TotalBytes = 0
	m.st.Error = ""
	m.st.At = time.Now()
	m.mu.Unlock()

	err := m.fetchAll(ctx)

	m.mu.Lock()
	m.err = err
	if err != nil {
		m.st.State = StateFailed
		m.st.Error = err.Error()
	} else {
		m.st.State = StateReady
		m.st.Done = m.st.Total
		m.st.Current = ""
	}
	m.st.At = time.Now()
	m.done = nil
	close(done)
	st := m.snapshotLocked()
	m.mu.Unlock()
	return st, err
}

// Prewarm 准备 geo 数据，失败只记状态不返回错误。
//
// 它是同步的：调用方负责 `go`。这样 ctx 的生命周期和实际下载一致，
// 不会出现「调用方 defer cancel 把正在进行的下载取消掉」。
func (m *Manager) Prewarm(ctx context.Context) {
	if m.Ready() {
		return
	}
	_, _ = m.Ensure(ctx)
}

func (m *Manager) fetchAll(ctx context.Context) error {
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		return fmt.Errorf("创建 mihomo 目录失败: %w", err)
	}
	// 两个文件并行下：它们是独立资源，串行会让总耗时翻倍。
	// 实测国内到 jsDelivr 单流约 1.4 MB/s，慢线路（虚拟机 NAT）只有 75 KB/s，
	// 串行下 12 MB 要近 3 分钟，并行至少砍一半。
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		errs  []string
		done  int32
		bytes int64
	)
	for i, f := range m.files {
		if fileOK(filepath.Join(m.dir, f.Name), f.MinSize) {
			atomic.AddInt32(&done, 1)
			continue
		}
		wg.Add(1)
		go func(idx int, f File) {
			defer wg.Done()
			dst := filepath.Join(m.dir, f.Name)

			// 先试内置副本（编译时打包进来的），再走网络
			if !m.noEmbed {
				if n, err := m.extractEmbedded(f, dst); err == nil && n > 0 {
					atomic.AddInt64(&bytes, n)
					m.setProgress(int(atomic.AddInt32(&done, 1)), f.Name, atomic.LoadInt64(&bytes))
					return
				}
			}
			if err := m.download(ctx, idx, f, dst, &bytes); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", f.Name, err))
				mu.Unlock()
				return
			}
			m.setProgress(int(atomic.AddInt32(&done, 1)), f.Name, atomic.LoadInt64(&bytes))
		}(i, f)
	}
	wg.Wait()
	if len(errs) > 0 {
		return errors.New("geo 数据下载失败：" + strings.Join(errs, "；"))
	}
	return nil
}

// setProgress 汇报整体进度：done 是已完成文件数，bytes 是累计字节数。
func (m *Manager) setProgress(done int, cur string, bytes int64) {
	m.mu.Lock()
	m.st.Done = done
	m.st.Current = cur
	m.st.Bytes = bytes
	m.st.At = time.Now()
	m.mu.Unlock()
}

// 慢速切源的判定窗口与阈值。
// 实测 jsDelivr 在国内直连约 1.4 MB/s，但虚拟机 NAT 后面只有 75 KB/s；
// 这种时候 gh-proxy 反而有 2 MB/s。与其干等 3 分钟，不如 8 秒后换源。
const (
	defaultSlowWindow = 8 * time.Second
	defaultSlowSpeed  = 48 << 10 // 48 KB/s
)

// download 依次尝试候选镜像，成功后原子落盘。counter 是跨文件的累计字节数。
func (m *Manager) download(ctx context.Context, idx int, f File, dst string, counter *int64) error {
	var lastErr error
	for _, url := range mirrorsOf(f) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.downloadOne(ctx, idx, url, dst, f.MinSize, counter); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

func (m *Manager) downloadOne(ctx context.Context, idx int, url, dst string, minSize int64, counter *int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "sing-box-panel/1.0 (+geodata)")
	resp, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	total := resp.ContentLength
	if total > 0 && total < minSize {
		return fmt.Errorf("响应过小（%d 字节）", total)
	}

	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	var written int64
	// 慢速检测：窗口内平均速度低于阈值就放弃当前源，换下一个镜像。
	// 放弃时把已经计入的字节数退回去，避免进度虚高。
	fail := func(reason error) error {
		out.Close()
		os.Remove(tmp)
		if written > 0 && counter != nil {
			atomic.AddInt64(counter, -written)
		}
		return reason
	}
	mark := time.Now()
	var markBytes int64
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return fail(werr)
			}
			written += int64(n)
			if counter != nil {
				atomic.AddInt64(counter, int64(n))
			}
			if el := time.Since(mark); el >= m.slowWindow {
				speed := float64(written-markBytes) / el.Seconds()
				if speed < m.slowSpeed {
					return fail(fmt.Errorf("速度过慢（%.0f KB/s），换源", speed/1024))
				}
				mark, markBytes = time.Now(), written
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fail(rerr)
		}
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if written < minSize {
		os.Remove(tmp)
		return fmt.Errorf("下载不完整（%d 字节）", written)
	}
	// rename 保证 mihomo 不会读到写了一半的文件
	return os.Rename(tmp, dst)
}

func (m *Manager) hasEmbedded() bool {
	if m.noEmbed {
		return false
	}
	for _, f := range m.files {
		if _, err := fs.Stat(assetsFS, "assets/"+f.Name); err == nil {
			return true
		}
		if _, err := fs.Stat(assetsFS, "assets/"+f.Name+".gz"); err == nil {
			return true
		}
	}
	return false
}

// openEmbedded 打开内置副本。构建脚本会先 gzip 再打包（12 MB → 4 MB），
// 这样二进制里的 geo 数据小一大截；也兼容直接放原始文件的旧做法。
func openEmbedded(fsys fs.FS, name string) (fs.File, bool, error) {
	if f, err := fsys.Open("assets/" + name + ".gz"); err == nil {
		return f, true, nil
	}
	f, err := fsys.Open("assets/" + name)
	return f, false, err
}

// extractEmbedded 把编译期打包的副本释放到工作目录。返回写入字节数。
//
// 全程流式：以前用 assetsFS.ReadFile 会先把整份文件读进堆，7.6 MB 的
// geoip.metadb 等于「堆里一份 + 写盘一份」，峰值多 7.6 MB，而且 embed 页
// 一旦被读就常驻工作集，进程 RSS 一直挂着这 12 MB。
func (m *Manager) extractEmbedded(f File, dst string) (int64, error) {
	return extractFrom(assetsFS, f, dst)
}

// extractFrom 是 extractEmbedded 的实际实现，文件系统可注入以便测试。
func extractFrom(fsys fs.FS, f File, dst string) (int64, error) {
	src, gz, err := openEmbedded(fsys, f.Name)
	if err != nil {
		return 0, err
	}
	defer src.Close()

	var r io.Reader = src
	if gz {
		zr, zerr := gzip.NewReader(src)
		if zerr != nil {
			return 0, zerr
		}
		defer zr.Close()
		r = zr
	}

	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	written, cerr := io.CopyBuffer(out, r, make([]byte, 32<<10))
	if closeErr := out.Close(); cerr == nil {
		cerr = closeErr
	}
	if cerr != nil {
		os.Remove(tmp)
		return 0, cerr
	}
	// 压缩后的大小不能拿来比阈值，只能校验解压后的字节数
	if written < f.MinSize {
		os.Remove(tmp)
		return 0, fmt.Errorf("内置副本过小（%d 字节）", written)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return written, nil
}

// fileOK 判断文件是否已就位：存在且不小于阈值。
// 阈值用来挡住上次下载失败留下的空文件。
func fileOK(path string, minSize int64) bool {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return false
	}
	return st.Size() >= minSize
}
