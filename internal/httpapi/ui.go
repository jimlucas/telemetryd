package httpapi

import (
	"embed"
	"fmt"
	"mime"
	"net"
	"net/http"
	"path"
	"strings"
)

//go:embed ui/*
var dashboardAssets embed.FS

func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/ui/", http.StatusTemporaryRedirect)
}

func (s *Server) uiRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/", http.StatusTemporaryRedirect)
}

func (s *Server) uiIndex(w http.ResponseWriter, r *http.Request) {
	s.serveDashboardAsset(w, r, "index.html")
}

func (s *Server) uiAsset(w http.ResponseWriter, r *http.Request) {
	asset := strings.TrimSpace(r.PathValue("asset"))
	if asset == "" {
		s.uiIndex(w, r)
		return
	}
	cleaned := path.Clean("/" + asset)
	if strings.Contains(cleaned, "..") || cleaned == "/" {
		http.NotFound(w, r)
		return
	}
	s.serveDashboardAsset(w, r, strings.TrimPrefix(cleaned, "/"))
}

func (s *Server) favicon(w http.ResponseWriter, r *http.Request) {
	s.serveDashboardAsset(w, r, "favicon.svg")
}

func (s *Server) serveDashboardAsset(w http.ResponseWriter, r *http.Request, asset string) {
	contents, err := dashboardAssets.ReadFile("ui/" + asset)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	setDashboardSecurityHeaders(w)
	contentType := mime.TypeByExtension(path.Ext(asset))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	if asset == "index.html" {
		w.Header().Set("Cache-Control", "no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(contents)
}

func setDashboardSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
}

func authExemptPath(value string) bool {
	return value == "/" || value == "/ui" || value == "/favicon.svg" ||
		value == "/healthz" || value == "/readyz" || strings.HasPrefix(value, "/ui/")
}

func dashboardURL(address net.Addr) string {
	if address == nil {
		return "http://127.0.0.1:8080/ui/"
	}
	host, port, err := net.SplitHostPort(address.String())
	if err != nil {
		return fmt.Sprintf("http://%s/ui/", address.String())
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/ui/"
}
