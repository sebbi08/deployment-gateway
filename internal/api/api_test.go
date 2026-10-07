package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/agentauth"
	"github.com/sebbi08/deployment-gateway/internal/auth"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/service/servicetest"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

func setup(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	srv, tok, _ := setupWithStore(t)
	return srv, tok
}

func setupWithStore(t *testing.T) (*httptest.Server, string, *store.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	compose, docker := &servicetest.FakeCompose{}, servicetest.NewFakeDocker()
	servicetest.Wire(compose, docker)
	svc := service.New(st, compose, docker, t.TempDir())
	svc.Chown = func(string, int, int) error { return nil }
	w, err := svc.CreateWorkspace(ctx, "admin", "alice", store.BuiltinDefaultQuota(), service.PortSettings{Start: 20000, End: 20099, BindIP: "0.0.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svc.CreateToken(ctx, "admin", w, "ci")
	if err != nil {
		t.Fatal(err)
	}
	authn := &agentauth.Authenticator{Store: st, Limiter: auth.NewLimiter(10, time.Minute, time.Hour)}
	srv := httptest.NewServer((&Handler{Svc: svc, Auth: authn}).Routes())
	t.Cleanup(srv.Close)
	return srv, tok, st
}

func post(t *testing.T, srv *httptest.Server, tok, tool, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/"+tool, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("non-JSON response %d: %s", resp.StatusCode, raw)
	}
	return resp.StatusCode, m
}

func TestWhoamiAndAuth(t *testing.T) {
	srv, tok := setup(t)
	code, body := post(t, srv, tok, "whoami", "")
	if code != 200 || body["workspace"] != "alice" {
		t.Fatalf("%d %v", code, body)
	}
	if code, body := post(t, srv, "", "whoami", "{}"); code != 401 || body["code"] != "unauthorized" {
		t.Fatalf("%d %v", code, body)
	}
}

func TestDeployViaAPI(t *testing.T) {
	srv, tok := setup(t)
	ok := `{"app": "shop", "stack": {"services": {"web": {"image": "nginx:1.27.0", "ports": [{"container": 80, "host": 20001}]}}}}`
	code, body := post(t, srv, tok, "deploy", ok)
	if code != 200 || body["app"] != "shop" {
		t.Fatalf("%d %v", code, body)
	}
	code, body = post(t, srv, tok, "deploy", `{"app": "shop", "stack": {"services": {"web": {"image": "nginx:1.27.0", "privileged": true}}}}`)
	if code != 400 || body["code"] != "validation_failed" || !strings.Contains(body["message"].(string), "privileged") {
		t.Fatalf("unknown field: %d %v", code, body)
	}
	code, body = post(t, srv, tok, "deploy", `{"app": "shop", "stack": {"services": {"web": {"image": "nginx"}}}}`)
	if code != 422 || body["errors"] == nil {
		t.Fatalf("validation: %d %v", code, body)
	}
	code, body = post(t, srv, tok, "logs", `{"app": "nope", "service": "web"}`)
	if code != 404 || body["code"] != "not_found" {
		t.Fatalf("not found: %d %v", code, body)
	}
	code, body = post(t, srv, tok, "list", "{}")
	if code != 200 || len(body["apps"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, body)
	}
	code, body = post(t, srv, tok, "delete", `{"app": "shop", "confirm": "shop"}`)
	if code != 200 || body["deleted"] != true {
		t.Fatalf("delete: %d %v", code, body)
	}
}

// Calls rejected before the service (malformed or unknown fields) are
// exactly what an admin needs to see, e.g. a prompt-injected agent probing
// for option injection; they must leave an audit row.
func TestRejectedCallsAreAudited(t *testing.T) {
	srv, tok, st := setupWithStore(t)
	code, _ := post(t, srv, tok, "deploy", `{"app":"shop","stack":{"services":{"web":{"image":"a:1","privileged":true,"environment":{"K":"secret-v"}}}}}`)
	if code != http.StatusBadRequest {
		t.Fatalf("status %d", code)
	}
	rows, err := st.ListAudit(context.Background(), store.AuditFilter{Action: "deploy"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("audit rows = %+v (%v)", rows, err)
	}
	r := rows[0]
	if r.Result != service.CodeValidation || !strings.Contains(r.Params, "privileged") || strings.Contains(r.Params, "secret-v") || r.Workspace != "alice" {
		t.Fatalf("audit row = %+v", r)
	}
}
