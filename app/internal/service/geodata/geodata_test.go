package geodata

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

// 下载成功后文件必须落盘，状态转 ready，missing 清空。
func TestEnsureDownloads(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write(make([]byte, 2048))
	}))
	defer srv.Close()

	dir := t.TempDir()
	files := []File{
		{Name: "GeoSite.dat", URL: srv.URL + "/geosite.dat", MinSize: 1024},
		{Name: "geoip.metadb", URL: srv.URL + "/geoip.dat", MinSize: 1024},
	}
	m := newForTest(dir, files)

	st, err := m.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if st.State != StateReady {
		t.Fatalf("state = %s, want ready", st.State)
	}
	if len(st.Missing) != 0 {
		t.Fatalf("missing = %v, want 空", st.Missing)
	}
	for _, f := range files {
		info, err := os.Stat(filepath.Join(dir, f.Name))
		if err != nil {
			t.Fatalf("%s 没落盘: %v", f.Name, err)
		}
		if info.Size() != 2048 {
			t.Fatalf("%s size = %d, want 2048", f.Name, info.Size())
		}
	}
	if hits != 2 {
		t.Fatalf("请求数 = %d, want 2", hits)
	}

	// 第二次必须完全不联网：这是「切内核秒开」的核心保证
	before := atomic.LoadInt32(&hits)
	if _, err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("第二次 Ensure: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != before {
		t.Fatalf("已有文件仍发起了 %d 次请求", got-before)
	}
}

// 并发调用只能触发一次下载，不能把 12 MB 拉多遍。
func TestEnsureConcurrentSingleFlight(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(120 * time.Millisecond) // 拉长窗口，放大并发问题
		_, _ = w.Write(make([]byte, 4096))
	}))
	defer srv.Close()

	dir := t.TempDir()
	m := newForTest(dir, []File{{Name: "GeoSite.dat", URL: srv.URL + "/geosite.dat", MinSize: 1024}})

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Ensure(context.Background()); err != nil {
				t.Errorf("Ensure: %v", err)
			}
		}()
	}
	wg.Wait()
	if hits != 1 {
		t.Fatalf("并发 6 次 Ensure 发了 %d 个请求，want 1", hits)
	}
	if !m.Ready() {
		t.Fatal("并发结束后应处于 ready")
	}
}

// 下载失败要落到 failed 状态并带错误信息，不能静默成功。
func TestEnsureFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	dir := t.TempDir()
	m := newForTest(dir, []File{{Name: "GeoSite.dat", URL: srv.URL + "/geosite.dat", MinSize: 1024}})

	st, err := m.Ensure(context.Background())
	if err == nil {
		t.Fatal("HTTP 403 应返回错误")
	}
	if st.State != StateFailed {
		t.Fatalf("state = %s, want failed", st.State)
	}
	if st.Error == "" {
		t.Fatal("failed 状态应带错误信息")
	}
	if _, err := os.Stat(filepath.Join(dir, "GeoSite.dat")); err == nil {
		t.Fatal("失败时不应留下目标文件")
	}
	if _, err := os.Stat(filepath.Join(dir, "GeoSite.dat.tmp")); err == nil {
		t.Fatal("失败时应清理临时文件")
	}
}

// 响应体比阈值小说明是错误页/被劫持，必须当作失败，不能写进工作目录。
func TestEnsureRejectsTinyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("blocked"))
	}))
	defer srv.Close()

	m := newForTest(t.TempDir(), []File{{Name: "GeoSite.dat", URL: srv.URL + "/x", MinSize: 1 << 20}})
	if _, err := m.Ensure(context.Background()); err == nil {
		t.Fatal("过小的响应应报错")
	}
}

// 文件被删后状态要回退，否则前端一直显示「已就绪」。
func TestStatusFallsBackWhenFileRemoved(t *testing.T) {
	dir := t.TempDir()
	m := newForTest(dir, []File{{Name: "GeoSite.dat", URL: "http://127.0.0.1:1/x", MinSize: 4}})
	if err := os.WriteFile(filepath.Join(dir, "GeoSite.dat"), make([]byte, 16), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.State != StateReady {
		t.Fatalf("state = %s, want ready", st.State)
	}
	if err := os.Remove(filepath.Join(dir, "GeoSite.dat")); err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if st.State != StateIdle {
		t.Fatalf("删文件后 state = %s, want idle", st.State)
	}
	if len(st.Missing) != 1 {
		t.Fatalf("missing = %v, want 1 项", st.Missing)
	}
}

// jsDelivr 主源要能自动补一个 gh-proxy 备用源；自定义 URL 不应被篡改。
func TestMirrorsOf(t *testing.T) {
	got := mirrorsOf(File{URL: jsdelivr + "geosite.dat"})
	if len(got) != 2 {
		t.Fatalf("mirrors = %v, want 2 个", got)
	}
	if got[0] != jsdelivr+"geosite.dat" {
		t.Fatalf("主源 = %s", got[0])
	}
	if got[1] != ghproxy+"geosite.dat" {
		t.Fatalf("备用源 = %s", got[1])
	}

	custom := []string{"https://example.com/x.dat"}
	if got := mirrorsOf(File{URL: custom[0]}); len(got) != 1 || got[0] != custom[0] {
		t.Fatalf("自定义 URL 不应追加镜像: %v", got)
	}
}

func TestDefaultFilesCoverMihomoNeeds(t *testing.T) {
	got := map[string]bool{}
	for _, f := range DefaultFiles() {
		got[f.Name] = true
		if f.MinSize < 1<<20 {
			t.Fatalf("%s 的 MinSize 太小，挡不住错误页", f.Name)
		}
	}
	// 实测 mihomo 落盘的就是这两个，country.mmdb 不会单独生成
	for _, want := range []string{"GeoSite.dat", "geoip.metadb"} {
		if !got[want] {
			t.Fatalf("缺 %s", want)
		}
	}
}

// 两个文件必须并行下载：串行会让总耗时翻倍（慢线路下 12 MB 要几分钟）。
func TestEnsureParallel(t *testing.T) {
	var live int32
	var maxLive int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&live, 1)
		for {
			old := atomic.LoadInt32(&maxLive)
			if n <= old || atomic.CompareAndSwapInt32(&maxLive, old, n) {
				break
			}
		}
		defer atomic.AddInt32(&live, -1)
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write(make([]byte, 4096))
	}))
	defer srv.Close()

	dir := t.TempDir()
	m := newForTest(dir, []File{
		{Name: "GeoSite.dat", URL: srv.URL + "/a", MinSize: 1024},
		{Name: "geoip.metadb", URL: srv.URL + "/b", MinSize: 1024},
	})
	if _, err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := atomic.LoadInt32(&maxLive); got < 2 {
		t.Fatalf("并发峰值 = %d，说明是串行下载", got)
	}
}

// 主源慢到无法接受时要自动换到备用源，而不是干等。
func TestSlowMirrorFallsBack(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		buf := make([]byte, 256)
		for i := 0; i < 200; i++ {
			if _, err := w.Write(buf); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(60 * time.Millisecond) // 约 4 KB/s
		}
	}))
	defer slow.Close()

	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 8192))
	}))
	defer fast.Close()

	dir := t.TempDir()
	m := newForTest(dir, []File{
		{Name: "GeoSite.dat", URL: slow.URL + "/a " + fast.URL + "/a", MinSize: 1024},
	})
	// 缩短判定窗口，否则测试要等 8 秒
	m.slowWindow = 400 * time.Millisecond
	m.slowSpeed = 20 << 10

	start := time.Now()
	st, err := m.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if st.State != StateReady {
		t.Fatalf("state = %s, want ready", st.State)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("换源耗时 %v，说明没有触发慢速判定", el)
	}
	info, err := os.Stat(filepath.Join(dir, "GeoSite.dat"))
	if err != nil || info.Size() != 8192 {
		t.Fatalf("落盘的是慢源内容（size=%v, err=%v），应来自快源", info, err)
	}
}

// 慢速放弃时要把已计字节退回，否则进度条会虚高。
func TestSlowMirrorRestoresCounter(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 512)
		for i := 0; i < 100; i++ {
			if _, err := w.Write(buf); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(40 * time.Millisecond)
		}
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 2048))
	}))
	defer fast.Close()

	m := newForTest(t.TempDir(), []File{
		{Name: "GeoSite.dat", URL: slow.URL + "/a " + fast.URL + "/a", MinSize: 1024},
	})
	m.slowWindow = 300 * time.Millisecond
	m.slowSpeed = 20 << 10

	if _, err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := m.Status().Bytes; got != 2048 {
		t.Fatalf("累计字节 = %d，want 2048（慢源的字节没有退回）", got)
	}
}

// 内置副本以 gzip 打包：解压后的内容必须逐字节一致，
// 阈值也只能拿解压后的大小来比（压缩率会让任何阈值判断失真）。
func TestExtractFromGzip(t *testing.T) {
	payload := bytes.Repeat([]byte("geosite-bytes-"), 8192) // 约 114 KB
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if buf.Len() >= len(payload) {
		t.Fatalf("测试数据没被压缩：%d >= %d", buf.Len(), len(payload))
	}

	fsys := fstest.MapFS{"assets/GeoSite.dat.gz": {Data: buf.Bytes()}}
	dst := filepath.Join(t.TempDir(), "GeoSite.dat")
	f := File{Name: "GeoSite.dat", MinSize: int64(len(payload)) - 1}

	n, err := extractFrom(fsys, f, dst)
	if err != nil {
		t.Fatalf("extractFrom: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("写入字节 = %d, want %d", n, len(payload))
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("读回: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("解压内容与原始不一致（%d vs %d 字节）", len(got), len(payload))
	}
	if _, err := os.Stat(dst + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("临时文件没清掉")
	}
}

// 未压缩的内置副本也要能用：老版本二进制/手工放进 assets 的原始文件。
func TestExtractFromRaw(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 4096)
	fsys := fstest.MapFS{"assets/geoip.metadb": {Data: payload}}
	dst := filepath.Join(t.TempDir(), "geoip.metadb")

	n, err := extractFrom(fsys, File{Name: "geoip.metadb", MinSize: 1024}, dst)
	if err != nil {
		t.Fatalf("extractFrom: %v", err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("写入字节 = %d, want %d", n, len(payload))
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, payload) {
		t.Fatalf("内容不一致")
	}
}

// 解压后仍然太小（下载到半截、或打包了错误文件）必须报错，不能把坏文件留在原地。
func TestExtractRejectsTinyAfterGunzip(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte("tiny"))
	_ = zw.Close()

	fsys := fstest.MapFS{"assets/GeoSite.dat.gz": {Data: buf.Bytes()}}
	dst := filepath.Join(t.TempDir(), "GeoSite.dat")

	if _, err := extractFrom(fsys, File{Name: "GeoSite.dat", MinSize: 1 << 20}, dst); err == nil {
		t.Fatalf("过小的内置副本应报错")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("失败时不应留下目标文件")
	}
}
