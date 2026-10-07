// Package installpage serves a public HTML page on the agent listener that
// explains to agents (and their operators) how to connect to the MCP server.
// It contains no secrets: only the public endpoint URLs and the tool list.
package installpage

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"
	"net/netip"
	"regexp"

	"github.com/sebbi08/deployment-gateway/internal/auth"
)

//go:embed install.html.tmpl
var pageSrc string

var page = template.Must(template.New("install").Parse(pageSrc))

type Options struct {
	// AgentURL is the public base URL of the agent endpoint (MCP_HOST);
	// empty means it is derived from each request.
	AgentURL string
	// AdminURL is the public base URL of the admin UI (ADMIN_HOST); may be empty.
	AdminURL string
	Version  string
	// TrustedProxies may set X-Forwarded-Proto for the derived URL.
	TrustedProxies []netip.Prefix
}

type data struct {
	BaseURL, MCPURL, APIURL, InstallURL, AdminURL, Version string
	Derived                                                bool
}

// hostRe accepts what a reverse proxy may pass as Host: a DNS name or IPv4
// address with an optional port, or a bracketed IPv6 address.
var hostRe = regexp.MustCompile(`^(?:[a-zA-Z0-9.-]{1,253}|\[[0-9a-fA-F:.]{2,45}\])(?::[0-9]{1,5})?$`)

// baseURL returns the configured agent URL, or one derived from the
// request when MCP_HOST is unset.
func (o Options) baseURL(r *http.Request) (string, bool) {
	if o.AgentURL != "" {
		return o.AgentURL, false
	}
	host := r.Host
	if !hostRe.MatchString(host) {
		host = "gateway.invalid"
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if auth.PeerTrusted(r, o.TrustedProxies) {
		if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
			scheme = p
		}
	}
	return scheme + "://" + host, true
}

// Handler renders the install page.
func Handler(o Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base, derived := o.baseURL(r)
		d := data{BaseURL: base, MCPURL: base + "/mcp", APIURL: base + "/api/v1", InstallURL: base + "/install",
			AdminURL: o.AdminURL, Version: o.Version, Derived: derived}
		var buf bytes.Buffer
		if err := page.Execute(&buf, d); err != nil {
			http.Error(w, "cannot render page", http.StatusInternalServerError)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if derived {
			// The URLs come from the request's Host header.
			h.Set("Cache-Control", "no-store")
			h.Set("Vary", "Host, X-Forwarded-Proto")
		} else {
			h.Set("Cache-Control", "public, max-age=300")
		}
		w.Write(buf.Bytes())
	})
}
