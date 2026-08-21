package webui

import (
	"bytes"
	"embed"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed dist
var embedded embed.FS

type Handler struct {
	api    http.Handler
	assets fs.FS
}

func New(api http.Handler) (http.Handler, error) {
	if api == nil {
		return nil, errors.New("API handler is required")
	}
	assets, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(assets, "index.html"); err != nil {
		return nil, errors.New("embedded Web UI index is unavailable")
	}
	return &Handler{api: api, assets: assets}, nil
}

func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(writer.Header())
	if request.URL.Path == "/api" || strings.HasPrefix(request.URL.Path, "/api/") ||
		request.URL.Path == "/healthz" || request.URL.Path == "/healthz/" {
		handler.api.ServeHTTP(writer, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		http.NotFound(writer, request)
		return
	}

	assetPath := strings.TrimPrefix(path.Clean("/"+request.URL.Path), "/")
	if assetPath == "." || assetPath == "" {
		assetPath = "index.html"
	}
	contents, err := fs.ReadFile(handler.assets, assetPath)
	if err == nil {
		handler.serveAsset(writer, request, assetPath, contents)
		return
	}
	if path.Ext(assetPath) != "" {
		http.NotFound(writer, request)
		return
	}
	contents, err = fs.ReadFile(handler.assets, "index.html")
	if err != nil {
		http.Error(writer, "Web UI unavailable", http.StatusInternalServerError)
		return
	}
	handler.serveAsset(writer, request, "index.html", contents)
}

func (handler *Handler) serveAsset(writer http.ResponseWriter, request *http.Request, name string, contents []byte) {
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		writer.Header().Set("Content-Type", contentType)
	}
	if strings.HasPrefix(name, "assets/") {
		writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		writer.Header().Set("Cache-Control", "no-store")
	}
	http.ServeContent(writer, request, name, time.Time{}, bytes.NewReader(contents))
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
	header.Set("Cross-Origin-Opener-Policy", "same-origin")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}
