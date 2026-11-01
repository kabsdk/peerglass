// Package web is peerglass's HTTP layer: handlers that render HTML with
// html/template (S2) and a fixed set of POST actions, each mapping to one
// rTorrent command (S1).
package web

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/kabsdk/peerglass/internal/rtorrent"
)

// Config is what the server needs besides the rTorrent client (N2).
type Config struct {
	// Labels are the labels the UI offers; any other label is rejected
	// (F6, F8).
	Labels []string
	// Root is the only tree destination paths may point into (S6).
	Root string
}

// Server serves the UI. It is an http.Handler.
type Server struct {
	client *rtorrent.Client
	labels []string
	root   string
	log    *slog.Logger
	tmpl   *template.Template
	mux    *http.ServeMux

	mu       sync.Mutex
	cached   rtorrent.Overview
	cachedAt time.Time
	// versions remembers what each browser shows, so a poll can be answered
	// with only the rows that changed (see renderedList).
	versions     map[string]*renderedList
	versionOrder []string
}

// New wires the routes. Root must be a directory below /.
func New(client *rtorrent.Client, cfg Config, log *slog.Logger) (*Server, error) {
	root := path.Clean("/" + strings.Trim(cfg.Root, "/"))
	if root == "/" {
		return nil, fmt.Errorf("web: root %q must be a directory below /", cfg.Root)
	}
	tmpl, err := template.New("").Funcs(funcMap()).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("web: templates: %w", err)
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Server{client: client, labels: cfg.Labels, root: root, log: log, tmpl: tmpl, versions: map[string]*renderedList{}}

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheFor(24*time.Hour, http.FileServerFS(static))))
	mux.HandleFunc("GET /{$}", s.handlePage)
	mux.HandleFunc("GET /torrents", s.handleList)
	mux.HandleFunc("GET /torrents/{hash}", s.handleDetail)
	mux.HandleFunc("GET /detail", s.handleDetailEmpty)
	mux.HandleFunc("GET /menu/{hash}", s.handleMenu)
	mux.HandleFunc("GET /limits", s.handleLimitsForm)
	mux.HandleFunc("GET /flash", s.handleFlashClear)
	// Every action is POST-only and same-origin (S4).
	mux.Handle("POST /actions/add", s.sameOrigin(http.HandlerFunc(s.handleAdd)))
	mux.Handle("POST /actions/start", s.sameOrigin(s.bulkAction("Started", client.Start)))
	mux.Handle("POST /actions/stop", s.sameOrigin(s.bulkAction("Stopped", client.Stop)))
	mux.Handle("POST /actions/recheck", s.sameOrigin(s.bulkAction("Rechecking", client.CheckHash)))
	mux.Handle("POST /actions/reannounce", s.sameOrigin(s.bulkAction("Reannounced", client.Reannounce)))
	mux.Handle("POST /actions/limits", s.sameOrigin(http.HandlerFunc(s.handleLimits)))
	mux.Handle("POST /actions/label", s.sameOrigin(http.HandlerFunc(s.handleLabel)))
	mux.Handle("POST /actions/remove", s.sameOrigin(s.bulkAction("Removed", client.Erase)))
	mux.Handle("POST /actions/remove-data", s.sameOrigin(s.bulkAction("Removed, with data", client.EraseWithData)))
	s.mux = mux
	return s, nil
}

// csp allows only peerglass's own stylesheet and htmx, with no inline
// scripts, styles or event handlers (S3).
const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// ServeHTTP sets the security headers, dispatches, and logs one line per
// request (N3). Query strings and bodies are never logged.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	h := w.Header()
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	s.mux.ServeHTTP(sw, r)
	s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func cacheFor(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(d.Seconds())))
		next.ServeHTTP(w, r)
	})
}

// sameOrigin rejects a state-changing request unless its Origin, or failing
// that its Referer, names this UI's own host (S4). A request without either
// is rejected too.
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isSameOrigin(r) {
			s.flash(w, http.StatusForbidden, flashData{Kind: "error", Lines: []string{"Request refused: it did not come from this page (missing or foreign Origin)."}})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isSameOrigin(r *http.Request) bool {
	src := r.Header.Get("Origin")
	if src == "" {
		src = r.Header.Get("Referer")
	}
	if src == "" || src == "null" {
		return false
	}
	u, err := url.Parse(src)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// render executes a template into a buffer first, so a template error
// becomes a clean 500 instead of half a page.
func (s *Server) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("template", "name", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// describe turns an error from the client into the sentence shown to the
// user, and logs it (N3). A fault is rTorrent refusing; anything else means
// rTorrent could not be reached (F11).
func (s *Server) describe(err error) (status int, text string) {
	var f *rtorrent.Fault
	if errors.As(err, &f) {
		s.log.Error("rtorrent fault", "code", f.Code, "message", f.Message)
		return http.StatusBadGateway, "rTorrent refused: " + f.Message
	}
	s.log.Error("rtorrent unavailable", "err", err.Error())
	return http.StatusBadGateway, "rTorrent unavailable: " + err.Error()
}
