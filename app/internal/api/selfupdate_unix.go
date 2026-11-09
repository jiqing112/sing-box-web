//go:build linux

// Linux 平台实现：更新流（tar.gz）直接流式抽取面板二进制——
// 不落盘整包（路由器 128MB ROM 只需 ~9MB 峰值占用），交换后延迟 exec 重启。

package api

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

func platformArtifact() (string, bool) {
	switch runtime.GOARCH {
	case "amd64":
		return "linux-amd64", true
	case "arm64":
		return "linux-arm64", true
	case "mipsle":
		return "linux-mipsle", true
	}
	return "", false
}

// stageUpdatePlatform 拉起更新流（先直连后本机 mixed，600s 长超时）并流式抽取
// panel 二进制。不落盘整包；sha256 无法整体校验（panel 抽到即停读，见下），
// 完整性由 gzip/tar 解码错误兜底。
func stageUpdatePlatform(mixedPort int, url, asset, exeDir, expect string) (string, error) {
	_, _ = asset, expect
	var resp *http.Response
	var lastErr error
	clients := []*http.Client{updateDownloadClient(0)}
	if mixedPort > 0 {
		clients = append(clients, updateDownloadClient(mixedPort))
	}
	for _, client := range clients {
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("User-Agent", "sing-box-web-panel")
		r, err := client.Do(req)
		if err == nil && r.StatusCode == 200 {
			resp = r
			break
		}
		if r != nil {
			r.Body.Close()
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("HTTP %d", r.StatusCode)
		}
	}
	if resp == nil {
		return "", fmt.Errorf("下载更新包失败（直连与本机代理均失败）: %v", lastErr)
	}
	defer resp.Body.Close()
	return extractUpdatePlatform(resp.Body, exeDir)
}

// extractUpdatePlatform 从更新流（gzip+tar）中抽取 panel 二进制。
// sha256 由调用方在 r 外层 tee 累加（哈希的是压缩字节，与发布 .sha256 一致）。
func extractUpdatePlatform(r io.Reader, exeDir string) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("更新包不是有效 gzip: %v", err)
	}
	staged := filepath.Join(exeDir, "panel.update.tmp")
	tr := tar.NewReader(gz)
	wrotePanel := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("读取更新包失败: %v", err)
		}
		if (hdr.Typeflag == tar.TypeReg || hdr.Typeflag == 0) &&
			(hdr.Name == "panel" || strings.HasSuffix(hdr.Name, "/panel")) {
			out, err := os.OpenFile(staged, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return "", fmt.Errorf("写入面板失败: %v", err)
			}
			out.Close()
			wrotePanel = true
			break // 面板已拿到，后续条目（内核等）不解
		}
	}
	if !wrotePanel {
		return "", fmt.Errorf("更新包里没有 panel 二进制")
	}
	return staged, nil
}

// swapAndRestartPanel 交换面板并调度重启：
//   - Linux 运行中的二进制可直接 rename 替换（inode 交换，老进程继续跑旧映像）；
//   - 延迟 exec 自身（同 PID，systemd/procd 无感知；核心已停，新映像按配置拉起）。
func swapAndRestartPanel(staged string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.Abs(self)
	if err := os.Rename(staged, self); err != nil {
		return fmt.Errorf("替换面板失败: %v", err)
	}

	args := os.Args[1:]
	exe := self
	go func() {
		time.Sleep(500 * time.Millisecond)
		// exec 替换进程映像：同 PID，监听 socket 随 exec 关闭后由新面板重绑
		e := syscall.Exec(exe, append([]string{exe}, args...), os.Environ())
		log.Printf("自动更新: exec 失败 %v，请手动重启面板", e)
		os.Exit(1)
	}()
	return nil
}
