package agentauth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/sebbi08/deployment-gateway/internal/auth"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

func setup(t *testing.T) (*Authenticator, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	w, err := st.CreateWorkspace(context.Background(), store.Workspace{Name: "alice", Quota: store.BuiltinDefaultQuota(), PortBindIP: "0.0.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	tok, hash, prefix, _ := auth.GenerateToken()
	if _, err := st.CreateToken(context.Background(), w.ID, "ci", hash, prefix); err != nil {
		t.Fatal(err)
	}
	return &Authenticator{Store: st, Limiter: auth.NewLimiter(3, time.Minute, time.Hour)}, tok
}

func TestVerify(t *testing.T) {
	a, tok := setup(t)
	p, err := a.Verify(context.Background(), tok, "1.2.3.4")
	if err != nil || p.Workspace.Name != "alice" || p.ClientIP != "1.2.3.4" || p.TokenID == 0 {
		t.Fatalf("Verify = %+v, %v", p, err)
	}
	for _, bad := range []string{"", "dgw_short", "Bearer x", tok + "x"} {
		if _, err := a.Verify(context.Background(), bad, "5.5.5.5"); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("token %q: %v", bad, err)
		}
	}
	if !a.Limiter.Blocked("5.5.5.5") {
		t.Fatal("repeated failures must block the IP")
	}
}

// A blocked IP is often a shared proxy address, so it must never lock out
// valid tokens; 256-bit tokens make the block irrelevant against guessing.
func TestVerifyValidTokenPassesFromBlockedIP(t *testing.T) {
	a, tok := setup(t)
	for range 3 {
		a.Verify(context.Background(), "dgw_short", "172.20.0.10")
	}
	if !a.Limiter.Blocked("172.20.0.10") {
		t.Fatal("precondition: IP not blocked")
	}
	p, err := a.Verify(context.Background(), tok, "172.20.0.10")
	if err != nil || p.Workspace.Name != "alice" {
		t.Fatalf("valid token from blocked IP: %+v, %v", p, err)
	}
}

func TestVerifyLogsFailuresAndBlockOnce(t *testing.T) {
	a, tok := setup(t)
	var buf bytes.Buffer
	a.Logger = slog.New(slog.NewTextHandler(&buf, nil))
	for range 5 {
		a.Verify(context.Background(), tok+"x", "9.9.9.9")
	}
	out := buf.String()
	if n := strings.Count(out, "agent auth failed"); n != 2 {
		t.Errorf("want 2 failure lines before the block, got %d:\n%s", n, out)
	}
	if n := strings.Count(out, "agent auth blocked"); n != 1 {
		t.Errorf("want exactly 1 block line, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "client_ip=9.9.9.9") {
		t.Errorf("client IP not logged:\n%s", out)
	}
	if strings.Contains(out, tok) {
		t.Error("token must never be logged")
	}
}

func TestMiddleware(t *testing.T) {
	a, tok := setup(t)
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := FromContext(r.Context())
		if !ok {
			t.Error("principal missing")
		}
		w.Write([]byte(p.Workspace.Name))
	}))
	r := httptest.NewRequest("POST", "/api/v1/whoami", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "alice" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("POST", "/api/v1/whoami", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 || w.Body.String() != `{"code":"unauthorized","message":"unauthorized"}`+"\n" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}

func TestMCPVerifier(t *testing.T) {
	a, tok := setup(t)
	v := a.MCPVerifier()
	r := httptest.NewRequest("POST", "/mcp", nil)
	info, err := v(context.Background(), tok, r)
	if err != nil || info.UserID == "" {
		t.Fatalf("info = %+v, %v", info, err)
	}
	if _, err := v(context.Background(), "dgw_nope", r); !errors.Is(err, sdkauth.ErrInvalidToken) {
		t.Fatalf("bad token: %v", err)
	}
}

func TestMiddlewareCapsConcurrentCallsPerToken(t *testing.T) {
	a, tok := setup(t)
	a.MaxInFlight = 2
	release := make(chan struct{})
	h := a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	r1 := make(chan int, 2)
	for range 2 {
		go func() {
			r := httptest.NewRequest("POST", "/x", nil)
			r.Header.Set("Authorization", "Bearer "+tok)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			r1 <- w.Code
		}()
	}
	waitInFlight(t, a, 2)
	r := httptest.NewRequest("POST", "/x", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"code":"too_many_requests"`) {
		t.Fatalf("third concurrent call: %d %q, want 429 too_many_requests", w.Code, w.Body.String())
	}
	close(release)
	if c1, c2 := <-r1, <-r1; c1 != 200 || c2 != 200 {
		t.Fatalf("blocked calls finished with %d, %d", c1, c2)
	}
	w = httptest.NewRecorder()
	h2 := a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	h2.ServeHTTP(w, r.Clone(r.Context()))
	if w.Code != 200 {
		t.Fatalf("after release the slot must be free again, got %d", w.Code)
	}
}

func TestLimitMCPCapsConcurrentCallsPerToken(t *testing.T) {
	a, tok := setup(t)
	a.MaxInFlight = 1
	release := make(chan struct{})
	h := sdkauth.RequireBearerToken(a.MCPVerifier(), &sdkauth.RequireBearerTokenOptions{AllowMissingExpiration: true})(
		a.LimitMCP(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release })))
	first := make(chan int, 1)
	go func() {
		r := httptest.NewRequest("POST", "/mcp", nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		first <- w.Code
	}()
	waitInFlight(t, a, 1)
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second concurrent MCP call got %d, want 429", w.Code)
	}
	close(release)
	if c := <-first; c != 200 {
		t.Fatalf("first call finished with %d", c)
	}
}

func waitInFlight(t *testing.T, a *Authenticator, n int) {
	t.Helper()
	for range 200 {
		if a.InFlight() == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("in-flight calls never reached %d (now %d)", n, a.InFlight())
}

// A request whose headers arrived before the token was revoked must not run
// once its body completes: the token is checked only after the body is read.
func TestTokenCheckedAfterBodyIsRead(t *testing.T) {
	a, tok := setup(t)
	ran := false
	h := ReadBodyFirst(a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ran = true })))
	srv := httptest.NewServer(h)
	defer srv.Close()

	pr, pw := io.Pipe()
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/deploy", pr)
	req.Header.Set("Authorization", "Bearer "+tok)
	done := make(chan *http.Response, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			done <- nil
			return
		}
		done <- resp
	}()
	pw.Write([]byte(`{"app":`)) // headers and part of the body are on the wire
	time.Sleep(100 * time.Millisecond)
	tk, _, err := a.Store.TokenByHash(context.Background(), auth.HashToken(tok))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Store.RevokeToken(context.Background(), tk.ID); err != nil {
		t.Fatal(err)
	}
	pw.Write([]byte(`"shop"}`))
	pw.Close()
	resp := <-done
	if resp == nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || ran {
		t.Fatalf("revoked token: status %d, handler ran=%v; want 401 and no run", resp.StatusCode, ran)
	}
}

func TestReadBodyFirstRejectsOversizeBody(t *testing.T) {
	h := ReadBodyFirst(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("handler must not run") }))
	r := httptest.NewRequest("POST", "/api/v1/validate", strings.NewReader(strings.Repeat("x", MaxBodyBytes+1)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("got %d, want 413", w.Code)
	}
}

func TestRateLimitPerToken(t *testing.T) {
	a, tok := setup(t)
	ws, err := a.Store.GetWorkspaceByName(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	other, hash, prefix, _ := auth.GenerateToken()
	if _, err := a.Store.CreateToken(context.Background(), ws.ID, "other", hash, prefix); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000_000, 0)
	a.Now = func() time.Time { return now }
	a.RateBurst, a.RatePerSecond = 3, 1
	h := a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	call := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/whoami", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for i := range 3 {
		if w := call(tok); w.Code != 200 {
			t.Fatalf("call %d within burst: %d", i, w.Code)
		}
	}
	if w := call(tok); w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "too_many_requests") {
		t.Fatalf("call beyond burst: %d %q", w.Code, w.Body.String())
	}
	if w := call(other); w.Code != 200 {
		t.Fatalf("another token must have its own bucket: %d", w.Code)
	}
	now = now.Add(2 * time.Second)
	if w := call(tok); w.Code != 200 {
		t.Fatalf("bucket must refill over time: %d", w.Code)
	}
}

func TestInternalAuthErrorsAreLogged(t *testing.T) {
	a, tok := setup(t)
	var buf bytes.Buffer
	a.Logger = slog.New(slog.NewTextHandler(&buf, nil))
	a.Store.Close() // every lookup now fails
	h := a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	r := httptest.NewRequest("POST", "/api/v1/whoami", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusInternalServerError || !strings.Contains(buf.String(), "level=ERROR") || !strings.Contains(buf.String(), "agent auth error") {
		t.Fatalf("status %d, log %q", w.Code, buf.String())
	}
	if strings.Contains(buf.String(), tok) {
		t.Fatal("token logged")
	}
}
