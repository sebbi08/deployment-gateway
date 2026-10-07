package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSaveDeploymentAndUsage(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 20000, 20099)
	a, err := s.SaveDeployment(ctx, w.ID, "shop", 256<<20, 0.5, map[string]string{"web": "shop.alice.example.com"},
		[]PortClaim{{Protocol: "tcp", HostPort: 20001, ContainerPort: 8080, Service: "web"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 || a.Hosts["web"] != "shop.alice.example.com" || a.MemoryBytes != 256<<20 {
		t.Fatalf("app = %+v", a)
	}
	if _, err := s.SaveDeployment(ctx, w.ID, "blog", 128<<20, 0.25, nil, nil); err != nil {
		t.Fatal(err)
	}
	u, err := s.WorkspaceUsage(ctx, w.ID, "")
	if err != nil || u.Apps != 2 || u.MemoryBytes != 384<<20 || u.CPUs != 0.75 {
		t.Fatalf("usage = %+v, %v", u, err)
	}
	u, _ = s.WorkspaceUsage(ctx, w.ID, "shop")
	if u.Apps != 1 || u.MemoryBytes != 128<<20 {
		t.Fatalf("usage excluding shop = %+v", u)
	}
	claims, _ := s.ListPortClaims(ctx, w.ID)
	if len(claims) != 1 || claims[0].App != "shop" || claims[0].HostPort != 20001 {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestPortClaimConflicts(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 20000, 20099)
	claim := PortClaim{Protocol: "tcp", HostPort: 20001, ContainerPort: 80, Service: "web"}
	if _, err := s.SaveDeployment(ctx, w.ID, "shop", 1, 0.1, nil, []PortClaim{claim}); err != nil {
		t.Fatal(err)
	}
	// Same app may re-claim its own port (redeploy).
	if _, err := s.SaveDeployment(ctx, w.ID, "shop", 2, 0.1, nil, []PortClaim{claim}); err != nil {
		t.Fatalf("redeploy own port: %v", err)
	}
	conflicts, err := s.PortConflicts(ctx, w.ID, "blog", []PortClaim{claim, {Protocol: "udp", HostPort: 20001}})
	if err != nil || len(conflicts) != 1 || conflicts[0].App != "shop" {
		t.Fatalf("conflicts = %+v, %v", conflicts, err)
	}
	_, err = s.SaveDeployment(ctx, w.ID, "blog", 1, 0.1, nil, []PortClaim{claim})
	var taken *PortTakenError
	if !errors.As(err, &taken) || taken.Claim.App != "shop" {
		t.Fatalf("expected PortTakenError, got %v", err)
	}
	if _, err := s.GetApp(ctx, w.ID, "blog"); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed SaveDeployment must not leave an app row behind")
	}
	// Redeploy shop without the port releases it.
	if _, err := s.SaveDeployment(ctx, w.ID, "shop", 1, 0.1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveDeployment(ctx, w.ID, "blog", 1, 0.1, nil, []PortClaim{claim}); err != nil {
		t.Fatalf("port should be free now: %v", err)
	}
}

func TestRecordDeployDeleteAndKeys(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 20000, 20099)
	a, _ := s.SaveDeployment(ctx, w.ID, "shop", 1, 0.1, nil, []PortClaim{{Protocol: "tcp", HostPort: 20001, ContainerPort: 1, Service: "web"}})
	if err := s.RecordDeploy(ctx, a.ID, "ok"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetApp(ctx, w.ID, "shop")
	if got.LastDeployResult != "ok" || got.LastDeployAt == nil {
		t.Fatalf("app = %+v", got)
	}
	keys, _ := s.AppKeys(ctx)
	if !keys["alice/shop"] {
		t.Fatalf("keys = %v", keys)
	}
	if err := s.DeleteApp(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if claims, _ := s.ListPortClaims(ctx, w.ID); len(claims) != 0 {
		t.Fatal("claims must be deleted with the app")
	}
	if apps, _ := s.ListApps(ctx, w.ID); len(apps) != 0 {
		t.Fatalf("apps = %+v", apps)
	}
}

func TestAudit(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	old := time.Now().Add(-100 * 24 * time.Hour)
	entries := []AuditEntry{
		{Time: old, ActorType: "agent", Actor: "token:1", Workspace: "alice", TokenID: 1, Action: "deploy", Params: "{}", Result: "ok", Severity: "info"},
		{Time: time.Now(), ActorType: "agent", Actor: "token:1", Workspace: "alice", TokenID: 1, Action: "deploy", Params: "{}", Result: "security_violation", Severity: "critical"},
		{Time: time.Now(), ActorType: "admin", Actor: "admin", Workspace: "bob", Action: "create_workspace", Params: "{}", Result: "ok", Severity: "info"},
	}
	for _, e := range entries {
		if err := s.InsertAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	crit, _ := s.ListAudit(ctx, AuditFilter{Severity: "critical"})
	if len(crit) != 1 || crit[0].Result != "security_violation" {
		t.Fatalf("critical = %+v", crit)
	}
	alice, _ := s.ListAudit(ctx, AuditFilter{Workspace: "alice", Limit: 1})
	if len(alice) != 1 || alice[0].Severity != "critical" {
		t.Fatalf("newest-first limit failed: %+v", alice)
	}
	page2, _ := s.ListAudit(ctx, AuditFilter{Workspace: "alice", BeforeID: alice[0].ID})
	if len(page2) != 1 || page2[0].Result != "ok" {
		t.Fatalf("paging failed: %+v", page2)
	}
	n, err := s.PruneAudit(ctx, time.Now().Add(-90*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	q, err := s.DefaultQuota(ctx)
	if err != nil || q != BuiltinDefaultQuota() {
		t.Fatalf("default quota = %+v, %v", q, err)
	}
	q.MaxApps = 7
	if err := s.SetDefaultQuota(ctx, q); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.DefaultQuota(ctx); got.MaxApps != 7 {
		t.Fatalf("got %+v", got)
	}
	if d, _ := s.AuditRetentionDays(ctx); d != 90 {
		t.Fatalf("retention default = %d", d)
	}
	if err := s.SetAuditRetentionDays(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.AuditRetentionDays(ctx); d != 30 {
		t.Fatalf("retention = %d", d)
	}
}

// whoami lists the port claims of the caller's workspace only.
func TestListPortClaimsIsScopedToWorkspace(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	alice := newWorkspace(t, s, "alice", 20000, 20099)
	bob := newWorkspace(t, s, "bob", 20100, 20199)
	if _, err := s.SaveDeployment(ctx, alice.ID, "shop", 1, 0.1, nil, []PortClaim{{Protocol: "tcp", HostPort: 20001, ContainerPort: 80, Service: "web"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveDeployment(ctx, bob.ID, "blog", 1, 0.1, nil, []PortClaim{{Protocol: "tcp", HostPort: 20101, ContainerPort: 80, Service: "web"}}); err != nil {
		t.Fatal(err)
	}
	claims, err := s.ListPortClaims(ctx, alice.ID)
	if err != nil || len(claims) != 1 || claims[0].App != "shop" {
		t.Fatalf("alice's claims = %+v, %v", claims, err)
	}
}

// The agent list shows the caller's apps only.
func TestListAppsIsScopedToWorkspace(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	alice := newWorkspace(t, s, "alice", 20000, 20099)
	bob := newWorkspace(t, s, "bob", 20100, 20199)
	for _, a := range []struct {
		ws   int64
		name string
	}{{alice.ID, "shop"}, {bob.ID, "blog"}, {bob.ID, "shop"}} {
		if _, err := s.SaveDeployment(ctx, a.ws, a.name, 1, 0.1, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	apps, err := s.ListApps(ctx, alice.ID)
	if err != nil || len(apps) != 1 || apps[0].Name != "shop" || apps[0].WorkspaceID != alice.ID {
		t.Fatalf("alice's apps = %+v, %v", apps, err)
	}
}
