package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sebmahr/deploy-gateway/internal/auth"
	"github.com/sebmahr/deploy-gateway/internal/dockerx"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/store"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
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
	w.PortRangeStart, w.PortRangeEnd = 20000, 20010
	if err := h.svc.UpdateWorkspace(h.ctx, "admin", w); err != nil {
		t.Fatalf("range still covering 20001 must be accepted: %v", err)
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
