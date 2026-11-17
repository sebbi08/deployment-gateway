package admin

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/service/servicetest"
	"github.com/sebbi08/deployment-gateway/internal/stack"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

const secretBase = "/api/admin/workspaces/alice/secrets"

func TestSecretsCRUD(t *testing.T) {
	const val = "hunter2-very-secret"
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	e.do("POST", "/api/admin/workspaces", workspaceBody("alice"), c)
	ctx := context.Background()
	ws, _ := e.st.GetWorkspaceByName(ctx, "alice")

	// A requested secret (named by a deploy) becomes set with PUT.
	if err := e.st.RequestSecrets(ctx, ws.ID, []string{"stripe"}, "shop"); err != nil {
		t.Fatal(err)
	}
	w := e.do("GET", secretBase, nil, c)
	if l := decode[[]service.SecretView](t, w); len(l) != 1 || l[0].State != "requested" || l[0].RequestedBy != "shop" {
		t.Fatalf("list: %s", w.Body.String())
	}
	w = e.do("PUT", secretBase+"/stripe", map[string]string{"value": val}, c)
	if w.Code != 200 || strings.Contains(w.Body.String(), val) || len(decode[map[string][]string](t, w)["apps"]) != 0 {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	if l := decode[[]service.SecretView](t, e.do("GET", secretBase, nil, c)); l[0].State != "set" {
		t.Fatalf("state = %+v", l)
	}

	w = e.do("POST", secretBase, map[string]string{"name": "db", "value": val}, c)
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), val) {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", secretBase, map[string]string{"name": "db", "value": val}, c); w.Code != http.StatusConflict ||
		decode[apiError](t, w).Message != "secret already exists" {
		t.Fatalf("dup: %d %s", w.Code, w.Body.String())
	}
	w = e.do("GET", secretBase, nil, c)
	if w.Code != 200 || strings.Contains(w.Body.String(), val) {
		t.Fatalf("list leaks: %s", w.Body.String())
	}
	if w := e.do("DELETE", secretBase+"/db", nil, c); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := e.do("DELETE", secretBase+"/db", nil, c); w.Code != http.StatusNotFound {
		t.Fatalf("delete missing: %d", w.Code)
	}
	if w := e.do("PUT", secretBase+"/nope", map[string]string{"value": "x"}, c); w.Code != http.StatusNotFound {
		t.Fatalf("put missing: %d", w.Code)
	}
}

func TestSecretsValidation(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	e.do("POST", "/api/admin/workspaces", workspaceBody("alice"), c)
	for _, tc := range []struct{ name, value, field string }{
		{"k", "a\nb", "value"}, {"k", "a\rb", "value"}, {"k", "a\x00b", "value"}, {"k", "", "value"},
		{"k", strings.Repeat("x", 8193), "value"}, {"Bad", "ok", "name"},
	} {
		w := e.do("POST", secretBase, map[string]string{"name": tc.name, "value": tc.value}, c)
		body := w.Body.String()
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(body, `"field":"`+tc.field+`"`) {
			t.Errorf("%q/%d: %d %s", tc.name, len(tc.value), w.Code, body)
		}
		if len(tc.value) > 1 && strings.Contains(body, tc.value) {
			t.Errorf("error echoes the value: %s", body)
		}
	}
	if w := e.do("PUT", secretBase+"/k", map[string]string{"value": "a\nb"}, c); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("put: %d", w.Code)
	}
}

// An operator who pastes a value into the name field must not find it in
// the audit log: invalid names are audited as "<invalid>".
func TestSecretsInvalidNameNotAudited(t *testing.T) {
	const pasted = "Sk_Live_Pasted-Value-123"
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	e.do("POST", "/api/admin/workspaces", workspaceBody("alice"), c)
	if w := e.do("POST", secretBase, map[string]string{"name": pasted, "value": "v"}, c); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("PUT", secretBase+"/"+pasted, map[string]string{"value": "v"}, c); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("set: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", secretBase+"/"+pasted+"/apply", nil, c); w.Code != http.StatusNotFound {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("DELETE", secretBase+"/"+pasted, nil, c); w.Code != http.StatusNotFound {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	for _, action := range []string{"secret_create", "secret_set", "secret_apply_request", "secret_delete"} {
		rows, err := e.st.ListAudit(context.Background(), store.AuditFilter{Action: action})
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s: rows = %+v, %v", action, rows, err)
		}
		if r := rows[0]; strings.Contains(r.Params+r.Error, pasted) || r.Params != `{"name":"<invalid>"}` {
			t.Fatalf("%s: params = %s, error = %s", action, r.Params, r.Error)
		}
	}
}

func TestSecretsCap(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	e.do("POST", "/api/admin/workspaces", workspaceBody("alice"), c)
	ctx := context.Background()
	ws, _ := e.st.GetWorkspaceByName(ctx, "alice")
	for i := 0; i < store.MaxSecretsPerWorkspace; i++ {
		if err := e.st.CreateSecret(ctx, ws.ID, fmt.Sprintf("s%d", i), "v"); err != nil {
			t.Fatal(err)
		}
	}
	w := e.do("POST", secretBase, map[string]string{"name": "extra", "value": "v"}, c)
	if w.Code != http.StatusUnprocessableEntity || decode[apiError](t, w).Message != "this workspace already holds 100 secrets" {
		t.Fatalf("cap: %d %s", w.Code, w.Body.String())
	}
}

func TestSecretsInUseAndApply(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	e.do("POST", "/api/admin/workspaces", workspaceBody("alice"), c)
	ctx := context.Background()
	ws, _ := e.st.GetWorkspaceByName(ctx, "alice")
	if w := e.do("POST", secretBase, map[string]string{"name": "stripe", "value": "first-value"}, c); w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	st := stack.Stack{Services: map[string]stack.Service{"web": {Image: "nginx:1.27.0", Secrets: map[string]string{"K": "stripe"}}}}
	if _, err := e.svc.Deploy(ctx, servicetest.Principal(t, e.svc, e.st, ws.Name, ""), service.DeployInput{App: "shop", Stack: st}); err != nil {
		t.Fatal(err)
	}
	w := e.do("DELETE", secretBase+"/stripe", nil, c)
	if w.Code != http.StatusConflict || !strings.Contains(decode[apiError](t, w).Message, "shop") {
		t.Fatalf("in use: %d %s", w.Code, w.Body.String())
	}
	l := decode[[]service.SecretView](t, e.do("GET", secretBase, nil, c))
	if len(l) != 1 || len(l[0].UsedBy) != 1 || l[0].UsedBy[0].App != "shop" || l[0].UsedBy[0].EnvKey != "K" {
		t.Fatalf("used_by = %+v", l)
	}
	w = e.do("PUT", secretBase+"/stripe", map[string]string{"value": "second-value"}, c)
	if a := decode[map[string][]string](t, w)["apps"]; w.Code != 200 || len(a) != 1 || a[0] != "shop" {
		t.Fatalf("put apps: %d %s", w.Code, w.Body.String())
	}
	w = e.do("POST", secretBase+"/stripe/apply", nil, c)
	if w.Code != 200 || strings.Contains(w.Body.String(), "second-value") {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", secretBase+"/nope/apply", nil, c); w.Code != http.StatusNotFound {
		t.Fatalf("apply missing: %d", w.Code)
	}
	// A delete that succeeds still has an audit row: use another secret.
	e.do("POST", secretBase, map[string]string{"name": "tmp", "value": "third-value"}, c)
	if w := e.do("DELETE", secretBase+"/tmp", nil, c); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}

	// Audit rows carry the name only, never a value.
	for _, action := range []string{"secret_create", "secret_set", "secret_apply_request", "secret_delete"} {
		rows, err := e.st.ListAudit(ctx, store.AuditFilter{Action: action})
		if err != nil || len(rows) == 0 {
			t.Fatalf("%s: no audit row (%v)", action, err)
		}
		for _, r := range rows {
			all := r.Params + r.Error
			if strings.Contains(all, "first-value") || strings.Contains(all, "second-value") || strings.Contains(all, "third-value") {
				t.Fatalf("%s leaks: %+v", action, r)
			}
			if !strings.Contains(r.Params, `"name"`) {
				t.Fatalf("%s params = %s", action, r.Params)
			}
		}
	}
}

func TestRequestedSecretsCounts(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	e.do("POST", "/api/admin/workspaces", workspaceBody("alice"), c)
	ctx := context.Background()
	ws, _ := e.st.GetWorkspaceByName(ctx, "alice")
	if err := e.st.RequestSecrets(ctx, ws.ID, []string{"a", "b"}, "shop"); err != nil {
		t.Fatal(err)
	}
	if d := decode[dashboardResp](t, e.do("GET", "/api/admin/dashboard", nil, c)); d.RequestedSecrets != 2 || d.Workspaces[0].RequestedSecrets != 2 {
		t.Fatalf("dashboard = %+v", d)
	}
	if d := decode[workspaceDetail](t, e.do("GET", "/api/admin/workspaces/alice", nil, c)); d.RequestedSecrets != 2 {
		t.Fatalf("detail = %+v", d.workspaceRow)
	}
}

func TestSecretFilesAreNotShown(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	e.do("POST", "/api/admin/workspaces", workspaceBody("alice"), c)
	dir := filepath.Join(e.root, "alice", "shop", "secrets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web.env"), []byte("K=v\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := e.do("GET", "/api/admin/workspaces/alice/files/content?path=shop/secrets/web.env", nil, c)
	if w.Code != http.StatusUnprocessableEntity || decode[apiError](t, w).Message != "secret files are not shown" || strings.Contains(w.Body.String(), "K=v") {
		t.Fatalf("content: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("GET", "/api/admin/workspaces/alice/files?path=shop/secrets", nil, c); w.Code != 200 {
		t.Fatalf("listing: %d %s", w.Code, w.Body.String())
	}
}
