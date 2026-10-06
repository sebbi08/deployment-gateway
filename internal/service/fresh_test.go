package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

// Workspace IDs are reused after the newest workspace is deleted. A request
// authenticated for the deleted workspace must not act on the new one.
func TestStalePrincipalCannotUseReusedWorkspaceID(t *testing.T) {
	h := newHarness(t)
	stale := h.workspace("alice", 20000, 20099)
	if err := h.svc.DeleteWorkspace(h.ctx, "admin", stale.Workspace); err != nil {
		t.Fatal(err)
	}
	bob := h.workspace("bob", 20000, 20099)
	if bob.Workspace.ID != stale.Workspace.ID {
		t.Skipf("workspace ID not reused (%d vs %d)", bob.Workspace.ID, stale.Workspace.ID)
	}
	deployExample(t, h, bob)
	if _, err := h.svc.Deploy(h.ctx, stale, service.DeployInput{App: "other", Stack: mustStack(t, exampleStack)}); code(err) != service.CodeNotFound {
		t.Errorf("deploy with stale principal: %v", err)
	}
	if res, err := h.svc.List(h.ctx, stale); code(err) != service.CodeNotFound {
		t.Errorf("list with stale principal leaked %+v (err %v)", res, err)
	}
	if _, err := h.svc.Delete(h.ctx, stale, service.DeleteInput{App: "shop", Confirm: "shop"}); code(err) != service.CodeNotFound {
		t.Errorf("delete with stale principal: %v", err)
	}
	if _, err := h.st.GetApp(h.ctx, bob.Workspace.ID, "shop"); err != nil {
		t.Errorf("bob's app was touched: %v", err)
	}
}

func TestRevokedTokenPrincipalIsRejected(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	if err := h.svc.RevokeToken(h.ctx, "admin", p.Workspace, p.TokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Whoami(h.ctx, p); code(err) != service.CodeUnauthorized {
		t.Fatalf("whoami with revoked token: %v", err)
	}
}

// A token revoked while a deploy pulls images must not commit the deploy.
func TestTokenRevokedDuringPullStopsDeploy(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.compose.ObserveCtx = func(op string, _ context.Context) {
		if op == "pull" {
			h.svc.RevokeToken(h.ctx, "admin", p.Workspace, p.TokenID)
		}
	}
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)}); code(err) != service.CodeUnauthorized {
		t.Fatalf("deploy after revocation during pull: %v", err)
	}
	if _, err := h.st.GetApp(h.ctx, p.Workspace.ID, "shop"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deploy was committed: %v", err)
	}
}

// Quota, port range and bind IP come from the current workspace row, not
// from the copy taken at authentication (LO9).
func TestPlanUsesCurrentWorkspaceRow(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	w := p.Workspace
	w.Quota.MaxTotalMemoryMB, w.Quota.MaxMemoryMB, w.Quota.DefaultMemoryMB = 64, 64, 64
	if err := h.svc.UpdateWorkspace(h.ctx, "admin", w); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Validate(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)}); code(err) != service.CodeQuota {
		t.Fatalf("validate with the auth-time quota: %v", err)
	}
}
