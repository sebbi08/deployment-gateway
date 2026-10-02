package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sebmahr/deploy-gateway/internal/tmpl"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newWorkspace(t *testing.T, s *Store, name string, start, end int) Workspace {
	t.Helper()
	w, err := s.CreateWorkspace(context.Background(), Workspace{
		Name: name, Quota: BuiltinDefaultQuota(), PortRangeStart: start, PortRangeEnd: end, PortBindIP: "0.0.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestQuotaValidate(t *testing.T) {
	if err := BuiltinDefaultQuota().Validate(); err != nil {
		t.Fatalf("builtin default invalid: %v", err)
	}
	q := BuiltinDefaultQuota()
	q.DefaultMemoryMB = q.MaxMemoryMB + 1
	if err := q.Validate(); err == nil {
		t.Fatal("default above max must be rejected")
	}
	q = BuiltinDefaultQuota()
	q.MaxApps = 0
	if err := q.Validate(); err == nil {
		t.Fatal("max_apps 0 must be rejected")
	}
}

func TestWorkspaceCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 20000, 20099)
	if w.ID == 0 || w.CreatedAt.IsZero() {
		t.Fatalf("not populated: %+v", w)
	}
	if _, err := s.CreateWorkspace(ctx, Workspace{Name: "alice", Quota: BuiltinDefaultQuota(), PortBindIP: "0.0.0.0"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	w.Quota.MaxApps = 9
	w.PortBindIP = "127.0.0.1"
	if err := s.UpdateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetWorkspaceByName(ctx, "alice")
	if err != nil || got.Quota.MaxApps != 9 || got.PortBindIP != "127.0.0.1" || got.PortRangeEnd != 20099 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := s.GetWorkspace(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	list, _ := s.ListWorkspaces(ctx)
	if len(list) != 1 {
		t.Fatalf("list = %v", list)
	}
	if err := s.DeleteWorkspace(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetWorkspace(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestOverlappingWorkspace(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a := newWorkspace(t, s, "alice", 20000, 20099)
	newWorkspace(t, s, "nopublish", 0, 0)
	cases := []struct {
		start, end int
		exclude    int64
		want       string
	}{
		{20050, 20150, 0, "alice"},
		{19900, 20000, 0, "alice"},
		{20100, 20199, 0, ""},
		{20050, 20150, a.ID, ""},
	}
	for _, c := range cases {
		got, err := s.OverlappingWorkspace(ctx, c.start, c.end, c.exclude)
		if err != nil || got != c.want {
			t.Errorf("%d-%d excl %d: got %q, %v", c.start, c.end, c.exclude, got, err)
		}
	}
}

func TestTokens(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 0, 0)
	tok, err := s.CreateToken(ctx, w.ID, "ci", "hash1", "dgw_abcdefgh")
	if err != nil {
		t.Fatal(err)
	}
	got, ws, err := s.TokenByHash(ctx, "hash1")
	if err != nil || got.ID != tok.ID || ws.Name != "alice" {
		t.Fatalf("TokenByHash = %+v %+v %v", got, ws, err)
	}
	if err := s.TouchToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetToken(ctx, tok.ID); got.LastUsedAt == nil {
		t.Fatal("last_used_at not set")
	}
	if err := s.RevokeToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.TokenByHash(ctx, "hash1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token still valid: %v", err)
	}
	list, _ := s.ListTokens(ctx, w.ID)
	if len(list) != 1 || list[0].RevokedAt == nil {
		t.Fatalf("ListTokens = %+v", list)
	}
	if err := s.DeleteWorkspace(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetToken(ctx, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("tokens must be deleted with their workspace")
	}
}

func TestTemplatesAndGrants(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 0, 0)
	tp := tmpl.Template{Name: "public-web", Description: "d", HostPattern: "{{app}}.{{workspace}}.a.example.com",
		Networks: []string{"apps"}, Labels: map[string]string{"traefik.enable": "true"}}
	id, err := s.CreateTemplate(ctx, tp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTemplate(ctx, tp); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate template: %v", err)
	}
	internal, _ := s.CreateTemplate(ctx, tmpl.Template{Name: "internal", Networks: []string{"apps"}})
	tp.Description = "updated"
	if err := s.UpdateTemplate(ctx, id, tp); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTemplate(ctx, id)
	if err != nil || got.Description != "updated" || got.Labels["traefik.enable"] != "true" || got.Networks[0] != "apps" {
		t.Fatalf("GetTemplate = %+v, %v", got, err)
	}
	if err := s.SetGrants(ctx, w.ID, []int64{id, internal}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGrants(ctx, w.ID, []int64{id}); err != nil {
		t.Fatal(err)
	}
	granted, _ := s.GrantedTemplates(ctx, w.ID)
	if len(granted) != 1 || granted[0].Name != "public-web" {
		t.Fatalf("granted = %+v", granted)
	}
	if err := s.DeleteTemplate(ctx, id); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete granted template: %v", err)
	}
	if err := s.DeleteTemplate(ctx, internal); err != nil {
		t.Fatal(err)
	}
	all, _ := s.ListTemplates(ctx)
	if len(all) != 1 {
		t.Fatalf("templates = %+v", all)
	}
}

func TestRevokeWorkspaceTokens(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	alice := newWorkspace(t, s, "alice", 0, 0)
	bob := newWorkspace(t, s, "bob", 0, 0)
	for _, h := range []string{"a1", "a2"} {
		if _, err := s.CreateToken(ctx, alice.ID, h, h, "dgw_"+h); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateToken(ctx, bob.ID, "b1", "b1", "dgw_b1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeWorkspaceTokens(ctx, alice.ID); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"a1", "a2"} {
		if _, _, err := s.TokenByHash(ctx, h); !errors.Is(err, ErrNotFound) {
			t.Fatalf("token %s still valid: %v", h, err)
		}
	}
	if _, _, err := s.TokenByHash(ctx, "b1"); err != nil {
		t.Fatalf("other workspace's token revoked: %v", err)
	}
	// Idempotent, also for workspaces without active tokens.
	if err := s.RevokeWorkspaceTokens(ctx, alice.ID); err != nil {
		t.Fatal(err)
	}
}
