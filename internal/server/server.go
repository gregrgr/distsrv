package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"distsrv/internal/config"
	"distsrv/internal/db"
	"distsrv/internal/storage"
)

type Server struct {
	cfg       *config.Config
	db        *db.DB
	storage   *storage.Manager
	templates *template.Template
	plist     *template.Template
	staticFS  fs.FS
}

func New(cfg *config.Config, database *db.DB, st *storage.Manager) (*Server, error) {
	s := &Server{cfg: cfg, db: database, storage: st}
	if err := s.loadTemplates(); err != nil {
		return nil, err
	}
	sub, err := fs.Sub(webFS, "web/static")
	if err != nil {
		return nil, fmt.Errorf("static fs: %w", err)
	}
	s.staticFS = sub
	return s, nil
}

func (s *Server) loadTemplates() error {
	htmlFuncs := template.FuncMap{
		"humanBytes": humanBytes,
		"unixToDate": unixToDate,
		"shortHash":  shortHash,
		"dict":       dictFunc,
		"firstChar":  firstChar,
	}
	t, err := template.New("").Funcs(htmlFuncs).ParseFS(webFS, "web/templates/*.html")
	if err != nil {
		return fmt.Errorf("parse html templates: %w", err)
	}
	s.templates = t

	p, err := template.New("").ParseFS(webFS, "web/plist/*")
	if err != nil {
		return fmt.Errorf("parse plist templates: %w", err)
	}
	s.plist = p
	return nil
}

func (s *Server) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(recoverer, requestLog)

	// Public
	r.Get("/", s.handleIndex)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	r.Route("/d/{shortID}", func(r chi.Router) {
		r.Get("/", s.handleDownloadPage)
		r.Post("/auth", s.handleDownloadAuth)
		r.Get("/qr.png", s.handleDownloadQR)
	})
	r.Get("/manifest/{vid}.plist", s.handleManifest)
	r.Get("/file/{vid}/{filename}", s.handleFileDownload)
	r.Get("/icon/{vid}.png", s.handleIcon)
	r.Get("/mobileconfig/{shortID}.mobileconfig", s.handleMobileconfig)
	r.Post("/udid-callback", s.handleUDIDCallback)
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(s.staticFS))))

	// API (Bearer token)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/server", s.handleAPIServerInfo)
		r.Group(func(r chi.Router) {
			r.Use(s.requireAPIToken)
			r.Get("/whoami", s.handleAPIWhoami)
			r.Get("/apps", s.handleAPIListApps)
			r.Post("/apps", s.handleAPICreateApp)
			r.Get("/apps/{shortID}", s.handleAPIGetApp)
			r.Post("/apps/{shortID}/upload", s.handleAPIUpload)
		})
	})

	// Admin
	r.Route("/admin", func(r chi.Router) {
		r.Get("/login", s.handleAdminLoginGet)
		r.Post("/login", s.handleAdminLoginPost)
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Post("/logout", s.handleAdminLogout)
			r.Get("/", s.handleAdminDashboard)
			r.Get("/apps/new", s.handleAdminAppNewGet)
			r.Post("/apps/new", s.handleAdminAppNewPost)
			r.Route("/apps/{id}", func(r chi.Router) {
				r.Get("/", s.handleAdminAppDetail)
				r.Post("/edit", s.handleAdminAppEdit)
				r.Post("/delete", s.handleAdminAppDelete)
				r.Post("/upload", s.handleAdminAppUpload)
				r.Post("/password", s.handleAdminAppSetPassword)
				r.Get("/udids", s.handleAdminAppUDIDs)
				r.Get("/stats", s.handleAdminAppStats)
			})
			r.Route("/versions/{id}", func(r chi.Router) {
				r.Post("/set-current", s.handleAdminVersionSetCurrent)
				r.Post("/delete", s.handleAdminVersionDelete)
			})
			r.Post("/password", s.handleAdminChangePassword)
			r.Get("/tokens", s.handleAdminTokensList)
			r.Post("/tokens", s.handleAdminTokensCreate)
			r.Post("/tokens/{id}/delete", s.handleAdminTokensDelete)

			// User management (admin-only).
			r.Group(func(r chi.Router) {
				r.Use(s.requireAdmin)
				r.Get("/users", s.handleAdminUsersList)
				r.Post("/users", s.handleAdminUsersCreate)
				r.Post("/users/{id}/toggle-admin", s.handleAdminUsersToggleAdmin)
				r.Post("/users/{id}/toggle-disabled", s.handleAdminUsersToggleDisabled)
				r.Post("/users/{id}/reset-password", s.handleAdminUsersResetPassword)
				r.Post("/users/{id}/delete", s.handleAdminUsersDelete)
			})
		})
	})

	return r
}

// Run starts the HTTP/HTTPS server(s). In dev mode it serves plain HTTP on DevAddr.
// In production it serves HTTPS on HTTPSAddr with autocert and HTTP on HTTPAddr
// for ACME challenges + 301 redirects.
func (s *Server) Run(ctx context.Context) error {
	handler := s.routes()

	if s.cfg.Server.DevMode {
		srv := &http.Server{
			Addr:        s.cfg.Server.DevAddr,
			Handler:     handler,
			ReadTimeout: time.Duration(s.cfg.Server.ReadTimeoutSeconds) * time.Second,
			// WriteTimeout intentionally 0 to allow large file downloads.
			IdleTimeout: 120 * time.Second,
		}
		log.Printf("dev mode: listening on http://localhost%s", s.cfg.Server.DevAddr)
		return runShutdown(ctx, srv)
	}

	m := &autocert.Manager{
		Cache:      autocert.DirCache(s.cfg.Server.ACMECacheDir),
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(s.cfg.Server.Domain),
		Email:      s.cfg.Server.ACMEEmail,
		Client:     &acme.Client{DirectoryURL: acme.LetsEncryptURL},
	}

	tlsCfg := m.TLSConfig()
	tlsCfg.MinVersion = tls.VersionTLS12

	httpsSrv := &http.Server{
		Addr:        s.cfg.Server.HTTPSAddr,
		Handler:     handler,
		TLSConfig:   tlsCfg,
		ReadTimeout: time.Duration(s.cfg.Server.ReadTimeoutSeconds) * time.Second,
		IdleTimeout: 120 * time.Second,
	}

	// HTTP server: ACME challenge + redirect.
	httpHandler := m.HTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := "https://" + s.cfg.Server.Domain + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	}))
	httpSrv := &http.Server{
		Addr:        s.cfg.Server.HTTPAddr,
		Handler:     httpHandler,
		ReadTimeout: 30 * time.Second,
		IdleTimeout: 60 * time.Second,
	}

	errCh := make(chan error, 2)
	go func() {
		log.Printf("HTTPS listening on %s for %s", s.cfg.Server.HTTPSAddr, s.cfg.Server.Domain)
		err := httpsSrv.ListenAndServeTLS("", "")
		if err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("https: %w", err)
		}
	}()
	go func() {
		log.Printf("HTTP listening on %s (ACME + redirect)", s.cfg.Server.HTTPAddr)
		err := httpSrv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpsSrv.Shutdown(shutdownCtx)
		_ = httpSrv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

func runShutdown(ctx context.Context, srv *http.Server) error {
	errCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// ---- template helpers ----

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	suffix := []string{"K", "M", "G", "T"}[exp]
	return fmt.Sprintf("%.1f %sB", float64(n)/float64(div), suffix)
}

func unixToDate(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}

func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

func firstChar(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

func dictFunc(values ...any) (map[string]any, error) {
	if len(values)%2 != 0 {
		return nil, fmt.Errorf("dict requires an even number of arguments")
	}
	m := make(map[string]any, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		k, ok := values[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict key must be string")
		}
		m[k] = values[i+1]
	}
	return m, nil
}

func (s *Server) baseURL() string {
	if s.cfg.Server.DevMode {
		return "http://localhost" + s.cfg.Server.DevAddr
	}
	return "https://" + s.cfg.Server.Domain
}

func (s *Server) renderHTML(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template %s: %v", name, err)
	}
}

func (s *Server) renderPlist(w http.ResponseWriter, name string, data any, contentType string) {
	if contentType == "" {
		contentType = "application/xml; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	if err := s.plist.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("plist %s: %v", name, err)
	}
}

// joinURL joins base + p, ensuring a single slash between parts.
func joinURL(base, p string) string {
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return base + p
}

// safeFilename returns just the basename, stripping any path traversal.
func safeFilename(name string) string {
	return path.Base(name)
}
