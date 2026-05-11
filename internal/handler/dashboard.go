package handler

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/zhangyu/windsurfapi-go/internal/dashboard"
)

// DashboardHandler serves the embedded React SPA under /dashboard.
func DashboardHandler() http.HandlerFunc {
	dist, err := fs.Sub(dashboard.Dist, "dist")
	if err != nil {
		return func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "dashboard assets unavailable", http.StatusInternalServerError)
		}
	}
	fileServer := http.FileServer(http.FS(dist))

	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/dashboard")
		if path == "" || path == "/" {
			serveDashboardIndex(w, r, dist)
			return
		}
		path = strings.TrimPrefix(path, "/")
		if _, err := fs.Stat(dist, path); err == nil {
			r2 := new(http.Request)
			*r2 = *r
			u := *r.URL
			r2.URL = &u
			r2.URL.Path = "/" + path
			fileServer.ServeHTTP(w, r2)
			return
		}
		serveDashboardIndex(w, r, dist)
	}
}

func serveDashboardIndex(w http.ResponseWriter, r *http.Request, dist fs.FS) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, dist, "index.html")
}
