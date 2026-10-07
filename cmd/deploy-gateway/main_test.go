package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
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
	if a.MaxInFlight <= 0 || a.RateBurst <= 0 || a.RatePerSecond <= 0 || a.Logger != logger || a.Limiter == nil || len(a.TrustedProxies) != 1 {
		t.Fatalf("authenticator not wired: %+v", a)
	}
}

// The agent limiter blocks an address after 10 failed logins within a
// minute, for 15 minutes.
func TestNewAuthenticatorLimiterValues(t *testing.T) {
	a := newAuthenticator(nil, config.Config{}, slog.Default())
	start := time.Unix(1_000_000, 0)
	now := start
	a.Limiter.Now = func() time.Time { return now }
	for i := range 10 {
		if a.Limiter.Blocked("192.0.2.1") {
			t.Fatalf("blocked after %d failures", i)
		}
		now = start.Add(time.Duration(i) * 5 * time.Second) // 10 failures over 45 s
		a.Limiter.Fail("192.0.2.1")
	}
	if !a.Limiter.Blocked("192.0.2.1") {
		t.Fatal("10 failures within a minute must block")
	}
	blockedAt := now
	now = blockedAt.Add(15*time.Minute - time.Second)
	if !a.Limiter.Blocked("192.0.2.1") {
		t.Fatal("block ended before 15 minutes")
	}
	now = blockedAt.Add(15 * time.Minute)
	if a.Limiter.Blocked("192.0.2.1") {
		t.Fatal("block lasted more than 15 minutes")
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

// In proxy mode the identity header is trusted from every address in
// ADMIN_TRUSTED_PROXIES; a range lets other containers on that network
// claim an admin identity.
func TestWarnBroadAdminProxies(t *testing.T) {
	logs := func(cfg config.Config) string {
		var buf bytes.Buffer
		warnBroadAdminProxies(cfg, slog.New(slog.NewTextHandler(&buf, nil)))
		return buf.String()
	}
	broad := config.Config{AdminTrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")}}
	if out := logs(broad); !strings.Contains(out, "ADMIN_TRUSTED_PROXIES") || !strings.Contains(out, "172.20.0.0/16") {
		t.Fatalf("proxy mode with a range must warn, got %q", out)
	}
	single := config.Config{AdminTrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.20.0.10/32"), netip.MustParsePrefix("fd00::5/128")}}
	if out := logs(single); out != "" {
		t.Fatalf("single addresses must not warn, got %q", out)
	}
	password := broad
	password.AdminPasswordHash = "$argon2id$x"
	if out := logs(password); out != "" {
		t.Fatalf("password mode must not warn, got %q", out)
	}
}

type fakePools struct {
	configured bool
	err        error
}

func (f fakePools) AddressPoolsConfigured(context.Context) (bool, error) { return f.configured, f.err }

// Every app gets its own Docker network; the daemon's built-in pools allow
// only about 31 networks host-wide.
func TestWarnDefaultAddressPools(t *testing.T) {
	logs := func(p fakePools) string {
		var buf bytes.Buffer
		warnDefaultAddressPools(context.Background(), p, slog.New(slog.NewTextHandler(&buf, nil)))
		return buf.String()
	}
	if out := logs(fakePools{}); !strings.Contains(out, "default-address-pools") {
		t.Fatalf("built-in pools must warn, got %q", out)
	}
	if out := logs(fakePools{configured: true}); out != "" {
		t.Fatalf("configured pools must not warn, got %q", out)
	}
}

// A deploy can wait for the compose slot and then run up to three compose
// operations (pull, up, teardown), each bounded by DEPLOY_TIMEOUT+30s with a
// wait of up to DEPLOY_TIMEOUT; the response must still reach the agent.
func TestAgentWriteTimeoutCoversDeployBudget(t *testing.T) {
	for _, d := range []time.Duration{30 * time.Second, 300 * time.Second} {
		budget := 3 * (d + d + 30*time.Second)
		if got := agentWriteTimeout(d); got <= budget {
			t.Errorf("DEPLOY_TIMEOUT %v: write timeout %v, want more than %v", d, got, budget)
		}
	}
}

type fakeRuntime string

func (f fakeRuntime) DefaultRuntime(context.Context) (string, error) { return string(f), nil }

// Every container gets the daemon's default runtime; refuse to start when
// the runtime check would reject all of them or when it is not allowed.
func TestCheckDefaultRuntime(t *testing.T) {
	ctx := context.Background()
	if err := checkDefaultRuntime(ctx, fakeRuntime("runc"), nil); err != nil {
		t.Fatal(err)
	}
	if err := checkDefaultRuntime(ctx, fakeRuntime("runsc"), nil); err == nil || !strings.Contains(err.Error(), "EXTRA_RUNTIMES") {
		t.Fatalf("runsc without EXTRA_RUNTIMES: %v", err)
	}
	if err := checkDefaultRuntime(ctx, fakeRuntime("runsc"), []string{"runsc"}); err != nil {
		t.Fatal(err)
	}
	if err := checkDefaultRuntime(ctx, fakeRuntime("nvidia"), nil); err == nil {
		t.Fatal("nvidia default runtime must be refused")
	}
}

// Only ADMIN_TRUSTED_PROXIES may assert the admin identity; TRUSTED_PROXIES
// (the agent proxy) only supply forwarded client IPs.
func TestAdminOptionsTrustWiring(t *testing.T) {
	cfg := config.Config{
		AdminTrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.1/32")},
		TrustedProxies:      []netip.Prefix{netip.MustParsePrefix("172.20.0.10/32")},
		AdminUserHeader:     "Remote-User",
		AdminPasswordHash:   "hash",
	}
	o := adminOptions(cfg, nil, nil, slog.Default())
	if !slices.Equal(o.TrustedProxies, cfg.AdminTrustedProxies) || !slices.Equal(o.ForwardedProxies, cfg.TrustedProxies) {
		t.Fatalf("trusted = %v, forwarded = %v", o.TrustedProxies, o.ForwardedProxies)
	}
	if o.UserHeader != "Remote-User" || o.PasswordHash != "hash" {
		t.Fatalf("options = %+v", o)
	}
}

// The Docker daemon resolves bind sources on the host, so a symlinked
// WORKSPACES_ROOT is refused before anything else starts.
func TestServeRefusesSymlinkedWorkspacesRoot(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// DataDir below a regular file: should the check ever pass, serve
	// still stops at MkdirAll instead of reaching Docker.
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{WorkspacesRoot: link, DataDir: filepath.Join(blocker, "data")}
	err := serve(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "symlinks") {
		t.Fatalf("serve with a symlinked root: %v, want the symlink error", err)
	}
}

// Each listener serves its own handler: the admin API must never be
// reachable on the agent address.
func TestNewServersBindsHandlersToListeners(t *testing.T) {
	cfg := config.Config{AgentListen: ":8080", AdminListen: "127.0.0.1:8081", DeployTimeout: time.Minute}
	agentH := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(201) })
	adminH := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(202) })
	got := map[string]int{}
	for _, srv := range newServers(cfg, agentH, adminH) {
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		got[srv.Addr] = w.Code
		if srv.ReadHeaderTimeout <= 0 || srv.WriteTimeout != agentWriteTimeout(cfg.DeployTimeout) {
			t.Errorf("%s: timeouts not set: %+v", srv.Addr, srv)
		}
	}
	if len(got) != 2 || got[":8080"] != 201 || got["127.0.0.1:8081"] != 202 {
		t.Fatalf("listener -> handler = %v", got)
	}
}

// A missing /.dockerenv means "on the host"; then a failed lookup of our own
// container is not an error.
func TestRunningInContainer(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, ".dockerenv")
	if runningInContainer(marker) {
		t.Fatal("no marker file: not in a container")
	}
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !runningInContainer(marker) {
		t.Fatal("marker file present: in a container")
	}
}
