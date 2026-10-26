package supervisor

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"singbox-panel/internal/model"
)

var ErrRunning = errors.New("sing-box already running")

// urltest 测速高峰每秒能产生十几行日志，50 行会把 FATAL 现场瞬间冲掉，
// 环形缓冲放宽到 400 行（每行 ~100B，内存开销可忽略）。
const logCapacity = 400

type Ring struct {
	mu   sync.Mutex
	buf  []model.LogLine
	head int
	size int
}

func NewRing(n int) *Ring { return &Ring{buf: make([]model.LogLine, n)} }

func (r *Ring) Push(v model.LogLine) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.head] = v
	r.head = (r.head + 1) % len(r.buf)
	if r.size < len(r.buf) {
		r.size++
	}
}

func (r *Ring) Snapshot() []model.LogLine {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]model.LogLine, 0, r.size)
	start := (r.head - r.size + len(r.buf)) % len(r.buf)
	for i := 0; i < r.size; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}

type Supervisor struct {
	mu       sync.Mutex
	bin      string
	conf     string
	cmd      *exec.Cmd
	started  time.Time
	lastErr  string
	logs     *Ring
	onExit   func(error)
	onGiveUp func() // 连续崩溃超过阈值时回调，用于撤销透明代理规则

	stopping bool
	crashes  []time.Time
}

// 崩溃自动重启：10 分钟内最多 3 次，超过就放弃并通知上层降级
const (
	crashWindow  = 10 * time.Minute
	crashLimit   = 3
	restartDelay = 2 * time.Second
)

func New(bin, conf string) *Supervisor {
	return &Supervisor{bin: bin, conf: conf, logs: NewRing(logCapacity)}
}

// ResetCrashes 在用户手动启动时清零，避免历史崩溃影响后续自动重启策略。
func (s *Supervisor) ResetCrashes() {
	s.mu.Lock()
	s.crashes = nil
	s.mu.Unlock()
}

func (s *Supervisor) SetOnExit(fn func(error)) { s.onExit = fn }
func (s *Supervisor) SetOnGiveUp(fn func())   { s.onGiveUp = fn }

func (s *Supervisor) SetConf(path string) {
	s.mu.Lock()
	s.conf = path
	s.mu.Unlock()
}

func (s *Supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmd != nil
}

func (s *Supervisor) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil {
		return ErrRunning
	}
	cmd := exec.Command(s.bin, "run", "-c", s.conf)
	// sing-box 的 cache_file 在配置里是相对路径（如 data/cache.db），
	// 解析基准是子进程的工作目录。这里把工作目录显式设为 data 目录的父级，
	// 这样无论从哪个目录启动面板，cache.db 都能正确落到 <data>/cache.db，
	// 避免「找不到 cache.db → 核心崩溃循环 → 撤销透明代理」的问题。
	if dir := filepath.Dir(filepath.Dir(s.conf)); dir != "" {
		cmd.Dir = dir
	}
	setupProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		s.lastErr = err.Error()
		return fmt.Errorf("start sing-box: %w", err)
	}
	s.cmd = cmd
	s.started = time.Now()
	s.lastErr = ""
	s.stopping = false

	go s.pump(stdout)
	go func(c *exec.Cmd) {
		err := c.Wait()
		s.mu.Lock()
		if s.cmd == c {
			s.cmd = nil
		}
		if err != nil {
			s.lastErr = err.Error()
		}
		// stopping 必须在锁内读：Stop() 会在另一个 goroutine 里写它
		stopping := s.stopping
		s.mu.Unlock()
		s.logs.Push(model.LogLine{At: time.Now(), Raw: "[panel] sing-box exited: " + errText(err)})
		if s.onExit != nil {
			s.onExit(err)
		}
		if !stopping {
			s.handleCrash()
		}
	}(cmd)
	return nil
}

func errText(err error) string {
	if err == nil {
		return "clean"
	}
	return err.Error()
}

// sing-box 的日志带 ANSI 颜色转义，直接渲染会看到 [38;5;31m 之类的噪声
var ansiRe = regexp.MustCompile("\\x1b\\[[0-9;]*[a-zA-Z]")

func (s *Supervisor) pump(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 32<<10)
	for sc.Scan() {
		s.logs.Push(model.LogLine{At: time.Now(), Raw: ansiRe.ReplaceAllString(sc.Text(), "")})
	}
}

// handleCrash 在非主动停止的情况下触发：限流重启，超限则回调 onGiveUp。
func (s *Supervisor) handleCrash() {
	s.mu.Lock()
	now := time.Now()
	kept := s.crashes[:0]
	for _, t := range s.crashes {
		if now.Sub(t) < crashWindow {
			kept = append(kept, t)
		}
	}
	s.crashes = append(kept, now)
	n := len(s.crashes)
	s.mu.Unlock()

	if n > crashLimit {
		s.logs.Push(model.LogLine{At: time.Now(), Raw: "[panel] 10 分钟内崩溃超过 3 次，停止自动重启"})
		if s.onGiveUp != nil {
			s.onGiveUp()
		}
		return
	}

	s.logs.Push(model.LogLine{At: time.Now(), Raw: "[panel] 2 秒后自动重启核心"})
	go func() {
		time.Sleep(restartDelay)
		s.mu.Lock()
		stopping := s.stopping
		s.mu.Unlock()
		if !stopping {
			_ = s.Start()
		}
	}()
}

func (s *Supervisor) Stop() error {
	s.mu.Lock()
	s.stopping = true
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// 杀整个进程组/树，避免子进程残留
	_ = killProcessTree(cmd.Process.Pid, false)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !s.Running() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = killProcessTree(cmd.Process.Pid, true)
	// 强杀后不能立刻拉起新核心：Windows 上 cache.db 的文件锁要等进程真正
	// 消失才释放，否则新核心会卡在 cache-file 初始化直到超时崩溃。
	deadline2 := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline2) {
		if !processAlive(cmd.Process.Pid) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func (s *Supervisor) Restart() error {
	_ = s.Stop()
	time.Sleep(300 * time.Millisecond)
	return s.Start()
}

func (s *Supervisor) Status() (bool, int, time.Time, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd == nil || s.cmd.Process == nil {
		return false, 0, time.Time{}, s.lastErr
	}
	return true, s.cmd.Process.Pid, s.started, s.lastErr
}

func (s *Supervisor) Logs() []model.LogLine { return s.logs.Snapshot() }
