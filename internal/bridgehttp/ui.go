package bridgehttp

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed ui/*
var embeddedUI embed.FS

func (s *Server) ui() http.Handler {
	assets, err := fs.Sub(embeddedUI, "ui")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix("/ui/", http.FileServer(http.FS(assets)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		switch r.URL.Path {
		case "/ui/api/featureAreas":
			s.listProductFeatureAreas(w, r)
			return
		case "/ui/api/modules":
			s.listProductModules(w, r)
			return
		}
		files.ServeHTTP(w, r)
	})
}
