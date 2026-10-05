package agentauth

import (
	"bytes"
	"context"
	"errors"
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
