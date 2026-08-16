package workbench

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var files embed.FS

func StaticHandler() http.Handler {
	dist, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if isAPIPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && fs.ValidPath(name) {
			if info, statErr := fs.Stat(dist, name); statErr == nil && !info.IsDir() {
				serveFile(w, r, dist, name)
				return
			}
		}
		serveFile(w, r, dist, "index.html")
	})
}

func isAPIPath(requestPath string) bool {
	return requestPath == "/api" || strings.HasPrefix(requestPath, "/api/") ||
		requestPath == "/_haowork" || strings.HasPrefix(requestPath, "/_haowork/")
}

func serveFile(w http.ResponseWriter, r *http.Request, filesystem fs.FS, name string) {
	contents, err := fs.ReadFile(filesystem, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(contents))
}
