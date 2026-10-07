package installpage

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func get(t *testing.T, o Options, mod func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/install", nil)
	if mod != nil {
		mod(r)
	}
	w := httptest.NewRecorder()
	Handler(o).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	return w
}

func TestConfiguredURLs(t *testing.T) {
	w := get(t, Options{AgentURL: "https://mcp.example.com", AdminURL: "https://deploy-admin.example.com", Version: "1.2.3"},
		func(r *http.Request) { r.Host = "evil.example.net" })
	body := w.Body.String()
	for _, want := range []string{
		"claude mcp add --transport http deploy-gateway https://mcp.example.com/mcp",
		`"url": "https://mcp.example.com/mcp"`,
		"POST https://mcp.example.com/api/v1/&lt;tool&gt;",
		`href="https://deploy-admin.example.com"`,
		"https://mcp.example.com/install",
		"Server version 1.2.3",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(body, "evil.example.net") || strings.Contains(body, "MCP_HOST") {
		t.Error("configured URL must win over the Host header")
	}
	h := w.Header()
	if !strings.HasPrefix(h.Get("Content-Type"), "text/html") || !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "public, max-age=300" {
		t.Fatalf("headers: %v", h)
	}
}

func TestDerivedURL(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.1/32")}
	cases := []struct {
		name string
		mod  func(*http.Request)
		want string
	}{
		{"plain http", func(r *http.Request) { r.Host = "gw.example.com:8080" }, "http://gw.example.com:8080/mcp"},
		{"tls", func(r *http.Request) { r.Host = "gw.example.com"; r.TLS = &tls.ConnectionState{} }, "https://gw.example.com/mcp"},
		{"trusted proxy", func(r *http.Request) {
			r.Host, r.RemoteAddr = "gw.example.com", "10.0.0.1:1234"
			r.Header.Set("X-Forwarded-Proto", "https")
		}, "https://gw.example.com/mcp"},
		{"untrusted proxy", func(r *http.Request) {
			r.Host, r.RemoteAddr = "gw.example.com", "10.0.0.2:1234"
			r.Header.Set("X-Forwarded-Proto", "https")
		}, "http://gw.example.com/mcp"},
		{"bogus proto", func(r *http.Request) {
			r.Host, r.RemoteAddr = "gw.example.com", "10.0.0.1:1234"
			r.Header.Set("X-Forwarded-Proto", "javascript")
		}, "http://gw.example.com/mcp"},
		{"ipv6", func(r *http.Request) { r.Host = "[::1]:8080" }, "http://[::1]:8080/mcp"},
		{"invalid host", func(r *http.Request) { r.Host = `x"><script>` }, "http://gateway.invalid/mcp"},
	}
	for _, c := range cases {
		w := get(t, Options{TrustedProxies: trusted}, c.mod)
		body := w.Body.String()
		if !strings.Contains(body, c.want) {
			t.Errorf("%s: page lacks %q", c.name, c.want)
		}
		if !strings.Contains(body, "MCP_HOST") || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: derived URL must be flagged and not cached", c.name)
		}
		if strings.Contains(body, "<script>") {
			t.Errorf("%s: unescaped host", c.name)
		}
	}
}

func TestNoAdminURL(t *testing.T) {
	body := get(t, Options{AgentURL: "https://mcp.example.com"}, nil).Body.String()
	if !strings.Contains(body, "ask the administrator") {
		t.Fatal("missing admin URL must be explained")
	}
}
