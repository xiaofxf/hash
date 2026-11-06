// Package web serves Hash in a browser through an xterm.js frontend backed by
// a local pseudo-terminal.
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

//go:embed static
var staticFiles embed.FS

const (
	initialCols     = 80
	initialRows     = 24
	defaultMaxRead  = 1 << 20
	assetCacheAge   = 300 // seconds
	contentSecurity = "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; " +
		"connect-src 'self' ws: wss:; " +
		"base-uri 'none'; " +
		"form-action 'none'; " +
		"frame-ancestors 'none'"
)

// Config configures a Server.
type Config struct {
	// Token guards the page and the terminal WebSocket. NewToken generates one
	// when this is empty.
	Token string
	// Command is the program started for each terminal session, with its
	// arguments. It defaults to the running Hash executable.
	Command []string
	// Env holds extra environment variables ("KEY=value") for session
	// processes. They override inherited values.
	Env []string
	// MaxRead limits a single client WebSocket message in bytes. It defaults to
	// 1 MiB, which is plenty for pasted input.
	MaxRead int64
}

// Server serves the terminal page, its static assets and the terminal socket.
type Server struct {
	token     string
	command   []string
	env       []string
	maxRead   int64
	indexHTML []byte
	assets    fs.FS
	upgrader  websocket.Upgrader

	mu       sync.Mutex
	sessions map[*session]struct{}
	closed   bool
}

// NewToken returns a random token suitable for guarding a terminal session.
func NewToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// New creates a Server from cfg.
func New(cfg Config) (*Server, error) {
	token := cfg.Token
	if token == "" {
		generated, err := NewToken()
		if err != nil {
			return nil, err
		}
		token = generated
	}

	command := append([]string(nil), cfg.Command...)
	if len(command) == 0 {
		executable, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locate hash executable: %w", err)
		}
		command = []string{executable}
	}

	maxRead := cfg.MaxRead
	if maxRead <= 0 {
		maxRead = defaultMaxRead
	}

	assets, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return nil, fmt.Errorf("load web assets: %w", err)
	}
	indexHTML, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, fmt.Errorf("load terminal page: %w", err)
	}

	server := &Server{
		token:     token,
		command:   command,
		env:       append([]string(nil), cfg.Env...),
		maxRead:   maxRead,
		indexHTML: indexHTML,
		assets:    assets,
		sessions:  make(map[*session]struct{}),
	}
	server.upgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 32 * 1024,
		CheckOrigin:     server.originAllowed,
	}
	return server, nil
}

// Token returns the token required by the page and the terminal socket.
func (s *Server) Token() string {
	return s.token
}

// Close terminates every active terminal session. New sessions are refused
// afterwards. It is used when the server shuts down.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	sessions := make([]*session, 0, len(s.sessions))
	for sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, sess := range sessions {
		wg.Add(1)
		go func(sess *session) {
			defer wg.Done()
			sess.close()
		}(sess)
	}
	wg.Wait()
}

// track registers an active session, unless the server is shutting down.
func (s *Server) track(sess *session) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return false
	}
	s.sessions[sess] = struct{}{}
	return true
}

// untrack removes a finished session.
func (s *Server) untrack(sess *session) {
	s.mu.Lock()
	delete(s.sessions, sess)
	s.mu.Unlock()
}

// Handler returns the HTTP handler for the terminal page, its assets and the
// WebSocket endpoint.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/ws", s.handleWebSocket)
	mux.Handle("/assets/", http.StripPrefix("/assets/", s.handleAssets()))
	return securityHeaders(mux)
}

// handleIndex serves the terminal page for "/".
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "missing or invalid token", http.StatusUnauthorized)
		return
	}
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(s.indexHTML)
}

// handleAssets serves the embedded frontend files.
func (s *Server) handleAssets() http.Handler {
	files := http.FileServer(http.FS(s.assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// FileServer lists directories; the terminal has no directories to show.
		path := r.URL.Path
		if path == "" || path == "/" || strings.HasSuffix(path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", assetCacheAge))
		files.ServeHTTP(w, r)
	})
}

// handleWebSocket upgrades a request and runs one Hash session for the
// lifetime of the connection.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "missing or invalid token", http.StatusUnauthorized)
		return
	}
	if !s.originAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an HTTP error.
		return
	}
	conn.SetReadLimit(s.maxRead)
	s.runSession(conn)
}

// authorized reports whether the request carries the server token.
func (s *Server) authorized(r *http.Request) bool {
	token := r.URL.Query().Get("token")
	if token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) == 1
}

// originAllowed rejects cross-origin WebSocket connections so that other web
// pages cannot reach the terminal even if they learn its URL. Requests without
// an Origin header (non-browser clients) are allowed; they still need the
// token.
func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

// securityHeaders adds hardening headers to every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", contentSecurity)
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}
