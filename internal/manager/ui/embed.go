// Package ui 把 web/ 构建产物（输出到本目录 dist/）嵌入二进制，以 SPA 方式提供静态服务。
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist 由 web/ 的 npm run build 输出；仓库里只保留占位 index.html。
//
//go:embed dist
var dist embed.FS

// Handler 返回 SPA 静态服务：存在的文件直接返回，其余路径回落到 index.html。
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if p != "/" {
			if f, err := sub.Open(strings.TrimPrefix(p, "/")); err == nil {
				info, statErr := f.Stat()
				_ = f.Close()
				if statErr == nil && !info.IsDir() {
					// 带哈希的静态资源可长期缓存。
					if strings.HasPrefix(p, "/assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
					} else {
						w.Header().Set("Cache-Control", "no-cache")
					}
					fileServer.ServeHTTP(w, r)
					return
				}
			}
		}
		// SPA 回落。
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}
