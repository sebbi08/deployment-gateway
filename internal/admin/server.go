// Package admin serves the admin JSON API and the embedded React app
// (spec §14) on the admin listener. Authentication is either the built-in
// password login or a fail-closed trusted forward-auth proxy.
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/api"
	"github.com/sebbi08/deployment-gateway/internal/auth"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/stack"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

// The __Host- prefix makes browsers accept the cookie only host-only,
// Secure and with Path=/, so no other site (such as an agent app on a
// sibling subdomain) can set or shadow it.
const cookieName = "__Host-dg_admin"

type Options struct {
	Svc            *service.Service
	Store          *store.Store
	PasswordHash   string // empty = proxy mode
	TrustedProxies []netip.Prefix
	// ForwardedProxies are further reverse proxies (TRUSTED_PROXIES) whose
	// X-Forwarded-For is trusted for the client IP only, never for proxy-mode
	// authentication.
	ForwardedProxies []netip.Prefix
	UserHeader       string
	UI               fs.FS // built React app (index.html + assets/); may lack index.html
	Logger           *slog.Logger
	// AgentURL and AdminURL are the public base URLs (MCP_HOST, ADMIN_HOST)
	// the UI puts into the agent setup prompt; empty when not configured.
	AgentURL string
	AdminURL string
}

type Server struct {
	opts     Options
	sessions *auth.Sessions
	limiter  *auth.Limiter
	// loginSlots bounds concurrent (memory-hungry) argon2id checks.
	loginSlots chan struct{}
}

func New(o Options) *Server {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Server{opts: o, sessions: auth.NewSessions(12 * time.Hour), limiter: auth.NewLimiter(10, 15*time.Minute, 15*time.Minute), loginSlots: make(chan struct{}, 2)}
}

func (s *Server) proxyMode() bool { return s.opts.PasswordHash == "" }

// Handler routes /api/ to the admin API and everything else to the SPA.
// Password mode: the SPA and POST /api/admin/login are public, the rest of
// the API needs a session. Proxy mode: everything needs the trusted proxy.
func (s *Server) Handler() http.Handler {
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /api/admin/me", s.me)
	apiMux.HandleFunc("POST /api/admin/logout", s.logout)
	s.workspaceRoutes(apiMux)
	s.miscRoutes(apiMux)
	apiMux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, apiError{Code: service.CodeNotFound, Message: "no such endpoint"})
	})

	root := http.NewServeMux()
	var apiH http.Handler = apiMux
	if !s.proxyMode() {
		root.HandleFunc("POST /api/admin/login", s.login)
		apiH = s.requireAdmin(apiMux)
	}
	root.Handle("/api/", noStore(apiH))
	root.Handle("/", s.spa())

	var h http.Handler = root
	if s.proxyMode() {
		h = s.requireAdmin(root)
	}
	return securityHeaders(http.NewCrossOriginProtection().Handler(h))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; form-action 'self'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type actorKey struct{}

func actor(r *http.Request) string {
	a, _ := r.Context().Value(actorKey{}).(string)
	return a
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var who string
		if s.proxyMode() {
			if !auth.PeerTrusted(r, s.opts.TrustedProxies) {
				writeJSON(w, http.StatusForbidden, apiError{Code: "forbidden", Message: "request did not come through the trusted proxy"})
				return
			}
			// Exactly one value: a proxy that adds rather than replaces the
			// header would otherwise pass a client-supplied identity on.
			if vals := r.Header.Values(s.opts.UserHeader); len(vals) == 1 {
				who = strings.TrimSpace(vals[0])
			}
			if who == "" {
				writeJSON(w, http.StatusUnauthorized, apiError{Code: service.CodeUnauthorized, Message: "identity header missing"})
				return
			}
			who = "proxy:" + who
		} else {
			var ok bool
			for _, c := range r.CookiesNamed(cookieName) {
				if who, ok = s.sessions.Lookup(c.Value); ok {
					break
				}
			}
			if !ok {
				writeJSON(w, http.StatusUnauthorized, apiError{Code: "login_required", Message: "login required"})
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, who)))
	})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"actor": actor(r), "proxy_mode": s.proxyMode(),
		"agent_url": s.opts.AgentURL, "admin_url": s.opts.AdminURL})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := auth.ClientIP(r, slices.Concat(s.opts.TrustedProxies, s.opts.ForwardedProxies))
	if s.limiter.Blocked(ip) {
		s.opts.Logger.Warn("admin login rate limited", "client_ip", ip)
		writeJSON(w, http.StatusTooManyRequests, apiError{Code: "rate_limited", Message: "too many failed attempts; try again later"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		badRequest(w, err)
		return
	}
	select {
	case s.loginSlots <- struct{}{}:
		defer func() { <-s.loginSlots }()
	default:
		s.opts.Logger.Warn("admin login rate limited (concurrent attempts)", "client_ip", ip)
		writeJSON(w, http.StatusTooManyRequests, apiError{Code: "rate_limited", Message: "too many concurrent login attempts; try again"})
		return
	}
	if !auth.CheckPassword(s.opts.PasswordHash, body.Password) {
		s.limiter.Fail(ip)
		s.opts.Logger.Warn("admin login failed", "client_ip", ip) // never log the password
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "login_required", Message: "wrong password"})
		return
	}
	s.limiter.Reset(ip)
	id, err := s.sessions.Create("admin")
	if err != nil {
		writeErr(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: id, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	for _, c := range r.CookiesNamed(cookieName) {
		s.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

// spa serves built assets and falls back to index.html for client routes.
func (s *Server) spa() http.Handler {
	ui := s.opts.UI
	built := ui != nil && isFile(ui, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !built {
			http.Error(w, "admin UI not built: run `npm ci && npm run build` in web/", http.StatusServiceUnavailable)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		switch {
		case p != "" && p != "index.html" && isFile(ui, p):
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			http.ServeFileFS(w, r, ui, p)
		case strings.HasPrefix(p, "assets/"):
			http.NotFound(w, r)
		default:
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFileFS(w, r, ui, "index.html")
		}
	})
}

func isFile(fsys fs.FS, name string) bool {
	st, err := fs.Stat(fsys, name)
	return err == nil && st.Mode().IsRegular()
}

// apiError is the admin error body. Unlike the agent API it includes the
// internal detail, because the admin is trusted.
type apiError struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Errors  stack.Errors `json:"errors,omitempty"`
	Detail  string       `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var se *service.Error
	if errors.As(err, &se) {
		writeJSON(w, api.StatusFor(se.Code), apiError{Code: se.Code, Message: se.Message, Errors: se.Fields, Detail: se.Detail()})
		return
	}
	writeJSON(w, http.StatusInternalServerError, apiError{Code: service.CodeInternal, Message: err.Error()})
}

func badRequest(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, apiError{Code: service.CodeValidation, Message: "invalid request body: " + err.Error()})
}

func notFound(w http.ResponseWriter, what string) {
	writeJSON(w, http.StatusNotFound, apiError{Code: service.CodeNotFound, Message: what + " not found"})
}

// decodeJSON rejects unknown fields, trailing data, empty and oversize bodies.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return errors.New("request body required")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after JSON object")
	}
	return nil
}
