package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

func TestTemplatesCRUDAndPreview(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	draft := publicWebTemplate()
	draft.HostPattern = "{{app}}.apps.example.com"
	w := e.do("POST", "/api/admin/templates/preview", map[string]any{"id": 0, "template": draft}, c)
	p := decode[previewResponse](t, w)
	if w.Code != 200 || p.Valid || len(p.Errors) == 0 || !strings.Contains(strings.Join(p.Errors, ";"), "{{workspace}}") {
		t.Fatalf("invalid preview: %d %+v", w.Code, p)
	}
	draft.HostPattern = "{{app}}.{{workspace}}.apps.example.com"
	p = decode[previewResponse](t, e.do("POST", "/api/admin/templates/preview", map[string]any{"id": 0, "template": draft}, c))
	if !p.Valid || p.Host != "shop.alice.apps.example.com" || p.Labels[1].Key != "traefik.http.routers.alice-shop-web.rule" {
		t.Fatalf("valid preview: %+v", p)
	}
	if all, _ := e.st.ListTemplates(context.Background()); len(all) != 0 {
		t.Fatal("preview must not save")
	}

	w = e.do("POST", "/api/admin/templates", draft, c)
	id := decode[map[string]int64](t, w)["id"]
	if w.Code != http.StatusCreated || id == 0 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	path := "/api/admin/templates/" + strconv.FormatInt(id, 10)
	if got := decode[templateDTO](t, e.do("GET", path, nil, c)); got.Name != "public-web" || got.Labels["traefik.enable"] != "true" {
		t.Fatalf("get = %+v", got)
	}
	// Previewing the stored template while editing must not report an overlap with itself.
	if p := decode[previewResponse](t, e.do("POST", "/api/admin/templates/preview", map[string]any{"id": id, "template": draft}, c)); !p.Valid {
		t.Fatalf("self overlap: %+v", p)
	}
	draft.Description = "updated"
	if got := decode[templateDTO](t, e.do("PUT", path, draft, c)); got.Description != "updated" {
		t.Fatalf("update = %+v", got)
	}
	if list := decode[[]templateDTO](t, e.do("GET", "/api/admin/templates", nil, c)); len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if w := e.do("DELETE", path, nil, c); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("GET", path, nil, c); w.Code != http.StatusNotFound {
		t.Fatalf("after delete: %d", w.Code)
	}
}

func TestTemplateSaveRejectsMissingNetwork(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	tpl := publicWebTemplate()
	tpl.Networks = []string{"nope"}
	w := e.do("POST", "/api/admin/templates", tpl, c)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "does not exist") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestAuditFiltersAndPaging(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	for _, n := range []string{"alice", "bob", "carol"} {
		body := workspaceBody(n)
		body["ports"] = map[string]any{"start": 0, "end": 0, "bind_ip": "0.0.0.0"}
		if w := e.do("POST", "/api/admin/workspaces", body, c); w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d", n, w.Code)
		}
	}
	type page struct {
		Entries    []auditDTO `json:"entries"`
		NextBefore int64      `json:"next_before"`
	}
	p1 := decode[page](t, e.do("GET", "/api/admin/audit?action=create_workspace&limit=2", nil, c))
	if len(p1.Entries) != 2 || p1.NextBefore == 0 || p1.Entries[0].Workspace != "carol" {
		t.Fatalf("page 1 = %+v", p1)
	}
	p2 := decode[page](t, e.do("GET", "/api/admin/audit?action=create_workspace&limit=2&before="+strconv.FormatInt(p1.NextBefore, 10), nil, c))
	if len(p2.Entries) != 1 || p2.NextBefore != 0 || p2.Entries[0].Workspace != "alice" {
		t.Fatalf("page 2 = %+v", p2)
	}
	if p := decode[page](t, e.do("GET", "/api/admin/audit?workspace=bob", nil, c)); len(p.Entries) != 1 {
		t.Fatalf("workspace filter = %+v", p)
	}
}

func TestSettingsAPI(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	got := decode[settingsBody](t, e.do("GET", "/api/admin/settings", nil, c))
	if got.AuditRetentionDays != 90 || got.DefaultQuota != store.BuiltinDefaultQuota() {
		t.Fatalf("defaults = %+v", got)
	}
	got.DefaultQuota.MaxApps = 7
	got.AuditRetentionDays = 30
	if saved := decode[settingsBody](t, e.do("PUT", "/api/admin/settings", got, c)); saved.DefaultQuota.MaxApps != 7 || saved.AuditRetentionDays != 30 {
		t.Fatalf("saved = %+v", saved)
	}
	got.AuditRetentionDays = 0
	if w := e.do("PUT", "/api/admin/settings", got, c); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid retention: %d", w.Code)
	}
}

func TestOrphansAPI(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	labels := map[string]string{"deploy-gateway.workspace": "ghost", "deploy-gateway.app": "x", "deploy-gateway.managed": "true"}
	e.docker.Add(dockerx.ContainerInfo{ID: "ghost-x-web", Name: "ghost-x-web", Labels: labels, State: "running"}, dockerx.Facts{})
	list := decode[[]orphanDTO](t, e.do("GET", "/api/admin/orphans", nil, c))
	if len(list) != 1 || list[0].Workspace != "ghost" {
		t.Fatalf("orphans = %+v", list)
	}
	if w := e.do("DELETE", "/api/admin/orphans/ghost-x-web", nil, c); w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("DELETE", "/api/admin/orphans/ghost-x-web", nil, c); w.Code != http.StatusNotFound {
		t.Fatalf("second remove: %d", w.Code)
	}
	if audit, _ := e.st.ListAudit(context.Background(), store.AuditFilter{Action: "remove_orphan"}); len(audit) != 2 {
		t.Fatalf("remove_orphan audit = %+v", audit)
	}
}

func TestUpdateMissingTemplateIsNotFound(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	w := e.do("PUT", "/api/admin/templates/999", publicWebTemplate(), c)
	if w.Code != http.StatusNotFound || decode[apiError](t, w).Code != "not_found" {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}
