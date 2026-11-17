package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/service/servicetest"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

const secretStack = `{"services": {"web": {"image": "a:1", "secrets": {"K": "stripe-prod"}}}}`

func (h *harness) secretsDir(ws, app string) string {
	return filepath.Join(h.root, ws, app, "secrets")
}

func (h *harness) createSecret(p service.Principal, name, value string) {
	h.t.Helper()
	if err := h.st.CreateSecret(h.ctx, p.Workspace.ID, name, value); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) setSecret(p service.Principal, name, value string) {
	h.t.Helper()
	if _, err := h.st.SetSecretValue(h.ctx, p.Workspace.ID, name, value); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) deployRaw(p service.Principal, app, raw string) (service.AppStatus, error) {
	h.t.Helper()
	return h.svc.Deploy(h.ctx, p, service.DeployInput{App: app, Stack: mustStack(h.t, raw)})
}

func (h *harness) appRefs(p service.Principal, app string) []store.SecretRef {
	h.t.Helper()
	a, err := h.st.GetApp(h.ctx, p.Workspace.ID, app)
	if err != nil {
		h.t.Fatal(err)
	}
	refs, err := h.st.AppSecretRefs(h.ctx, a.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return refs
}

func fieldMessage(err error, field string) (string, bool) {
	var se *service.Error
	if !errors.As(err, &se) {
		return "", false
	}
	for _, f := range se.Fields {
		if f.Field == field {
			return f.Message, true
		}
	}
	return "", false
}

func TestValidateRequestsUnknownSecret(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	_, err := h.svc.Validate(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, secretStack)})
	if code(err) != service.CodeValidation {
		t.Fatalf("err = %v", err)
	}
	msg, ok := fieldMessage(err, "services.web.secrets.K")
	want := `secret "stripe-prod" has no value yet; ask the operator to set it in the deploy-gateway admin UI (workspace "alice", tab Secrets)`
	if !ok || msg != want {
		t.Fatalf("field message = %q (found %v), err = %v", msg, ok, err)
	}
	secrets, err := h.st.ListSecrets(h.ctx, p.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 1 || secrets[0].Name != "stripe-prod" || secrets[0].Set || secrets[0].RequestedAt == nil || secrets[0].RequestedBy != "shop" {
		t.Fatalf("secrets = %+v", secrets)
	}
	if _, err := h.st.GetApp(h.ctx, p.Workspace.ID, "shop"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("app record must not exist: %v", err)
	}
}

func TestDeployRequestsUnknownSecretAndChangesNothing(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	_, err := h.deployRaw(p, "shop", secretStack)
	if _, ok := fieldMessage(err, "services.web.secrets.K"); !ok || code(err) != service.CodeValidation {
		t.Fatalf("err = %v", err)
	}
	if calls := h.compose.Calls(); len(calls) != 0 {
		t.Fatalf("compose calls = %v", calls)
	}
	if secrets, _ := h.st.ListSecrets(h.ctx, p.Workspace.ID); len(secrets) != 1 || secrets[0].RequestedBy != "shop" {
		t.Fatalf("secrets = %+v", secrets)
	}
	if _, err := h.st.GetApp(h.ctx, p.Workspace.ID, "shop"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("app record must not exist: %v", err)
	}
}

func TestValidateMissingSecretAtCap(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	for i := range store.MaxSecretsPerWorkspace {
		h.createSecret(p, fmt.Sprintf("s%d", i), "v")
	}
	_, err := h.svc.Validate(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, secretStack)})
	msg, ok := fieldMessage(err, "services.web.secrets.K")
	want := `secret "stripe-prod" does not exist and the workspace already holds 100 secrets; ask the operator to remove unused ones`
	if !ok || msg != want || code(err) != service.CodeValidation {
		t.Fatalf("field message = %q, err = %v", msg, err)
	}
}

func TestValidateListsSecretEnv(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.createSecret(p, "stripe-prod", "sk_live_123456")
	h.createSecret(p, "mail", "smtp-password")
	raw := `{"services": {"web": {"image": "a:1", "secrets": {"K": "stripe-prod", "A": "mail"}}, "db": {"image": "b:1"}}}`
	res, err := h.svc.Validate(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, raw)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Services) != 2 || res.Services[0].SecretEnv != nil || !slices.Equal(res.Services[1].SecretEnv, []string{"A", "K"}) {
		t.Fatalf("services = %+v", res.Services)
	}
}

func TestDeployWritesEnvFile(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.createSecret(p, "stripe-prod", "sk_live_123456")
	res, err := h.deployRaw(p, "shop", secretStack)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Services) != 1 || !slices.Equal(res.Services[0].SecretEnv, []string{"K"}) {
		t.Fatalf("result = %+v", res)
	}
	dir := h.secretsDir("alice", "shop")
	envFile := filepath.Join(dir, "web.env")
	data, err := os.ReadFile(envFile)
	if err != nil || string(data) != "K=sk_live_123456\n" {
		t.Fatalf("env file = %q, %v", data, err)
	}
	if fi, _ := os.Stat(envFile); fi.Mode().Perm() != 0o600 {
		t.Fatalf("env file mode %o", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatalf("secrets dir mode %o", fi.Mode().Perm())
	}
	compose, err := os.ReadFile(filepath.Join(h.root, "alice", "shop", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(compose), "sk_live_123456") || !strings.Contains(string(compose), envFile) {
		t.Fatalf("compose file:\n%s", compose)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("secrets dir has leftovers: %v", entries)
	}
	refs := h.appRefs(p, "shop")
	if len(refs) != 1 || refs[0].Service != "web" || refs[0].EnvKey != "K" || refs[0].Secret != "stripe-prod" || refs[0].Stale {
		t.Fatalf("refs = %+v", refs)
	}
}

// pull runs on the next file: a new service's env file must exist (empty)
// so compose can load it, without touching the running deployment's files.
func TestDeployCreatesEmptyEnvFileBeforePull(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.createSecret(p, "stripe-prod", "sk_live_123456")
	envFile := filepath.Join(h.secretsDir("alice", "shop"), "web.env")
	var atPull []byte
	pullErr := errors.New("pull failed")
	h.compose.PullErr = pullErr
	h.compose.ObserveCtx = func(op string, _ context.Context) {
		if op == "pull" {
			atPull, _ = os.ReadFile(envFile)
		}
	}
	if _, err := h.deployRaw(p, "shop", secretStack); code(err) != service.CodeDeployFailed {
		t.Fatalf("err = %v", err)
	}
	fi, err := os.Stat(envFile)
	if err != nil || fi.Size() != 0 || fi.Mode().Perm() != 0o600 || atPull == nil || len(atPull) != 0 {
		t.Fatalf("env file at pull = %q, stat %v %v", atPull, fi, err)
	}
}

func TestDeployFailureBeforeCommitKeepsEnvFile(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.createSecret(p, "stripe-prod", "sk_live_123456")
	if _, err := h.deployRaw(p, "shop", secretStack); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(h.secretsDir("alice", "shop"), "web.env")
	before, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	h.setSecret(p, "stripe-prod", "sk_live_999999")
	h.compose.PullErr = errors.New("pull failed")
	if _, err := h.deployRaw(p, "shop", secretStack); code(err) != service.CodeDeployFailed {
		t.Fatalf("err = %v", err)
	}
	after, err := os.ReadFile(envFile)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("env file changed: %q -> %q (%v)", before, after, err)
	}
}

func TestRedeployClearsStale(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.createSecret(p, "stripe-prod", "sk_live_123456")
	if _, err := h.deployRaw(p, "shop", secretStack); err != nil {
		t.Fatal(err)
	}
	h.setSecret(p, "stripe-prod", "sk_live_999999")
	if refs := h.appRefs(p, "shop"); len(refs) != 1 || !refs[0].Stale {
		t.Fatalf("refs after set = %+v", refs)
	}
	if _, err := h.deployRaw(p, "shop", secretStack); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(h.secretsDir("alice", "shop"), "web.env"))
	if string(data) != "K=sk_live_999999\n" {
		t.Fatalf("env file = %q", data)
	}
	if refs := h.appRefs(p, "shop"); len(refs) != 1 || refs[0].Stale {
		t.Fatalf("refs after redeploy = %+v", refs)
	}
}

func TestDeployRemovesEnvFileOfServiceWithoutSecrets(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.createSecret(p, "stripe-prod", "sk_live_123456")
	h.createSecret(p, "dbpass", "postgres-pass")
	two := `{"services": {"web": {"image": "a:1", "secrets": {"K": "stripe-prod"}}, "db": {"image": "b:1", "secrets": {"P": "dbpass"}}}}`
	if _, err := h.deployRaw(p, "shop", two); err != nil {
		t.Fatal(err)
	}
	dir := h.secretsDir("alice", "shop")
	if _, err := os.Stat(filepath.Join(dir, "db.env")); err != nil {
		t.Fatal(err)
	}
	noDBSecrets := `{"services": {"web": {"image": "a:1", "secrets": {"K": "stripe-prod"}}, "db": {"image": "b:1"}}}`
	if _, err := h.deployRaw(p, "shop", noDBSecrets); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "db.env")); !os.IsNotExist(err) {
		t.Fatalf("db.env must be removed: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "web.env")); string(data) != "K=sk_live_123456\n" {
		t.Fatalf("web.env = %q", data)
	}
	if refs := h.appRefs(p, "shop"); len(refs) != 1 || refs[0].Service != "web" {
		t.Fatalf("refs = %+v", refs)
	}
	// Without any secrets left the folder is emptied as well.
	if _, err := h.deployRaw(p, "shop", `{"services": {"web": {"image": "a:1"}}}`); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("secrets dir = %v", entries)
	}
}

func TestMaskingInLogsAndErrors(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.createSecret(p, "a", "abcdef123")
	h.createSecret(p, "b", "abcdef")
	h.createSecret(p, "short", "abc12") // below 6 bytes: not masked
	raw := `{"services": {"web": {"image": "a:1", "secrets": {"A": "a", "B": "b"}}}}`
	if _, err := h.deployRaw(p, "shop", raw); err != nil {
		t.Fatal(err)
	}
	h.docker.LogsOut = "x abcdef123 y abcdef z abc12"
	res, err := h.svc.Logs(h.ctx, p, service.LogsInput{App: "shop", Service: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Logs != "x *** y *** z abc12" {
		t.Fatalf("logs = %q", res.Logs)
	}

	// Compose output and failure logs of a failed deploy.
	for id, c := range h.docker.Containers {
		c.State = "exited"
		h.docker.Containers[id] = c
	}
	h.compose.UpErr = errors.New("compose up failed with exit code 1")
	h.compose.UpOut = "Error: env A=abcdef123 B=abcdef"
	_, err = h.deployRaw(p, "shop", raw)
	if code(err) != service.CodeDeployFailed {
		t.Fatalf("err = %v", err)
	}
	msg := err.Error()
	if strings.Contains(msg, "abcdef") || !strings.Contains(msg, "env A=*** B=***") || !strings.Contains(msg, "x *** y *** z") {
		t.Fatalf("deploy error not masked: %s", msg)
	}

	// Lifecycle compose output.
	h.compose.UpErr, h.compose.UpOut = nil, ""
	h.svc.Compose = &failingStop{FakeCompose: h.compose, out: "stopping A=abcdef123", err: errors.New("stop failed")}
	_, err = h.svc.Stop(h.ctx, p, service.ServiceInput{App: "shop"})
	if code(err) != service.CodeDeployFailed || strings.Contains(err.Error(), "abcdef") || !strings.Contains(err.Error(), "A=***") {
		t.Fatalf("stop error not masked: %v", err)
	}

	entries, err := h.st.ListAudit(h.ctx, store.AuditFilter{Limit: 1000})
	if err != nil || len(entries) == 0 {
		t.Fatalf("audit: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Params, "abcdef") || strings.Contains(e.Error, "abcdef") {
			t.Fatalf("audit row %s contains a secret value: params=%s error=%s", e.Action, e.Params, e.Error)
		}
	}
}

// failingStop makes compose stop fail with the given output.
type failingStop struct {
	*servicetest.FakeCompose
	out string
	err error
}

func (f *failingStop) Stop(context.Context, string, string, string) (string, error) {
	return f.out, f.err
}

func TestRerequestAfterDelete(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	in := service.DeployInput{App: "shop", Stack: mustStack(t, secretStack)}
	if _, err := h.svc.Validate(h.ctx, p, in); code(err) != service.CodeValidation {
		t.Fatalf("err = %v", err)
	}
	if err := h.st.DeleteSecret(h.ctx, p.Workspace.ID, "stripe-prod"); err != nil {
		t.Fatal(err)
	}
	_, err := h.svc.Validate(h.ctx, p, in)
	if msg, _ := fieldMessage(err, "services.web.secrets.K"); code(err) != service.CodeValidation || !strings.Contains(msg, "has no value yet") {
		t.Fatalf("err = %v", err)
	}
	secrets, _ := h.st.ListSecrets(h.ctx, p.Workspace.ID)
	if len(secrets) != 1 || secrets[0].Name != "stripe-prod" || secrets[0].RequestedAt == nil {
		t.Fatalf("secrets = %+v", secrets)
	}
}

func TestEnvFilesNotOwnedByFileOwner(t *testing.T) {
	h := newHarness(t)
	h.svc.FileOwner = &service.Owner{UID: 1000, GID: 1001}
	p := h.workspace("alice", 20000, 20099)
	h.createSecret(p, "stripe-prod", "sk_live_123456")
	h.chowns = nil
	for range 2 { // the second deploy rewrites existing files
		if _, err := h.deployRaw(p, "shop", secretStack); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Contains(h.chowns, "shop 0:1001") {
		t.Fatalf("app folder not chowned: %v", h.chowns)
	}
	for _, c := range h.chowns {
		name, _, _ := strings.Cut(c, " ")
		if name == "secrets" || strings.HasSuffix(name, ".env") || strings.Contains(name, ".env.") {
			t.Fatalf("secrets path chowned: %v", h.chowns)
		}
	}
}

func TestSecretValueNeverInAgentResults(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.createSecret(p, "stripe-prod", "sk_live_123456")
	in := service.DeployInput{App: "shop", Stack: mustStack(t, secretStack)}
	var results []any
	v, err := h.svc.Validate(h.ctx, p, in)
	if err != nil {
		t.Fatal(err)
	}
	d, err := h.svc.Deploy(h.ctx, p, in)
	if err != nil {
		t.Fatal(err)
	}
	l, err := h.svc.List(h.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	w, err := h.svc.Whoami(h.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	results = append(results, v, d, l, w)
	for i, r := range results {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "sk_live_123456") {
			t.Fatalf("result %d contains the value: %s", i, b)
		}
	}
}
