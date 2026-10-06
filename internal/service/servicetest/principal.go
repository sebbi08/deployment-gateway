package servicetest

import (
	"context"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/auth"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

// Principal issues a real token for workspace name and returns the
// principal agent authentication would produce for it.
func Principal(t testing.TB, svc *service.Service, st *store.Store, name, clientIP string) service.Principal {
	t.Helper()
	ctx := context.Background()
	w, err := st.GetWorkspaceByName(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svc.CreateToken(ctx, "test", w, "test")
	if err != nil {
		t.Fatal(err)
	}
	hash := auth.HashToken(tok)
	tk, ws, err := st.TokenByHash(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	return service.Principal{Workspace: ws, TokenID: tk.ID, TokenHash: hash, ClientIP: clientIP}
}
