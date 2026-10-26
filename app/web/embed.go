package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
)

// dist 是 Svelte 构建产物（或同构的静态包）。
// 前端零 Node 运行时，由同一个 Go 进程直接服务。
//
//go:embed all:dist
var dist embed.FS

// etag 由嵌入资源的实际内容算出：只要前端内容变了，ETag 自动变化。
// 以前这里是手写常量（"v108"），改前端忘记改版本号时浏览器会一直命中 304，
// 继续用旧 JS/CSS，排查起来非常费劲。
var etag = contentETag()

func contentETag() string {
	h := sha256.New()
	_ = fs.WalkDir(dist, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := dist.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s:%d\n", p, len(b))
		_, _ = h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 没有内容哈希的文件名，统一走协商缓存：
		// 否则升级后浏览器会拿旧 JS/CSS 跑一小时。
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", `"`+etag+`"`)
		if !strings.Contains(r.URL.Path, ".") && r.URL.Path != "/" {
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}
