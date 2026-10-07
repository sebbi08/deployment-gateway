package admin

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sebbi08/deployment-gateway/internal/auth"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/service/servicetest"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

var builtUI = fstest.MapFS{
	"index.html":        {Data: []byte(`<!doctype html><div id="root"></div>`)},
	"assets/app-123.js": {Data: []byte(`console.log(1)`)},
	"favicon.svg":       {Data: []byte(`<svg/>`)},
}

type testEnv struct {
	h      http.Handler
	svc    *service.Service
	st     *store.Store
	docker *servicetest.FakeDocker
	root   string
}

func newEnv(t *testing.T, proxy bool, ui fs.FS) *testEnv {
	t.Helper()
	return newEnvWith(t, proxy, ui, nil)
}

func newEnvWith(t *testing.T, proxy bool, ui fs.FS, opt func(*Options)) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	compose, docker := &servicetest.FakeCompose{}, servicetest.NewFakeDocker()
	servicetest.Wire(compose, docker)
	docker.Networks["apps"] = true
	root := t.TempDir()
	svc := service.New(st, compose, docker, root)
	svc.Chown = func(string, int, int) error { return nil }
	o := Options{Svc: svc, Store: st, UI: ui}
	if proxy {
		o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.1/32")}
		o.UserHeader = "Remote-User"
	} else {
		hash, err := auth.HashPassword("correct horse battery")
		if err != nil {
			t.Fatal(err)
		}
		o.PasswordHash = hash
	}
	if opt != nil {
		opt(&o)
	}
	return &testEnv{h: New(o).Handler(), svc: svc, st: st, docker: docker, root: root}
}

func (e *testEnv) do(method, path string, body any, mut ...func(*http.Request)) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	for _, m := range mut {
		m(r)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

func (e *testEnv) login(t *testing.T) func(*http.Request) {
	t.Helper()
	w := e.do("POST", "/api/admin/login", map[string]string{"password": "correct horse battery"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie = %+v", cookies)
	}
	c := cookies[0]
	return func(r *http.Request) { r.AddCookie(c) }
}

func TestPasswordModeSession(t *testing.T) {
	e := newEnv(t, false, builtUI)
	w := e.do("GET", "/api/admin/me", nil)
	if w.Code != http.StatusUnauthorized || decode[apiError](t, w).Code != "login_required" {
		t.Fatalf("me without session: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", "/api/admin/login", map[string]string{"password": "wrong"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", w.Code)
	}
	if w := e.do("POST", "/api/admin/login", map[string]any{"password": "x", "admin": true}); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", w.Code)
	}
	c := e.login(t)
	w = e.do("GET", "/api/admin/me", nil, c)
	me := decode[map[string]any](t, w)
	if w.Code != 200 || me["actor"] != "admin" || me["proxy_mode"] != false || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("me: %d %v %v", w.Code, me, w.Header())
	}
	if w := e.do("POST", "/api/admin/logout", nil, c); w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := e.do("GET", "/api/admin/me", nil, c); w.Code != http.StatusUnauthorized {
		t.Fatal("session still valid after logout")
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t, false, builtUI)
	for i := 0; i < 10; i++ {
		e.do("POST", "/api/admin/login", map[string]string{"password": "wrong"})
	}
	if w := e.do("POST", "/api/admin/login", map[string]string{"password": "correct horse battery"}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
}

func TestFailedLoginsAreLogged(t *testing.T) {
	var buf bytes.Buffer
	e := newEnvWith(t, false, builtUI, func(o *Options) { o.Logger = slog.New(slog.NewTextHandler(&buf, nil)) })
	from := func(r *http.Request) { r.RemoteAddr = "198.51.100.7:5555" }
	e.do("POST", "/api/admin/login", map[string]string{"password": "hunter2-wrong"}, from)
	if out := buf.String(); !strings.Contains(out, "admin login failed") || !strings.Contains(out, "198.51.100.7") {
		t.Fatalf("failed login not logged with client IP: %q", out)
	}
	for i := 0; i < 10; i++ {
		e.do("POST", "/api/admin/login", map[string]string{"password": "hunter2-wrong"}, from)
	}
	if out := buf.String(); !strings.Contains(out, "admin login rate limited") {
		t.Fatalf("rate-limited login not logged: %q", out)
	}
	buf.Reset()
	e.do("POST", "/api/admin/login", map[string]string{"password": "correct horse battery"}, func(r *http.Request) { r.RemoteAddr = "198.51.100.8:1" })
	if strings.Contains(buf.String(), "failed") {
		t.Fatalf("successful login logged as failure: %q", buf.String())
	}
	if strings.Contains(buf.String(), "hunter2") || strings.Contains(buf.String(), "correct horse") {
		t.Fatal("password logged")
	}
}

func TestCrossOriginRejected(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	w := e.do("POST", "/api/admin/logout", nil, c, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST: %d", w.Code)
	}
}

func TestProxyMode(t *testing.T) {
	e := newEnv(t, true, builtUI)
	if w := e.do("GET", "/", nil); w.Code != http.StatusForbidden {
		t.Fatalf("untrusted peer must not even get the SPA: %d", w.Code)
	}
	trusted := func(r *http.Request) { r.RemoteAddr = "10.0.0.1:4444" }
	if w := e.do("GET", "/api/admin/me", nil, trusted); w.Code != http.StatusUnauthorized || decode[apiError](t, w).Code != "unauthorized" {
		t.Fatalf("missing header: %d %s", w.Code, w.Body.String())
	}
	user := func(r *http.Request) { trusted(r); r.Header.Set("Remote-User", "alice-admin") }
	w := e.do("GET", "/api/admin/me", nil, user)
	me := decode[map[string]any](t, w)
	if w.Code != 200 || me["actor"] != "proxy:alice-admin" || me["proxy_mode"] != true {
		t.Fatalf("me: %d %v", w.Code, me)
	}
	if w := e.do("POST", "/api/admin/login", map[string]string{"password": "x"}, user); w.Code != http.StatusNotFound {
		t.Fatalf("login must not exist in proxy mode: %d", w.Code)
	}
}

func TestSPAServing(t *testing.T) {
	e := newEnv(t, false, builtUI)
	w := e.do("GET", "/", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `id="root"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("index: %d %v", w.Code, w.Header())
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers: %v", w.Header())
	}
	if w := e.do("GET", "/workspaces/alice", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `id="root"`) {
		t.Fatalf("client route fallback: %d", w.Code)
	}
	w = e.do("GET", "/assets/app-123.js", nil)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %v", w.Code, w.Header())
	}
	if w := e.do("GET", "/assets/missing.js", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing asset: %d", w.Code)
	}
	if w := e.do("POST", "/", nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST to SPA: %d", w.Code)
	}
}

func TestUINotBuilt(t *testing.T) {
	e := newEnv(t, false, fstest.MapFS{".keep": {}})
	w := e.do("GET", "/", nil)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "admin UI not built") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestUnknownEndpointIsJSON404(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	w := e.do("GET", "/api/admin/nope", nil, c)
	if w.Code != http.StatusNotFound || decode[apiError](t, w).Code != "not_found" {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestLoginConcurrencyBounded(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{PasswordHash: hash, UI: builtUI})
	h := s.Handler()
	login := func() *httptest.ResponseRecorder {
		b, _ := json.Marshal(map[string]string{"password": "correct horse battery"})
		r := httptest.NewRequest("POST", "/api/admin/login", bytes.NewReader(b))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for i := 0; i < cap(s.loginSlots); i++ {
		s.loginSlots <- struct{}{}
	}
	w := login()
	if w.Code != http.StatusTooManyRequests || decode[apiError](t, w).Code != "rate_limited" {
		t.Fatalf("slots full: %d %s", w.Code, w.Body.String())
	}
	for i := 0; i < cap(s.loginSlots); i++ {
		<-s.loginSlots
	}
	if w := login(); w.Code != http.StatusNoContent {
		t.Fatalf("after drain: %d %s", w.Code, w.Body.String())
	}
}

// Behind a reverse proxy, failed logins of one client must not block the
// admin's login from another client: the limiter keys on the forwarded IP
// when the proxy is listed in ForwardedProxies (TRUSTED_PROXIES).
func TestLoginLimiterUsesForwardedClientIP(t *testing.T) {
	e := newEnvWith(t, false, builtUI, func(o *Options) {
		o.ForwardedProxies = []netip.Prefix{netip.MustParsePrefix("172.20.0.10/32")}
	})
	via := func(client string) func(*http.Request) {
		return func(r *http.Request) {
			r.RemoteAddr = "172.20.0.10:4000"
			r.Header.Set("X-Forwarded-For", client)
		}
	}
	for range 11 {
		e.do("POST", "/api/admin/login", map[string]string{"password": "wrong"}, via("203.0.113.66"))
	}
	if w := e.do("POST", "/api/admin/login", map[string]string{"password": "correct horse battery"}, via("198.51.100.20")); w.Code != http.StatusNoContent {
		t.Fatalf("admin login from another client behind the proxy: %d %s", w.Code, w.Body.String())
	}
}

// ForwardedProxies only affects the client IP, never proxy-mode trust.
func TestForwardedProxiesDoNotGrantProxyModeTrust(t *testing.T) {
	e := newEnvWith(t, true, builtUI, func(o *Options) {
		o.ForwardedProxies = []netip.Prefix{netip.MustParsePrefix("172.20.0.10/32")}
	})
	w := e.do("GET", "/api/admin/me", nil, func(r *http.Request) {
		r.RemoteAddr = "172.20.0.10:4000"
		r.Header.Set("Remote-User", "mallory")
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("peer only in ForwardedProxies got %d, want 403", w.Code)
	}
}

// The __Host- prefix makes browsers refuse the cookie from any other site
// (e.g. an agent app on a sibling subdomain), and a shadowing cookie with
// the same name must not hide a valid session.
func TestSessionCookieCannotBeShadowed(t *testing.T) {
	e := newEnv(t, false, builtUI)
	w := e.do("POST", "/api/admin/login", map[string]string{"password": "correct horse battery"})
	cs := w.Result().Cookies()
	if len(cs) != 1 || cs[0].Name != "__Host-dg_admin" || cs[0].Path != "/" || !cs[0].Secure || cs[0].Domain != "" {
		t.Fatalf("session cookie = %+v, want __Host-dg_admin, Secure, Path=/, host-only", cs)
	}
	valid := cs[0].Value
	w = e.do("GET", "/api/admin/me", nil, func(r *http.Request) {
		r.Header.Add("Cookie", "__Host-dg_admin=planted-by-agent-app; __Host-dg_admin="+valid)
	})
	if w.Code != http.StatusOK {
		t.Fatalf("valid session hidden by a shadowing cookie: %d", w.Code)
	}
}

// A proxy that adds the identity header instead of replacing it would pass
// the client's value through; more than one value is rejected.
func TestProxyModeRejectsRepeatedIdentityHeader(t *testing.T) {
	e := newEnv(t, true, builtUI)
	w := e.do("GET", "/api/admin/me", nil, func(r *http.Request) {
		r.RemoteAddr = "10.0.0.1:4444"
		r.Header.Add("Remote-User", "mallory")
		r.Header.Add("Remote-User", "alice")
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("two identity headers got %d, want 401", w.Code)
	}
}

// Only the trusted proxy may assert an identity; the same header from any
// other peer is a forgery.
func TestProxyModeRejectsIdentityHeaderFromUntrustedPeer(t *testing.T) {
	e := newEnv(t, true, builtUI)
	forged := func(r *http.Request) {
		r.RemoteAddr = "10.0.0.2:4444"
		r.Header.Set("Remote-User", "alice-admin")
	}
	if w := e.do("GET", "/api/admin/me", nil, forged); w.Code != http.StatusForbidden {
		t.Fatalf("forged identity header got %d %s, want 403", w.Code, w.Body.String())
	}
	if w := e.do("GET", "/api/admin/workspaces", nil, forged); w.Code != http.StatusForbidden {
		t.Fatalf("forged identity header got %d on the API, want 403", w.Code)
	}
}
