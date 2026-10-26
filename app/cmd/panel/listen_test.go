package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func testHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
}

func httpStatus(t *testing.T, addr string) int {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	res, err := client.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET %s: %v", addr, err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode
}

func TestListenManagerChange(t *testing.T) {
	m, err := newListenManager("127.0.0.1:0", testHandler())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	old := m.Addr()
	if code := httpStatus(t, old); code != http.StatusNoContent {
		t.Fatalf("old listener code=%d", code)
	}

	target := freeAddr(t)
	if err := m.Change(target); err != nil {
		t.Fatal(err)
	}
	if code := httpStatus(t, target); code != http.StatusNoContent {
		t.Fatalf("new listener code=%d", code)
	}
	// 关闭监听不会掐断已建立的 keep-alive 连接，这里用全新连接验证。
	client := &http.Client{
		Timeout:   time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	if _, err := client.Get("http://" + old + "/"); err == nil {
		t.Fatal("old listener still accepting connections")
	}

	if err := m.Change(target); err != nil {
		t.Fatalf("no-op change: %v", err)
	}
	if err := m.Change("bad-addr"); err == nil {
		t.Fatal("expected error for invalid address")
	}
	if code := httpStatus(t, target); code != http.StatusNoContent {
		t.Fatalf("listener lost after failed change: code=%d", code)
	}
}

func TestListenManagerSamePortHostChange(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	m, err := newListenManager(fmt.Sprintf("0.0.0.0:%d", port), testHandler())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	specific := fmt.Sprintf("127.0.0.1:%d", port)
	if err := m.Change(specific); err != nil {
		t.Fatalf("0.0.0.0 -> 127.0.0.1: %v", err)
	}
	if code := httpStatus(t, specific); code != http.StatusNoContent {
		t.Fatalf("specific listener code=%d", code)
	}
	if err := m.Change(fmt.Sprintf("0.0.0.0:%d", port)); err != nil {
		t.Fatalf("127.0.0.1 -> 0.0.0.0: %v", err)
	}
	if code := httpStatus(t, specific); code != http.StatusNoContent {
		t.Fatalf("wildcard listener code=%d", code)
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}
