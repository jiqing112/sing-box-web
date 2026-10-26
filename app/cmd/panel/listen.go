package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

// listenManager 允许运行期切换监听地址：新地址先绑定成功再关闭旧监听，
// 同端口换绑定地址时先释放旧端口，失败则回滚。
type listenManager struct {
	mu   sync.Mutex
	addr string
	ln   net.Listener
	srv  *http.Server
}

func newListenManager(addr string, handler http.Handler) (*listenManager, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	m := &listenManager{
		addr: addr,
		ln:   ln,
		srv: &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
			MaxHeaderBytes:    16 << 10,
			IdleTimeout:       60 * time.Second,
		},
	}
	m.serve(ln)
	return m, nil
}

func (m *listenManager) serve(ln net.Listener) {
	go func() {
		if err := m.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			log.Printf("listen %s: %v", ln.Addr(), err)
		}
	}()
}

func (m *listenManager) Change(addr string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if addr == m.addr {
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		m.swap(ln, addr)
		return nil
	}
	// 同一端口的通配地址与具体地址互斥，只能先关旧监听再重试。
	oldPort, oldOK := listenPort(m.addr)
	newPort, newOK := listenPort(addr)
	if !oldOK || !newOK || oldPort != newPort {
		return err
	}
	oldLn, oldAddr := m.ln, m.addr
	_ = oldLn.Close()
	ln, retryErr := net.Listen("tcp", addr)
	if retryErr != nil {
		if back, backErr := net.Listen("tcp", oldAddr); backErr == nil {
			m.ln, m.addr = back, oldAddr
			m.serve(back)
		} else {
			log.Printf("监听地址无法恢复 %s: %v", oldAddr, backErr)
		}
		return retryErr
	}
	m.swap(ln, addr)
	return nil
}

func (m *listenManager) swap(ln net.Listener, addr string) {
	old := m.ln
	m.ln, m.addr = ln, addr
	m.serve(ln)
	_ = old.Close()
	log.Printf("监听地址已切换: %s -> %s", old.Addr(), addr)
}

func (m *listenManager) Addr() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ln.Addr().String()
}

func (m *listenManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.srv.Shutdown(ctx)
}

func (m *listenManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.srv.Close()
}

func listenPort(addr string) (string, bool) {
	_, port, err := net.SplitHostPort(addr)
	return port, err == nil
}
