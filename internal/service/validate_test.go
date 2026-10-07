package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/store"
	"github.com/sebbi08/deployment-gateway/internal/tmpl"
)

func TestWhoami(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	res, err := h.svc.Whoami(h.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Workspace != "alice" || res.PortRange != "20000-20099" || len(res.Guidance) == 0 || res.PortsInUse == nil {
		t.Fatalf("whoami = %+v", res)
	}
	entries, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Action: "whoami"})
	if len(entries) != 1 || entries[0].Workspace != "alice" || entries[0].ClientIP != "203.0.113.1" {
		t.Fatalf("audit = %+v", entries)
	}
}

// list_templates shows only the templates granted to the workspace.
func TestListTemplates(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.SaveTemplate(h.ctx, "admin", 0, tmpl.Template{Name: "watch", Labels: map[string]string{"wud.watch": "true"}}); err != nil {
		t.Fatal(err)
	}
	p := h.workspace("alice", 0, 0) // granted public-web only
	res, err := h.svc.ListTemplates(h.ctx, p)
	if err != nil || len(res.Templates) != 1 || res.Templates[0].Name != "public-web" || res.Templates[0].Shareable {
		t.Fatalf("templates = %+v, %v", res, err)
	}
}

func TestValidateReturnsPlanWithoutSideEffects(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	res, err := h.svc.Validate(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Services) != 2 || res.TotalMemoryMB != 512 || res.TotalCPUs != 1 {
		t.Fatalf("plan = %+v", res)
	}
	web := res.Services[1]
	if web.Service != "web" || web.URL != "https://shop.alice.apps.example.com" || web.InternalHost != "alice-shop-web" ||
		len(web.Ports) != 1 || web.Ports[0].HostPort != 20001 {
		t.Fatalf("web plan = %+v", web)
	}
	if calls := h.compose.Calls(); len(calls) != 0 {
		t.Fatalf("validate must not run compose: %v", calls)
	}
	if _, err := os.Stat(filepath.Join(h.root, "alice", "shop")); !os.IsNotExist(err) {
		t.Fatal("validate must not create the app folder")
	}
}

func TestValidateErrors(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	_, err := h.svc.Validate(h.ctx, p, service.DeployInput{App: "Bad_Name", Stack: mustStack(t, exampleStack)})
	if code(err) != service.CodeValidation || !strings.Contains(err.Error(), `"field":"app"`) {
		t.Fatalf("bad app name: %v", err)
	}
	_, err = h.svc.Validate(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, `{"services": {"web": {"image": "nginx"}}}`)})
	if code(err) != service.CodeValidation || !strings.Contains(err.Error(), "services.web.image") {
		t.Fatalf("invalid stack: %v", err)
	}
	q := store.BuiltinDefaultQuota()
	q.MaxTotalMemoryMB, q.MaxMemoryMB, q.DefaultMemoryMB = 300, 256, 256
	small := h.workspaceWithQuota("small", 0, 0, q)
	_, err = h.svc.Validate(h.ctx, small, service.DeployInput{App: "shop", Stack: mustStack(t,
		`{"services": {"a": {"image": "a:1"}, "b": {"image": "b:1"}}}`)})
	if code(err) != service.CodeQuota || !strings.Contains(err.Error(), "total memory") {
		t.Fatalf("quota: %v", err)
	}
}

func TestAuditRedactsEnvironmentValues(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	if _, err := h.svc.Validate(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)}); err != nil {
		t.Fatal(err)
	}
	entries, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Action: "validate"})
	if len(entries) != 1 {
		t.Fatalf("audit = %+v", entries)
	}
	if strings.Contains(entries[0].Params, "s3cr3t-value") || !strings.Contains(entries[0].Params, `"SECRET_KEY":"***"`) {
		t.Fatalf("env not redacted: %s", entries[0].Params)
	}
}
