package agentauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/sebmahr/deploy-gateway/internal/auth"
	"github.com/sebmahr/deploy-gateway/internal/store"
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
	if _, err := a.Verify(context.Background(), tok, "5.5.5.5"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("IP must be blocked after repeated failures, even with a valid token")
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
