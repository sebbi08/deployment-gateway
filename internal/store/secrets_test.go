package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestRequestAndSetSecret(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 20000, 20099)
	if err := s.RequestSecrets(ctx, w.ID, []string{"stripe-prod"}, "shop"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSecrets(ctx, w.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list[0].Set || list[0].RequestedBy != "shop" || list[0].RequestedAt == nil {
		t.Fatalf("secret = %+v", list[0])
	}
	if v, _ := s.SecretValues(ctx, w.ID); len(v) != 0 {
		t.Fatalf("values = %v", v)
	}
	if _, err := s.SetSecretValue(ctx, w.ID, "stripe-prod", "sk_live_x"); err != nil {
		t.Fatal(err)
	}
	list, _ = s.ListSecrets(ctx, w.ID)
	if !list[0].Set || list[0].RequestedAt != nil {
		t.Fatalf("secret = %+v", list[0])
	}
	if v, _ := s.SecretValues(ctx, w.ID); v["stripe-prod"] != "sk_live_x" {
		t.Fatalf("values = %v", v)
	}
	if _, err := s.SetSecretValue(ctx, w.ID, "nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := s.CreateSecret(ctx, w.ID, "stripe-prod", "y"); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v", err)
	}
}

func TestRequestSecretsCap(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 20000, 20099)
	for i := 0; i < MaxSecretsPerWorkspace; i++ {
		if err := s.CreateSecret(ctx, w.ID, fmt.Sprintf("s%d", i), "v"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RequestSecrets(ctx, w.ID, []string{"extra"}, "shop"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListSecrets(ctx, w.ID); len(list) != MaxSecretsPerWorkspace {
		t.Fatalf("len = %d", len(list))
	}
}

func TestSaveDeploymentRefs(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 20000, 20099)
	if err := s.CreateSecret(ctx, w.ID, "stripe-prod", "v1"); err != nil {
		t.Fatal(err)
	}
	refs := []SecretRefInput{{"web", "STRIPE_API_KEY", "stripe-prod"}}
	app, err := s.SaveDeployment(ctx, w.ID, "shop", 1, 0.1, nil, nil, refs)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.AppSecretRefs(ctx, app.ID)
	if err != nil || len(got) != 1 || got[0].Stale || got[0].Service != "web" || got[0].EnvKey != "STRIPE_API_KEY" || got[0].Secret != "stripe-prod" || got[0].App != "shop" {
		t.Fatalf("refs = %+v, %v", got, err)
	}
	changed, err := s.SetSecretValue(ctx, w.ID, "stripe-prod", "v2")
	if err != nil || len(changed) != 1 || changed[0].AppID != app.ID || changed[0].EnvKey != "STRIPE_API_KEY" {
		t.Fatalf("changed = %+v, %v", changed, err)
	}
	got, _ = s.AppSecretRefs(ctx, app.ID)
	if !got[0].Stale {
		t.Fatalf("not stale: %+v", got)
	}
	if _, err := s.SaveDeployment(ctx, w.ID, "shop", 1, 0.1, nil, nil, refs); err != nil {
		t.Fatal(err)
	}
	got, _ = s.AppSecretRefs(ctx, app.ID)
	if len(got) != 1 || got[0].Stale {
		t.Fatalf("stale not reset: %+v", got)
	}
	// stale again; ClearStale only touches the listed services
	s.SetSecretValue(ctx, w.ID, "stripe-prod", "v3")
	if err := s.ClearStale(ctx, app.ID, []string{"other"}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.AppSecretRefs(ctx, app.ID); !got[0].Stale {
		t.Fatal("ClearStale touched wrong service")
	}
	if err := s.ClearStale(ctx, app.ID, []string{"web"}); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.AppSecretRefs(ctx, app.ID); got[0].Stale {
		t.Fatal("ClearStale did not clear")
	}
	all, err := s.ListSecretRefs(ctx, w.ID)
	if err != nil || len(all) != 1 || all[0].App != "shop" {
		t.Fatalf("all = %+v, %v", all, err)
	}
	// a ref to a requested or missing secret fails and leaves refs unchanged
	if err := s.RequestSecrets(ctx, w.ID, []string{"pending"}, "shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveDeployment(ctx, w.ID, "shop", 1, 0.1, nil, nil, []SecretRefInput{{"web", "A", "pending"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.SaveDeployment(ctx, w.ID, "shop", 1, 0.1, nil, nil, []SecretRefInput{{"web", "A", "missing"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	got, _ = s.AppSecretRefs(ctx, app.ID)
	if len(got) != 1 || got[0].EnvKey != "STRIPE_API_KEY" {
		t.Fatalf("refs changed: %+v", got)
	}
}

func TestDeleteSecretInUse(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 20000, 20099)
	if err := s.CreateSecret(ctx, w.ID, "k", "v"); err != nil {
		t.Fatal(err)
	}
	app, err := s.SaveDeployment(ctx, w.ID, "shop", 1, 0.1, nil, nil, []SecretRefInput{{"web", "K", "k"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSecret(ctx, w.ID, "k"); !errors.Is(err, ErrInUse) {
		t.Fatalf("err = %v", err)
	}
	if err := s.DeleteApp(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSecret(ctx, w.ID, "k"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSecret(ctx, w.ID, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretApplyState(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 20000, 20099)
	app, _ := s.SaveDeployment(ctx, w.ID, "shop", 1, 0.1, nil, nil, nil)
	if err := s.SetSecretApply(ctx, app.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	refs, err := s.AppsWithApplyState(ctx, "running")
	if err != nil || len(refs) != 1 || refs[0].Workspace != "alice" || refs[0].App.Name != "shop" {
		t.Fatalf("refs = %+v, %v", refs, err)
	}
	a, _ := s.GetApp(ctx, w.ID, "shop")
	if a.SecretApplyState != "running" || a.SecretApplyAt == nil || a.SecretApplyError != "" {
		t.Fatalf("app = %+v", a)
	}
}

func TestMigrateAddsApplyColumns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE workspaces (id INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL,
		max_apps INTEGER NOT NULL, max_services_per_app INTEGER NOT NULL, max_total_memory_mb INTEGER NOT NULL, max_total_cpus REAL NOT NULL,
		default_memory_mb INTEGER NOT NULL, max_memory_mb INTEGER NOT NULL, default_cpus REAL NOT NULL, max_cpus REAL NOT NULL,
		default_pids INTEGER NOT NULL, max_pids INTEGER NOT NULL, port_range_start INTEGER NOT NULL DEFAULT 0,
		port_range_end INTEGER NOT NULL DEFAULT 0, port_bind_ip TEXT NOT NULL DEFAULT '0.0.0.0');
		CREATE TABLE apps (id INTEGER PRIMARY KEY, workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
		name TEXT NOT NULL, memory_bytes INTEGER NOT NULL DEFAULT 0, cpus REAL NOT NULL DEFAULT 0, hosts_json TEXT NOT NULL DEFAULT '{}',
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, last_deploy_at INTEGER, last_deploy_result TEXT NOT NULL DEFAULT '',
		UNIQUE (workspace_id, name));`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w := newWorkspace(t, s, "alice", 20000, 20099)
	app, err := s.SaveDeployment(ctx, w.ID, "shop", 1, 0.1, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSecretApply(ctx, app.ID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	a, _ := s.GetApp(ctx, w.ID, "shop")
	if a.SecretApplyState != "failed" || a.SecretApplyError != "boom" {
		t.Fatalf("app = %+v", a)
	}
}
