package admin

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/wsfiles"
)

func TestWorkspaceFiles(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	if w := e.do("POST", "/api/admin/workspaces", workspaceBody("alice"), c); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	app := filepath.Join(e.root, "alice", "shop")
	if err := os.MkdirAll(filepath.Join(app, "data", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(app, "data", "web", "evil")); err != nil {
		t.Fatal(err)
	}

	w := e.do("GET", "/api/admin/workspaces/alice/files", nil, c)
	if l := decode[wsfiles.Listing](t, w); w.Code != 200 || len(l.Entries) != 1 || l.Entries[0].Name != "shop" {
		t.Fatalf("root: %d %s", w.Code, w.Body.String())
	}
	w = e.do("GET", "/api/admin/workspaces/alice/files?path=shop/data/web", nil, c)
	if l := decode[wsfiles.Listing](t, w); w.Code != 200 || len(l.Entries) != 1 || l.Entries[0].Target != "/etc/passwd" {
		t.Fatalf("data: %d %s", w.Code, w.Body.String())
	}
	w = e.do("GET", "/api/admin/workspaces/alice/files/content?path=shop/docker-compose.yml", nil, c)
	if fc := decode[wsfiles.Content](t, w); w.Code != 200 || fc.Text != "services: {}\n" {
		t.Fatalf("content: %d %s", w.Code, w.Body.String())
	}

	for _, tc := range []struct {
		path string
		code int
	}{
		{"/api/admin/workspaces/alice/files/content?path=shop/data/web/evil", http.StatusUnprocessableEntity},
		{"/api/admin/workspaces/alice/files/content?path=../alice/shop/docker-compose.yml", http.StatusUnprocessableEntity},
		{"/api/admin/workspaces/alice/files?path=shop/docker-compose.yml", http.StatusUnprocessableEntity},
		{"/api/admin/workspaces/alice/files/content?path=shop/data", http.StatusUnprocessableEntity},
		{"/api/admin/workspaces/alice/files?path=nope", http.StatusNotFound},
		{"/api/admin/workspaces/bob/files", http.StatusNotFound},
		{"/api/admin/workspaces/bob/files/content?path=x", http.StatusNotFound},
	} {
		if w := e.do("GET", tc.path, nil, c); w.Code != tc.code {
			t.Errorf("%s: %d %s, want %d", tc.path, w.Code, w.Body.String(), tc.code)
		}
	}
	if w := e.do("GET", "/api/admin/workspaces/alice/files", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", w.Code)
	}
}
