// Package web serves netward's web interface and its JSON API.
//
// The interface is rendered on the server with [html/template]. HTMX, which
// is embedded together with the stylesheet, refreshes the overview every 30
// seconds without reloading the page, and every page also works without
// JavaScript. Nothing is loaded from other hosts.
//
// The API under /api returns the latest snapshot of every feature and the
// recent changes as JSON.
package web

import (
	"context"
	"crypto/tls"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/devusSs/netward/internal/domain/domainip"
	"github.com/devusSs/netward/internal/domain/privatenet"
	"github.com/devusSs/netward/internal/domain/publicnet"
	"github.com/devusSs/netward/internal/service/history"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 2 * time.Minute
	shutdownTimeout   = 5 * time.Second
)

//go:embed static templates
var assets embed.FS

// Snapshots reads stored snapshots, newest first. The SQLite store
// implements it.
type Snapshots interface {
	PublicNetSnapshots(ctx context.Context, from, to time.Time, limit int) ([]*publicnet.Snapshot, error)
	PrivateNetSnapshots(ctx context.Context, from, to time.Time, limit int) ([]*privatenet.Snapshot, error)
	DomainIPSnapshots(ctx context.Context, from, to time.Time, limit int) ([]*domainip.Snapshot, error)
}

// Changes lists recent changes. [history.Service] implements it.
type Changes interface {
	Changes(ctx context.Context, since time.Time, limit int) ([]history.Event, error)
}

// Features tells which features are enabled, so the interface can tell a
// disabled feature from one that has no data yet.
type Features struct {
	PublicNet  bool
	PrivateNet bool
	DomainIP   bool
}

// Config configures a [Server].
type Config struct {
	// Address is the address to listen on, for example ":8443".
	Address string
	// TLS enables HTTPS when set. It needs a certificate, for example through
	// GetCertificate.
	TLS *tls.Config
	// Snapshots reads the stored snapshots. It is required.
	Snapshots Snapshots
	// Changes lists recent changes. It is required.
	Changes Changes
	// Features tells which features are enabled.
	Features Features
	// Logger receives the server's logs. Nil discards them.
	Logger *slog.Logger
	// Now returns the current time. Nil uses [time.Now].
	Now func() time.Time
	// Location is the time zone times are shown in. Nil uses [time.Local].
	Location *time.Location
}

// Server serves the web interface and the API.
type Server struct {
	address   string
	tls       *tls.Config
	snapshots Snapshots
	changes   Changes
	features  Features
	logger    *slog.Logger
	now       func() time.Time
	location  *time.Location
	templates *template.Template
	handler   http.Handler
}

// New returns a server for cfg.
func New(cfg Config) (*Server, error) {
	switch {
	case cfg.Snapshots == nil:
		return nil, errors.New("snapshots are required")
	case cfg.Changes == nil:
		return nil, errors.New("changes are required")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	location := cfg.Location
	if location == nil {
		location = time.Local
	}

	templates, err := template.New("").ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	s := &Server{
		address:   cfg.Address,
		tls:       cfg.TLS,
		snapshots: cfg.Snapshots,
		changes:   cfg.Changes,
		features:  cfg.Features,
		logger:    logger.With(slog.String("component", "web")),
		now:       now,
		location:  location,
		templates: templates,
		handler:   nil,
	}

	s.handler = s.routes()

	return s, nil
}

// Handler returns the server's HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Run listens on the configured address and serves until ctx ends, then
// shuts down gracefully.
func (s *Server) Run(ctx context.Context) error {
	var lc net.ListenConfig

	listener, err := lc.Listen(ctx, "tcp", s.address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.address, err)
	}

	return s.Serve(ctx, listener)
}

// Serve serves on listener until ctx ends, then shuts down gracefully. It
// closes listener.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	srv := &http.Server{
		Handler:           s.handler,
		TLSConfig:         s.tls,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		// Browsers that do not trust the self-signed certificate yet abort
		// handshakes, which the server reports as errors; they are noise.
		ErrorLog: slog.NewLogLogger(s.logger.Handler(), slog.LevelDebug),
		BaseContext: func(net.Listener) context.Context {
			return context.WithoutCancel(ctx)
		},
	}

	scheme := "http"
	if s.tls != nil {
		scheme = "https"
	}

	s.logger.InfoContext(ctx, "web interface listening",
		slog.String("url", scheme+"://"+listener.Addr().String()),
	)

	served := make(chan error, 1)

	go func() {
		if s.tls != nil {
			served <- srv.ServeTLS(listener, "", "")
		} else {
			served <- srv.Serve(listener)
		}
	}()

	select {
	case err := <-served:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	err := srv.Shutdown(shutdownCtx)
	if err != nil {
		return fmt.Errorf("shut down web interface: %w", err)
	}

	err = <-served
	if !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}

	return nil
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.handleOverviewPage)
	mux.HandleFunc("GET /fragments/overview", s.handleOverviewFragment)

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/publicnet/latest", s.handlePublicNetLatest)
	mux.HandleFunc("GET /api/privatenet/latest", s.handlePrivateNetLatest)
	mux.HandleFunc("GET /api/domainip/latest", s.handleDomainIPLatest)
	mux.HandleFunc("GET /api/changes", s.handleChanges)

	static, err := fs.Sub(assets, "static")
	if err != nil {
		panic(fmt.Sprintf("embedded static files: %v", err)) // The directory is embedded at build time.
	}

	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(time.Hour, http.FileServerFS(static))))

	return s.logRequests(securityHeaders(mux))
}

// securityHeaders sets headers that keep the interface from being framed,
// sniffed or extended with foreign scripts.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; "+
			"img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")

		next.ServeHTTP(w, r)
	})
}

func cacheFor(d time.Duration, next http.Handler) http.Handler {
	value := fmt.Sprintf("public, max-age=%d", int(d.Seconds()))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", value)
		next.ServeHTTP(w, r)
	})
}

// statusRecorder remembers the status code a handler wrote.
type statusRecorder struct {
	http.ResponseWriter

	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		s.logger.DebugContext(r.Context(), "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Duration("duration", s.now().Sub(start)),
		)
	})
}
