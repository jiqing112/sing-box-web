//go:build windows

// Windows 平台实现：更新流为 zip（先落临时文件再随机访问选条目），交换用
// 改名舞步（运行中的 exe 可改名不可覆盖），重启用分离进程。

package api

import (
	"archive/zip"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

func platformArtifact() (string, bool) {
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		return "windows-amd64", true
	}
	return "", false
}

// stageUpdatePlatform 下载整包 zip（mixed 代理与直连交替多轮 + 断点续传）→
// 完整性/哈希校验 → 选择性解出 panel.exe / wintun.dll 到暂存文件。
func stageUpdatePlatform(mixedPort int, url, asset, exeDir, expect string) (string, error) {
	_ = asset
	zipPath := filepath.Join(exeDir, "panel.update.zip")
	// 已有完整 zip（上次更新中断后保留/上轮 POST 已下完）→ 直接跳过下载
	if !zipHasEOCD(zipPath) {
		if err := downloadUpdateFile(mixedPort, url, zipPath); err != nil {
			os.Remove(zipPath)
			return "", err
		}
	}
	if !zipHasEOCD(zipPath) {
		os.Remove(zipPath)
		return "", fmt.Errorf("更新包下载不完整，请稍后重试，或手动从 GitHub Releases 下载后放进面板目录")
	}
	if expect != "" {
		got, gerr := sha256File(zipPath)
		if gerr != nil {
			os.Remove(zipPath)
			return "", gerr
		}
		if got != expect {
			os.Remove(zipPath)
			return "", fmt.Errorf("更新包校验失败（sha256 不符），已丢弃。请重试或手动更新")
		}
	}
	staged, err := extractZipPanel(zipPath, exeDir)
	if err != nil {
		os.Remove(zipPath)
		return "", err
	}
	os.Remove(zipPath)
	return staged, nil
}

// downloadUpdateFile 下载整包到 dest：本机 mixed 代理 → 直连交替多轮，
// 断点续传（Range），节点断流是常态，一次下完 55MB 不现实。
func downloadUpdateFile(mixedPort int, url, dest string) error {
	var lastErr error
	for attempt, viaProxy := range []bool{true, false, true, false} {
		client := updateDownloadClient(0)
		if viaProxy {
			client = updateDownloadClient(mixedPort)
		}
		size := int64(0)
		if st, err := os.Stat(dest); err == nil {
			size = st.Size()
		}
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("User-Agent", "sing-box-web-panel")
		if size > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", size))
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		var f *os.File
		switch {
		case resp.StatusCode == 206 && size > 0:
			f, err = os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		case resp.StatusCode == 200:
			f, err = os.Create(dest) // 服务器不支持 Range：截断重下
		case resp.StatusCode == 416 && size > 0:
			resp.Body.Close()
			return nil // 断点已到文件尾，交给 EOCD/sha256 校验判断
		default:
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			continue
		}
		if err != nil {
			resp.Body.Close()
			lastErr = err
			continue
		}
		n, cerr := io.Copy(f, resp.Body)
		f.Close()
		resp.Body.Close()
		if cerr == nil {
			return nil
		}
		lastErr = cerr
		log.Printf("自动更新: 下载中断于 %.1f MB（第 %d 轮），续传重试", float64(size+n)/1048576, attempt+1)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("未知错误")
	}
	return fmt.Errorf("下载更新包失败（本机代理与直连均尝试）: %v", lastErr)
}

// zipHasEOCD zip 完整性粗检：尾部 64KB 内有 EOCD 签名（PK\x05\x06）。
// 残缺包没有 EOCD，防止拿半截文件去解压误导用户。
func zipHasEOCD(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() < 22 {
		return false
	}
	tail := int64(66000)
	if st.Size() < tail {
		tail = st.Size()
	}
	buf := make([]byte, tail)
	if _, err := f.ReadAt(buf, st.Size()-tail); err != nil {
		return false
	}
	for i := len(buf) - 4; i >= 0; i-- {
		if buf[i] == 0x50 && buf[i+1] == 0x4B && buf[i+2] == 0x05 && buf[i+3] == 0x06 {
			return true
		}
	}
	return false
}

// extractZipPanel 从整包 zip 中解出 panel.exe / wintun.dll 到 exe 目录的暂存文件。
func extractZipPanel(zipPath, exeDir string) (string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", fmt.Errorf("更新包不是有效 zip: %v", err)
	}
	defer zr.Close()

	staged := filepath.Join(exeDir, "panel.update.panel.exe")
	wintunStaged := filepath.Join(exeDir, "panel.update.wintun.dll")
	wrotePanel := false
	for _, e := range zr.File {
		// 包内带顶层目录（zip -r out.zip <pkg>/），按基名匹配
		switch path.Base(e.Name) {
		case "panel.exe":
			if err := copyZipEntry(e, staged); err != nil {
				return "", err
			}
			wrotePanel = true
		case "wintun.dll":
			_ = copyZipEntry(e, wintunStaged)
		}
	}
	if !wrotePanel {
		return "", fmt.Errorf("更新包里没有 panel.exe")
	}
	return staged, nil
}

func copyZipEntry(e *zip.File, dest string) error {
	rc, err := e.Open()
	if err != nil {
		return fmt.Errorf("打开 %s 失败: %v", e.Name, err)
	}
	defer rc.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

// swapAndRestartPanel 交换面板并调度重启：
//   - 运行中的 exe 可改名不可覆盖：panel.exe → panel.exe.old → 新文件就位
//     （失败时回滚恢复旧面板）；
//   - wintun.dll 在核心停止后覆盖（TUN 已释放）；
//   - 分离进程启动新 panel（同参数），老进程延迟退出。
func swapAndRestartPanel(staged string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.Abs(self)
	old := self + ".old"
	_ = os.Remove(old)
	if err := os.Rename(self, old); err != nil {
		return fmt.Errorf("替换面板失败（改名旧文件）: %v", err)
	}
	if err := os.Rename(staged, self); err != nil {
		_ = os.Rename(old, self) // 回滚恢复旧面板
		return fmt.Errorf("替换面板失败（放入新文件）: %v", err)
	}
	// wintun.dll 顺带更新（核心已停，无占用）
	wt := filepath.Join(filepath.Dir(staged), "panel.update.wintun.dll")
	if _, err := os.Stat(wt); err == nil {
		_ = os.Rename(wt, filepath.Join(filepath.Dir(self), "wintun.dll"))
	}

	args := os.Args[1:]
	exe := self
	go func() {
		time.Sleep(600 * time.Millisecond)
		cmd := exec.Command(exe, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			HideWindow:    true,
			CreationFlags: 0x0000_0200 | 0x0800_0000, // NEW_PROCESS_GROUP | CREATE_NO_WINDOW
		}
		if err := cmd.Start(); err != nil {
			log.Printf("自动更新: 新面板进程启动失败（%v），可手动运行 panel.exe", err)
			return
		}
		log.Printf("自动更新: 新面板已启动")
		// 立即退出让出监听端口：新面板启动很快，老进程晚退 300ms 可能会让
		// 新面板 bind 撞上端口占用直接退出
		os.Exit(0)
	}()
	return nil
}
