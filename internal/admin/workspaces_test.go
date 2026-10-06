package admin

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/service/servicetest"
	"github.com/sebbi08/deployment-gateway/internal/stack"
	"github.com/sebbi08/deployment-gateway/internal/store"
	"github.com/sebbi08/deployment-gateway/internal/tmpl"
)

func workspaceBody(name string) map[string]any {
	return map[string]any{
		"name":  name,
		"quota": store.BuiltinDefaultQuota(),
		"ports": service.PortSettings{Start: 20000, End: 20099, BindIP: "0.0.0.0"},
	}
}

func publicWebTemplate() tmpl.Template {
	return tmpl.Template{Name: "public-web", Description: "Public HTTPS", HostPattern: "{{app}}.{{workspace}}.apps.example.com",
		Networks: []string{"apps"}, Labels: map[string]string{"traefik.enable": "true", "traefik.http.routers.{{id}}.rule": "Host(`{{host}}`)"}}
}

func TestWorkspaceLifecycle(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	w := e.do("POST", "/api/admin/workspaces", workspaceBody("alice"), c)
	if w.Code != http.StatusCreated || decode[workspaceRow](t, w).Name != "alice" {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(e.root, "alice")); err != nil {
		t.Fatalf("folder: %v", err)
	}
	if w := e.do("POST", "/api/admin/workspaces", workspaceBody("Bad-Name"), c); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(decode[apiError](t, w).Message, "workspace name must match") {
		t.Fatalf("bad name: %d %s", w.Code, w.Body.String())
	}
	if rows := decode[[]workspaceRow](t, e.do("GET", "/api/admin/workspaces", nil, c)); len(rows) != 1 || rows[0].Ports.Start != 20000 {
		t.Fatalf("list = %+v", rows)
	}

	bad := store.BuiltinDefaultQuota()
	bad.DefaultMemoryMB = 4096
	w = e.do("PUT", "/api/admin/workspaces/alice", map[string]any{"quota": bad, "ports": service.PortSettings{BindIP: "0.0.0.0"}}, c)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "default_memory_mb") {
		t.Fatalf("bad quota: %d %s", w.Code, w.Body.String())
	}
	good := store.BuiltinDefaultQuota()
	good.MaxApps = 9
	w = e.do("PUT", "/api/admin/workspaces/alice", map[string]any{"quota": good, "ports": service.PortSettings{BindIP: "127.0.0.1"}}, c)
	if d := decode[workspaceDetail](t, w); w.Code != 200 || d.Quota.MaxApps != 9 || d.Ports.BindIP != "127.0.0.1" {
		t.Fatalf("update: %d %+v", w.Code, d)
	}

	id, err := e.svc.SaveTemplate(context.Background(), "admin", 0, publicWebTemplate())
	if err != nil {
		t.Fatal(err)
	}
	if w := e.do("PUT", "/api/admin/workspaces/alice/grants", map[string]any{"template_ids": []int64{id}}, c); w.Code != http.StatusNoContent {
		t.Fatalf("grants: %d %s", w.Code, w.Body.String())
	}

	w = e.do("POST", "/api/admin/workspaces/alice/tokens", map[string]string{"label": "claude"}, c)
	tok := decode[map[string]string](t, w)["token"]
	if w.Code != http.StatusCreated || !strings.HasPrefix(tok, "dgw_") {
		t.Fatalf("token: %d %s", w.Code, w.Body.String())
	}
	w = e.do("GET", "/api/admin/workspaces/alice", nil, c)
	d := decode[workspaceDetail](t, w)
	if strings.Contains(w.Body.String(), tok) {
		t.Fatal("token must only be returned once")
	}
	if len(d.Tokens) != 1 || d.Tokens[0].Label != "claude" || len(d.Templates) != 1 || !d.Templates[0].Granted || d.Folder != filepath.Join(e.root, "alice") {
		t.Fatalf("detail = %+v", d)
	}
	if w := e.do("POST", "/api/admin/workspaces/alice/tokens/"+strconv.FormatInt(d.Tokens[0].ID, 10)+"/revoke", nil, c); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}

	if w := e.do("DELETE", "/api/admin/workspaces/alice", map[string]string{"confirm": "alic"}, c); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong confirm: %d", w.Code)
	}
	if w := e.do("DELETE", "/api/admin/workspaces/alice", map[string]string{"confirm": "alice"}, c); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("GET", "/api/admin/workspaces/alice", nil, c); w.Code != http.StatusNotFound {
		t.Fatalf("after delete: %d", w.Code)
	}
}

func TestAppsStopAndDelete(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	e.do("POST", "/api/admin/workspaces", workspaceBody("alice"), c)
	ws, _ := e.st.GetWorkspaceByName(context.Background(), "alice")
	st := stack.Stack{Services: map[string]stack.Service{"web": {Image: "nginx:1.27.0"}}}
	if _, err := e.svc.Deploy(context.Background(), servicetest.Principal(t, e.svc, e.st, ws.Name, ""), service.DeployInput{App: "shop", Stack: st}); err != nil {
		t.Fatal(err)
	}
	if w := e.do("POST", "/api/admin/workspaces/alice/apps/shop/stop", nil, c); w.Code != http.StatusNoContent {
		t.Fatalf("stop: %d %s", w.Code, w.Body.String())
	}
	d := decode[workspaceDetail](t, e.do("GET", "/api/admin/workspaces/alice", nil, c))
	if len(d.Apps) != 1 || d.Apps[0].Services[0].State != "exited" {
		t.Fatalf("apps = %+v", d.Apps)
	}
	if w := e.do("DELETE", "/api/admin/workspaces/alice/apps/shop", map[string]string{"confirm": "nope"}, c); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong confirm: %d", w.Code)
	}
	if w := e.do("DELETE", "/api/admin/workspaces/alice/apps/shop", map[string]string{"confirm": "shop"}, c); w.Code != http.StatusNoContent {
		t.Fatalf("delete app: %d %s", w.Code, w.Body.String())
	}
}

type dashboardResp struct {
	Workspaces []workspaceRow `json:"workspaces"`
	Critical   []auditDTO     `json:"critical"`
	Orphans    int            `json:"orphans"`
}

func TestDashboard(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	e.do("POST", "/api/admin/workspaces", workspaceBody("alice"), c)
	e.st.InsertAudit(context.Background(), store.AuditEntry{ActorType: "agent", Actor: "token:1", Workspace: "alice", Action: "deploy",
		Params: `{"app":"shop"}`, Result: "security_violation", Error: "rejected", Severity: "critical"})
	labels := map[string]string{"deploy-gateway.workspace": "ghost", "deploy-gateway.app": "x", "deploy-gateway.managed": "true"}
	e.docker.Add(dockerx.ContainerInfo{ID: "ghost-x-web", Name: "ghost-x-web", Labels: labels}, dockerx.Facts{})
	d := decode[dashboardResp](t, e.do("GET", "/api/admin/dashboard", nil, c))
	if len(d.Workspaces) != 1 || len(d.Critical) != 1 || string(d.Critical[0].Params) != `{"app":"shop"}` || d.Orphans != 1 {
		t.Fatalf("dashboard = %+v", d)
	}
}
