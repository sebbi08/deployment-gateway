package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/agentauth"
	"github.com/sebbi08/deployment-gateway/internal/auth"
	"github.com/sebbi08/deployment-gateway/internal/config"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/service/servicetest"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

func TestHashPassword(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := hashPassword(strings.NewReader("a long enough secret\n"), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	h := strings.TrimSpace(out.String())
	if !auth.CheckPassword(h, "a long enough secret") {
		t.Fatalf("hash %q does not verify", h)
	}
	out.Reset()
	if code := hashPassword(strings.NewReader("short\n"), &out, &errOut); code == 0 || out.Len() != 0 {
		t.Fatal("short password must be rejected")
	}
}

type fakeMounts struct {
	src         string
	inContainer bool
	err         error
}

func (f fakeMounts) MountSource(context.Context, string, string) (string, bool, error) {
	return f.src, f.inContainer, f.err
}

func TestCheckSameMount(t *testing.T) {
	root := "/srv/deploy-gateway/workspaces"
	cases := []struct {
		name    string
		m       fakeMounts
		wantErr bool
		cont    bool
	}{
		{"container, own container not found", fakeMounts{inContainer: false}, true, true},
		{"host, not found", fakeMounts{inContainer: false}, false, false},
		{"container, same path", fakeMounts{src: root, inContainer: true}, false, true},
		{"on host", fakeMounts{inContainer: false}, false, false},
		{"same path", fakeMounts{src: root, inContainer: true}, false, false},
		{"different host path", fakeMounts{src: "/var/lib/other", inContainer: true}, true, false},
		{"not mounted", fakeMounts{src: "", inContainer: true}, true, false},
		{"inspect error", fakeMounts{err: errors.New("boom")}, true, false},
	}
	for _, c := range cases {
		err := checkSameMount(context.Background(), c.m, "abc123", root, c.cont)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestAgentHandlerHealthz(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	compose, docker := &servicetest.FakeCompose{}, servicetest.NewFakeDocker()
	svc := service.New(st, compose, docker, t.TempDir())
	authn := &agentauth.Authenticator{Store: st, Limiter: auth.NewLimiter(10, time.Minute, time.Hour)}
	for _, c := range []struct {
		err  error
		want int
	}{{nil, 200}, {errors.New("down"), 503}} {
		h := agentHandler(svc, authn, fakePinger{c.err})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
		if w.Code != c.want {
			t.Errorf("healthz with ping err %v: %d", c.err, w.Code)
		}
	}
	w := httptest.NewRecorder()
	agentHandler(svc, authn, fakePinger{}).ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/whoami", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("api without token: %d", w.Code)
	}
}

func TestWarnUntrustedProxies(t *testing.T) {
	var buf bytes.Buffer
	warnUntrustedProxies(config.Config{}, slog.New(slog.NewTextHandler(&buf, nil)))
	if !strings.Contains(buf.String(), "TRUSTED_PROXIES") {
		t.Fatalf("empty TRUSTED_PROXIES must warn, got %q", buf.String())
	}
	buf.Reset()
	warnUntrustedProxies(config.Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.20.0.10/32")}},
		slog.New(slog.NewTextHandler(&buf, nil)))
	if buf.Len() != 0 {
		t.Fatalf("configured TRUSTED_PROXIES must not warn, got %q", buf.String())
	}
}

func TestNewAuthenticatorWiring(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	cfg := config.Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.20.0.10/32")}}
	a := newAuthenticator(nil, cfg, logger)
	if a.MaxInFlight <= 0 || a.Logger != logger || a.Limiter == nil || len(a.TrustedProxies) != 1 {
		t.Fatalf("authenticator not wired: %+v", a)
	}
}

// Both agent endpoints read the whole body before checking the token.
func TestAgentHandlerReadsBodyBeforeAuth(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := service.New(st, &servicetest.FakeCompose{}, servicetest.NewFakeDocker(), t.TempDir())
	authn := &agentauth.Authenticator{Store: st, Limiter: auth.NewLimiter(10, time.Minute, time.Hour)}
	h := agentHandler(svc, authn, fakePinger{})
	for _, path := range []string{"/api/v1/whoami", "/mcp"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(strings.Repeat("x", agentauth.MaxBodyBytes+1))))
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: oversize body without token got %d, want 413 (body read before auth)", path, w.Code)
		}
	}
}
