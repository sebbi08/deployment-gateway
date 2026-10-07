package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/auth"
	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/store"
	"github.com/sebbi08/deployment-gateway/internal/tmpl"
)

func TestWorkspacePortRules(t *testing.T) {
	h := newHarness(t)
	h.workspace("alice", 20000, 20099)
	q := store.BuiltinDefaultQuota()
	if _, err := h.svc.CreateWorkspace(h.ctx, "admin", "bob", q, service.PortSettings{Start: 20050, End: 20150, BindIP: "0.0.0.0"}); code(err) != service.CodeValidation {
		t.Fatalf("overlap: %v", err)
	}
	if _, err := h.svc.CreateWorkspace(h.ctx, "admin", "bob", q, service.PortSettings{Start: 80, End: 90, BindIP: "0.0.0.0"}); code(err) != service.CodeValidation {
		t.Fatalf("privileged ports: %v", err)
	}
	if _, err := h.svc.CreateWorkspace(h.ctx, "admin", "bob", q, service.PortSettings{BindIP: "not-an-ip"}); code(err) != service.CodeValidation {
		t.Fatalf("bad bind ip: %v", err)
	}
	if _, err := h.svc.CreateWorkspace(h.ctx, "admin", "Bad-Name", q, service.PortSettings{BindIP: "0.0.0.0"}); code(err) != service.CodeValidation {
		t.Fatalf("bad name: %v", err)
	}
}

func TestUpdateWorkspaceRejectsRangeExcludingUsedPorts(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	w := p.Workspace
	w.PortRangeStart, w.PortRangeEnd = 20050, 20099
	if err := h.svc.UpdateWorkspace(h.ctx, "admin", w); code(err) != service.CodeValidation {
		t.Fatalf("expected validation error, got %v", err)
	}
	w.PortRangeStart, w.PortRangeEnd = 20000, 20000
	if err := h.svc.UpdateWorkspace(h.ctx, "admin", w); code(err) != service.CodeValidation {
		t.Fatalf("range ending below a used port: expected validation error, got %v", err)
	}
	w.PortRangeStart, w.PortRangeEnd = 20000, 20010
	if err := h.svc.UpdateWorkspace(h.ctx, "admin", w); err != nil {
		t.Fatalf("range still covering 20001 must be accepted: %v", err)
	}
}

// The admin handlers pass quotas and port ranges straight through, so the
// service refuses invalid ones on create and update.
func TestWorkspaceQuotaAndPortRangeValidation(t *testing.T) {
	h := newHarness(t)
	bad := store.BuiltinDefaultQuota()
	bad.MaxApps = 0
	if _, err := h.svc.CreateWorkspace(h.ctx, "admin", "bob", bad, service.PortSettings{BindIP: "0.0.0.0"}); code(err) != service.CodeValidation {
		t.Errorf("create with invalid quota: %v", err)
	}
	for _, r := range [][2]int{{20000, 65536}, {20100, 20000}} {
		ps := service.PortSettings{Start: r[0], End: r[1], BindIP: "0.0.0.0"}
		if _, err := h.svc.CreateWorkspace(h.ctx, "admin", "bob", store.BuiltinDefaultQuota(), ps); code(err) != service.CodeValidation {
			t.Errorf("create with port range %d-%d: %v", r[0], r[1], err)
		}
	}
	p := h.workspace("alice", 20000, 20099)
	w := p.Workspace
	w.Quota = bad
	if err := h.svc.UpdateWorkspace(h.ctx, "admin", w); code(err) != service.CodeValidation {
		t.Errorf("update with invalid quota: %v", err)
	}
	if got, _ := h.st.GetWorkspace(h.ctx, w.ID); got.Quota.MaxApps != store.BuiltinDefaultQuota().MaxApps {
		t.Errorf("invalid quota stored: %+v", got.Quota)
	}
}

// DeleteWorkspace refuses a workspace whose name is not a valid workspace
// folder below the root, before revoking or removing anything.
func TestDeleteWorkspaceRefusesInvalidName(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	w := p.Workspace
	w.Name = ""
	if err := h.svc.DeleteWorkspace(h.ctx, "admin", w); err == nil {
		t.Fatal("delete with an empty name succeeded")
	}
	if _, err := os.Stat(filepath.Join(h.root, "alice")); err != nil {
		t.Fatalf("workspace folder removed: %v", err)
	}
	if _, err := h.svc.Whoami(h.ctx, p); err != nil {
		t.Fatalf("workspace or token touched: %v", err)
	}
}

func TestDeleteWorkspace(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	if err := h.svc.DeleteWorkspace(h.ctx, "admin", p.Workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "alice")); !os.IsNotExist(err) {
		t.Fatal("workspace folder not removed")
	}
	if len(h.docker.Containers) != 0 {
		t.Fatal("containers left behind")
	}
	if _, err := h.st.GetWorkspaceByName(h.ctx, "alice"); err == nil {
		t.Fatal("workspace row still present")
	}
}

func TestTokens(t *testing.T) {
	h := newHarness(t)
	alice := h.workspace("alice", 0, 0)
	bob := h.workspace("bob", 0, 0)
	tok, err := h.svc.CreateToken(h.ctx, "admin", alice.Workspace, "ci")
	if err != nil {
		t.Fatal(err)
	}
	got, ws, err := h.st.TokenByHash(h.ctx, auth.HashToken(tok))
	if err != nil || ws.Name != "alice" {
		t.Fatalf("token lookup: %v %v", ws, err)
	}
	if err := h.svc.RevokeToken(h.ctx, "admin", bob.Workspace, got.ID); code(err) != service.CodeNotFound {
		t.Fatalf("revoking another workspace's token: %v", err)
	}
	if err := h.svc.RevokeToken(h.ctx, "admin", alice.Workspace, got.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTemplateAdmin(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.SaveTemplate(h.ctx, "admin", 0, tmpl.Template{Name: "lan", Networks: []string{"missing-net"}})
	if code(err) != service.CodeValidation {
		t.Fatalf("missing network: %v", err)
	}
	_, err = h.svc.SaveTemplate(h.ctx, "admin", 0, tmpl.Template{Name: "dup", HostPattern: "{{app}}.{{workspace}}.apps.example.com", Networks: []string{"apps"}})
	if code(err) != service.CodeValidation {
		t.Fatalf("overlapping suffix: %v", err)
	}
	h.workspace("alice", 0, 0) // grants public-web
	if err := h.svc.DeleteTemplate(h.ctx, "admin", h.tplID); code(err) != service.CodeValidation {
		t.Fatalf("delete granted template: %v", err)
	}
}

func TestOrphans(t *testing.T) {
	h := newHarness(t)
	labels := map[string]string{"deploy-gateway.workspace": "ghost", "deploy-gateway.app": "x", "deploy-gateway.managed": "true"}
	h.docker.Add(dockerx.ContainerInfo{ID: "ghost-x-web", Name: "ghost-x-web", Labels: labels}, dockerx.Facts{})
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	orphans, err := h.svc.Orphans(h.ctx)
	if err != nil || len(orphans) != 1 || orphans[0].ID != "ghost-x-web" {
		t.Fatalf("orphans = %+v, %v", orphans, err)
	}
	if err := h.svc.RemoveOrphan(h.ctx, "admin", "alice-shop-web"); code(err) != service.CodeNotFound {
		t.Fatalf("removing a non-orphan must fail: %v", err)
	}
	if err := h.svc.RemoveOrphan(h.ctx, "admin", "ghost-x-web"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.docker.Containers["ghost-x-web"]; ok {
		t.Fatal("orphan not removed")
	}
}

func TestUpdateSettings(t *testing.T) {
	h := newHarness(t)
	q := store.BuiltinDefaultQuota()
	q.MaxApps = 2
	if err := h.svc.UpdateSettings(h.ctx, "admin", q, 30); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.st.DefaultQuota(h.ctx); got.MaxApps != 2 {
		t.Fatalf("default quota = %+v", got)
	}
	q.MaxApps = 0
	if err := h.svc.UpdateSettings(h.ctx, "admin", q, 30); code(err) != service.CodeValidation {
		t.Fatalf("invalid quota: %v", err)
	}
}

func TestDeleteWorkspaceRevokesTokensFirstAndRemovesLeftovers(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	tok, err := h.svc.CreateToken(h.ctx, "admin", p.Workspace, "ci")
	if err != nil {
		t.Fatal(err)
	}
	// A labelled container of this workspace that belongs to no known app.
	labels := map[string]string{"deploy-gateway.workspace": "alice", "deploy-gateway.app": "ghost", "deploy-gateway.managed": "true"}
	h.docker.Add(dockerx.ContainerInfo{ID: "alice-ghost-web", Name: "alice-ghost-web", Labels: labels}, dockerx.Facts{})
	other := map[string]string{"deploy-gateway.workspace": "bob", "deploy-gateway.app": "x", "deploy-gateway.managed": "true"}
	h.docker.Add(dockerx.ContainerInfo{ID: "bob-x-web", Name: "bob-x-web", Labels: other}, dockerx.Facts{})
	revokedDuringDelete := false
	h.compose.OnDown = func(string) {
		_, _, err := h.st.TokenByHash(h.ctx, auth.HashToken(tok))
		revokedDuringDelete = err != nil
	}
	if err := h.svc.DeleteWorkspace(h.ctx, "admin", p.Workspace); err != nil {
		t.Fatal(err)
	}
	if !revokedDuringDelete {
		t.Fatal("tokens must be revoked before apps are removed")
	}
	if _, _, err := h.st.TokenByHash(h.ctx, auth.HashToken(tok)); err == nil {
		t.Fatal("token still valid")
	}
	if _, ok := h.docker.Containers["alice-ghost-web"]; ok {
		t.Fatal("leftover workspace container not removed")
	}
	if _, ok := h.docker.Containers["bob-x-web"]; !ok {
		t.Fatal("another workspace's container was removed")
	}
}

func TestDeleteWorkspaceIsNotCancelledByRequest(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	ctx, cancel := context.WithCancel(h.ctx)
	h.compose.OnDown = func(string) { cancel() }
	if err := h.svc.DeleteWorkspace(ctx, "admin", p.Workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.GetWorkspaceByName(h.ctx, "alice"); err == nil {
		t.Fatal("workspace row still present")
	}
}

func TestDeployIntoDeletedWorkspaceIsNotFound(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099) // principal authenticated before the delete
	if err := h.svc.DeleteWorkspace(h.ctx, "admin", p.Workspace); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if code(err) != service.CodeNotFound {
		t.Fatalf("expected not_found, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "alice")); !os.IsNotExist(err) {
		t.Fatal("deploy recreated the deleted workspace folder")
	}
	if len(h.compose.Calls()) != 0 {
		t.Fatalf("compose ran: %v", h.compose.Calls())
	}
}

func TestWorkspaceBindIPIsCanonical(t *testing.T) {
	h := newHarness(t)
	w, err := h.svc.CreateWorkspace(h.ctx, "admin", "alice", store.BuiltinDefaultQuota(), service.PortSettings{BindIP: "::ffff:127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := h.st.GetWorkspace(h.ctx, w.ID); got.PortBindIP != "127.0.0.1" {
		t.Fatalf("created bind IP = %q", got.PortBindIP)
	}
	w.PortBindIP = "::ffff:192.168.1.10"
	if err := h.svc.UpdateWorkspace(h.ctx, "admin", w); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.st.GetWorkspace(h.ctx, w.ID); got.PortBindIP != "192.168.1.10" {
		t.Fatalf("updated bind IP = %q", got.PortBindIP)
	}
}

// Running apps keep their published ports on the old bind IP until they are
// redeployed, so a bind IP change is refused while ports are claimed.
func TestUpdateWorkspaceRefusesBindIPChangeWithPublishedPorts(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	w, _ := h.st.GetWorkspace(h.ctx, p.Workspace.ID)
	w.PortBindIP = "127.0.0.1"
	err := h.svc.UpdateWorkspace(h.ctx, "admin", w)
	if code(err) != service.CodeValidation || !strings.Contains(err.Error(), "shop") {
		t.Fatalf("bind IP change with published ports: %v", err)
	}
	w.PortBindIP = "0.0.0.0"
	w.Quota.MaxApps = 4
	if err := h.svc.UpdateWorkspace(h.ctx, "admin", w); err != nil {
		t.Fatalf("unchanged bind IP must still allow other edits: %v", err)
	}
	if _, err := h.svc.Delete(h.ctx, p, service.DeleteInput{App: "shop", Confirm: "shop"}); err != nil {
		t.Fatal(err)
	}
	w.PortBindIP = "127.0.0.1"
	if err := h.svc.UpdateWorkspace(h.ctx, "admin", w); err != nil {
		t.Fatalf("bind IP change without published ports: %v", err)
	}
}

// Hosts are unique by construction only for the current template patterns.
// Changing the pattern of a template, or deleting it, while apps still hold
// hosts under its suffix lets another workspace claim a running app's host.
func TestTemplateHostChangesRefusedWhileAppsHoldHosts(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	stored, err := h.st.GetTemplate(h.ctx, h.tplID)
	if err != nil {
		t.Fatal(err)
	}
	reordered := stored.Template
	reordered.HostPattern = "{{workspace}}.{{app}}.apps.example.com"
	if _, err := h.svc.SaveTemplate(h.ctx, "admin", h.tplID, reordered); code(err) != service.CodeValidation || !strings.Contains(err.Error(), "alice/shop") {
		t.Fatalf("pattern change with deployed hosts: %v", err)
	}
	same := stored.Template
	same.Description = "edited"
	if _, err := h.svc.SaveTemplate(h.ctx, "admin", h.tplID, same); err != nil {
		t.Fatalf("edit without a pattern change must be allowed: %v", err)
	}
	if err := h.svc.SetGrants(h.ctx, "admin", p.Workspace, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.DeleteTemplate(h.ctx, "admin", h.tplID); code(err) != service.CodeValidation || !strings.Contains(err.Error(), "alice/shop") {
		t.Fatalf("delete with deployed hosts: %v", err)
	}
	if _, err := h.svc.Delete(h.ctx, p, service.DeleteInput{App: "shop", Confirm: "shop"}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.DeleteTemplate(h.ctx, "admin", h.tplID); err != nil {
		t.Fatalf("delete after the app is gone: %v", err)
	}
}

func TestSetGrantsValidatesTemplateIDs(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	if err := h.svc.SetGrants(h.ctx, "admin", p.Workspace, []int64{h.tplID, h.tplID}); err != nil {
		t.Fatalf("duplicate IDs must be accepted once: %v", err)
	}
	if err := h.svc.SetGrants(h.ctx, "admin", p.Workspace, []int64{h.tplID, 9999}); code(err) != service.CodeValidation {
		t.Fatalf("unknown template ID: %v", err)
	}
}

// Revoking an already revoked token (double click, stale view) succeeds.
func TestRevokeTokenIsIdempotent(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	for i := range 2 {
		if err := h.svc.RevokeToken(h.ctx, "admin", p.Workspace, p.TokenID); err != nil {
			t.Fatalf("revoke #%d: %v", i+1, err)
		}
	}
}
