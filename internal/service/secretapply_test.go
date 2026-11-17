package service_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

const applyStack = `{"services": {
  "web": {"image": "a:1", "secrets": {"K": "db-pass"}, "expose": {"template": "public-web", "port": 8080},
          "healthcheck": {"command": ["wget", "-qO-", "http://localhost:8080/health"]}},
  "db": {"image": "b:1", "secrets": {"K": "db-pass"}}
}}`

const (
	oldPass = "old-password-1"
	newPass = "new-password-2"
)

// deployApply deploys applyStack as alice/shop and returns the app record.
func deployApply(t *testing.T, h *harness, p service.Principal) store.App {
	t.Helper()
	h.svc.ApplyLockWait = 5 * time.Second
	h.createSecret(p, "db-pass", oldPass)
	if _, err := h.deployRaw(p, "shop", applyStack); err != nil {
		t.Fatal(err)
	}
	return h.app(p, "shop")
}

func (h *harness) app(p service.Principal, name string) store.App {
	h.t.Helper()
	a, err := h.st.GetApp(h.ctx, p.Workspace.ID, name)
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

func (h *harness) setState(id, state string) {
	h.t.Helper()
	c, ok := h.docker.Containers[id]
	if !ok {
		h.t.Fatalf("no container %s", id)
	}
	c.State = state
	h.docker.Containers[id] = c
}

// changeSecret replaces the value, enqueues the affected apps and waits for
// the applier to finish.
func (h *harness) changeSecret(p service.Principal, name, value string) {
	h.t.Helper()
	refs, err := h.st.SetSecretValue(h.ctx, p.Workspace.ID, name, value)
	if err != nil {
		h.t.Fatal(err)
	}
	var ids []int64
	for _, r := range refs {
		if !slices.Contains(ids, r.AppID) {
			ids = append(ids, r.AppID)
		}
	}
	h.svc.EnqueueApply(p.Workspace.Name, ids)
	h.waitApplyIdle()
}

func (h *harness) startApplier() {
	ctx, cancel := context.WithCancel(h.ctx)
	h.t.Cleanup(cancel)
	h.svc.StartSecretApplier(ctx)
}

func (h *harness) waitApplyIdle() {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !h.svc.ApplyIdle() {
		if time.Now().After(deadline) {
			h.t.Fatal("applier did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) callsSince(n int) []string {
	return h.compose.Calls()[n:]
}

func staleByService(refs []store.SecretRef) map[string]bool {
	out := map[string]bool{}
	for _, r := range refs {
		out[r.Service] = r.Stale
	}
	return out
}

func TestApplyRecreatesRunningServices(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	before := deployApply(t, h, p)
	h.startApplier()
	h.setState("alice-shop-db", "exited")
	n := len(h.compose.Calls())
	h.changeSecret(p, "db-pass", newPass)

	if got := h.callsSince(n); !slices.Equal(got, []string{"recreate dg-alice-shop alice-shop-web"}) {
		t.Fatalf("compose calls = %v", got)
	}
	if stale := staleByService(h.appRefs(p, "shop")); stale["web"] || !stale["db"] {
		t.Fatalf("stale = %v", stale)
	}
	a := h.app(p, "shop")
	if a.SecretApplyState != "ok" || a.SecretApplyError != "" || a.SecretApplyAt == nil {
		t.Fatalf("apply state = %q %q %v", a.SecretApplyState, a.SecretApplyError, a.SecretApplyAt)
	}
	if a.LastDeployResult != before.LastDeployResult || !a.LastDeployAt.Equal(*before.LastDeployAt) {
		t.Fatalf("last deploy changed: %q %v", a.LastDeployResult, a.LastDeployAt)
	}
	// Both env files hold the new value, also the stopped service's.
	for _, svc := range []string{"web", "db"} {
		data, err := os.ReadFile(h.secretsDir("alice", "shop") + "/" + svc + ".env")
		if err != nil || string(data) != "K="+newPass+"\n" {
			t.Fatalf("%s.env = %q, %v", svc, data, err)
		}
	}
	rows, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Action: "secret_apply"})
	if len(rows) != 1 || rows[0].ActorType != "system" || rows[0].Result != "ok" || rows[0].Workspace != "alice" ||
		!strings.Contains(rows[0].Params, `"services":["web"]`) || !strings.Contains(rows[0].Params, `"secret":"db-pass"`) ||
		strings.Contains(rows[0].Params, newPass) {
		t.Fatalf("audit = %+v", rows)
	}
}

func TestApplyRecreatesRestarting(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployApply(t, h, p)
	h.startApplier()
	h.setState("alice-shop-db", "restarting")
	n := len(h.compose.Calls())
	h.changeSecret(p, "db-pass", newPass)
	if got := h.callsSince(n); !slices.Equal(got, []string{"recreate dg-alice-shop alice-shop-db,alice-shop-web"}) {
		t.Fatalf("compose calls = %v", got)
	}
	if a := h.app(p, "shop"); a.SecretApplyState != "ok" {
		t.Fatalf("apply state = %q %q", a.SecretApplyState, a.SecretApplyError)
	}
	if stale := staleByService(h.appRefs(p, "shop")); stale["web"] || stale["db"] {
		t.Fatalf("stale = %v", stale)
	}
}

func TestApplySkipsFailedDeploy(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	a := deployApply(t, h, p)
	h.startApplier()
	if err := h.st.RecordDeploy(h.ctx, a.ID, "failed"); err != nil {
		t.Fatal(err)
	}
	n := len(h.compose.Calls())
	h.changeSecret(p, "db-pass", newPass)
	if got := h.callsSince(n); len(got) != 0 {
		t.Fatalf("compose calls = %v", got)
	}
	a = h.app(p, "shop")
	if a.SecretApplyState != "skipped" || a.SecretApplyError != "applied on the next successful deploy" {
		t.Fatalf("apply state = %q %q", a.SecretApplyState, a.SecretApplyError)
	}
	if stale := staleByService(h.appRefs(p, "shop")); !stale["web"] || !stale["db"] {
		t.Fatalf("stale = %v", stale)
	}
	// The env files of the failed deploy are not touched.
	if data, _ := os.ReadFile(h.secretsDir("alice", "shop") + "/web.env"); string(data) != "K="+oldPass+"\n" {
		t.Fatalf("web.env = %q", data)
	}
}

func TestApplySkipsWhenPolicyChanged(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployApply(t, h, p)
	h.startApplier()
	if err := h.svc.SetGrants(h.ctx, "admin", p.Workspace, nil); err != nil {
		t.Fatal(err)
	}
	n := len(h.compose.Calls())
	h.changeSecret(p, "db-pass", newPass)
	if got := h.callsSince(n); len(got) != 0 {
		t.Fatalf("compose calls = %v", got)
	}
	a := h.app(p, "shop")
	if a.SecretApplyState != "skipped" || a.SecretApplyError != "configuration no longer allowed by current policy; the agent must redeploy" {
		t.Fatalf("apply state = %q %q", a.SecretApplyState, a.SecretApplyError)
	}
	if stale := staleByService(h.appRefs(p, "shop")); !stale["web"] {
		t.Fatalf("stale = %v", stale)
	}
}

func TestApplyWaitsForAppLock(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployApply(t, h, p)
	h.startApplier()
	unlock, ok := h.compose.TryLock("alice/shop")
	if !ok {
		t.Fatal("lock taken")
	}
	start := time.Now()
	go func() {
		time.Sleep(time.Second)
		unlock()
	}()
	n := len(h.compose.Calls())
	h.changeSecret(p, "db-pass", newPass)
	if time.Since(start) < time.Second {
		t.Fatalf("apply finished after %v, before the lock was released", time.Since(start))
	}
	if got := h.callsSince(n); !slices.Equal(got, []string{"recreate dg-alice-shop alice-shop-db,alice-shop-web"}) {
		t.Fatalf("compose calls = %v", got)
	}
	if a := h.app(p, "shop"); a.SecretApplyState != "ok" {
		t.Fatalf("apply state = %q %q", a.SecretApplyState, a.SecretApplyError)
	}
}

// A quota lowered while the pass waits for the app lock applies to that
// pass: the compose file's mem_limit now exceeds it, so the pass is skipped.
func TestApplyUsesWorkspaceReloadedAfterLock(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	a := deployApply(t, h, p)
	h.svc.SetApplyPoll(20 * time.Millisecond)
	h.startApplier()
	unlock, ok := h.compose.TryLock("alice/shop")
	if !ok {
		t.Fatal("lock taken")
	}
	if _, err := h.st.SetSecretValue(h.ctx, p.Workspace.ID, "db-pass", newPass); err != nil {
		t.Fatal(err)
	}
	n := len(h.compose.Calls())
	h.svc.EnqueueApply("alice", []int64{a.ID})
	time.Sleep(200 * time.Millisecond) // the pass is waiting for the lock now
	w, err := h.st.GetWorkspace(h.ctx, p.Workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	w.Quota.DefaultMemoryMB, w.Quota.MaxMemoryMB = 128, 128 // the file has mem_limit 256m
	if err := h.svc.UpdateWorkspace(h.ctx, "admin", w); err != nil {
		t.Fatal(err)
	}
	unlock()
	h.waitApplyIdle()
	if got := h.callsSince(n); len(got) != 0 {
		t.Fatalf("compose calls = %v", got)
	}
	if a := h.app(p, "shop"); a.SecretApplyState != "skipped" || a.SecretApplyError != "configuration no longer allowed by current policy; the agent must redeploy" {
		t.Fatalf("apply state = %q %q", a.SecretApplyState, a.SecretApplyError)
	}
}

func TestApplyLockTimeout(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployApply(t, h, p)
	h.svc.ApplyLockWait = 200 * time.Millisecond
	h.svc.SetApplyPoll(20 * time.Millisecond)
	h.startApplier()
	unlock, _ := h.compose.TryLock("alice/shop")
	defer unlock()
	n := len(h.compose.Calls())
	h.changeSecret(p, "db-pass", newPass)
	if got := h.callsSince(n); len(got) != 0 {
		t.Fatalf("compose calls = %v", got)
	}
	if a := h.app(p, "shop"); a.SecretApplyState != "skipped" || a.SecretApplyError != "app busy" {
		t.Fatalf("apply state = %q %q", a.SecretApplyState, a.SecretApplyError)
	}
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (w *syncBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *syncBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func TestApplyWorkspaceDeleted(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	a := deployApply(t, h, p)
	logs := &syncBuffer{}
	h.svc.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if _, err := h.st.SetSecretValue(h.ctx, p.Workspace.ID, "db-pass", newPass); err != nil {
		t.Fatal(err)
	}
	// Queued before the applier runs; the workspace is gone by then.
	h.svc.EnqueueApply("alice", []int64{a.ID})
	if err := h.svc.DeleteWorkspace(h.ctx, "admin", p.Workspace); err != nil {
		t.Fatal(err)
	}
	n := len(h.compose.Calls())
	h.startApplier()
	h.waitApplyIdle()
	if got := h.callsSince(n); len(got) != 0 {
		t.Fatalf("compose calls = %v", got)
	}
	if out := logs.String(); strings.Contains(out, "level=ERROR") {
		t.Fatalf("error logged:\n%s", out)
	}
	if rows, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Action: "secret_apply"}); len(rows) != 0 {
		t.Fatalf("audit = %+v", rows)
	}
}

func TestApplyRuntimeViolation(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployApply(t, h, p)
	h.startApplier()
	wired := h.compose.OnRecreate
	h.compose.OnRecreate = func(file, project string, services []string) error {
		if err := wired(file, project, services); err != nil {
			return err
		}
		h.docker.MutateFacts("alice-shop-web", func(f *dockerx.Facts) { f.Privileged = true })
		return nil
	}
	h.changeSecret(p, "db-pass", newPass)
	if len(h.docker.Containers) != 0 {
		t.Fatalf("containers left: %v", h.docker.Containers)
	}
	crit, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Severity: "critical"})
	if len(crit) != 1 || crit[0].Action != "secret_apply" || crit[0].Result != service.CodeSecurity || crit[0].ActorType != "system" {
		t.Fatalf("critical audit = %+v", crit)
	}
	a := h.app(p, "shop")
	if a.SecretApplyState != "failed" || !strings.Contains(a.SecretApplyError, "runtime security check") {
		t.Fatalf("apply state = %q %q", a.SecretApplyState, a.SecretApplyError)
	}
	if stale := staleByService(h.appRefs(p, "shop")); !stale["web"] {
		t.Fatalf("stale = %v", stale)
	}
}

func TestApplyRecreateFailure(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployApply(t, h, p)
	h.startApplier()
	h.compose.UpErr = errors.New("compose up failed with exit code 1")
	h.compose.UpOut = "container failed with password " + newPass
	h.changeSecret(p, "db-pass", newPass)
	a := h.app(p, "shop")
	if a.SecretApplyState != "failed" || a.SecretApplyError == "" || strings.Contains(a.SecretApplyError, newPass) {
		t.Fatalf("apply state = %q %q", a.SecretApplyState, a.SecretApplyError)
	}
	if a.LastDeployResult != "ok" {
		t.Fatalf("last deploy result = %q", a.LastDeployResult)
	}
	if stale := staleByService(h.appRefs(p, "shop")); !stale["web"] || !stale["db"] {
		t.Fatalf("stale = %v", stale)
	}
}

// A value saved while a pass for the app is queued is applied by that pass;
// the duplicate request is dropped.
func TestEnqueueApplyDedupes(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	a := deployApply(t, h, p)
	if _, err := h.st.SetSecretValue(h.ctx, p.Workspace.ID, "db-pass", newPass); err != nil {
		t.Fatal(err)
	}
	h.svc.EnqueueApply("alice", []int64{a.ID})
	h.svc.EnqueueApply("alice", []int64{a.ID})
	n := len(h.compose.Calls())
	h.startApplier()
	h.waitApplyIdle()
	if got := h.callsSince(n); !slices.Equal(got, []string{"recreate dg-alice-shop alice-shop-db,alice-shop-web"}) {
		t.Fatalf("compose calls = %v", got)
	}
}

func TestRecoverInterruptedApply(t *testing.T) {
	for name, tc := range map[string]struct {
		tamper   bool
		wantLeft int
	}{"clean": {false, 2}, "violating": {true, 0}} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			p := h.workspace("alice", 20000, 20099)
			a := deployApply(t, h, p)
			if err := h.st.SetSecretApply(h.ctx, a.ID, "running", ""); err != nil {
				t.Fatal(err)
			}
			if tc.tamper {
				h.docker.MutateFacts("alice-shop-web", func(f *dockerx.Facts) { f.Privileged = true })
			}
			if err := h.svc.RecoverInterrupted(h.ctx); err != nil {
				t.Fatal(err)
			}
			a = h.app(p, "shop")
			if a.SecretApplyState != "interrupted" || a.SecretApplyError != "gateway stopped during apply" {
				t.Fatalf("apply state = %q %q", a.SecretApplyState, a.SecretApplyError)
			}
			if a.LastDeployResult != "ok" || len(h.docker.Containers) != tc.wantLeft {
				t.Fatalf("result %q with %d containers", a.LastDeployResult, len(h.docker.Containers))
			}
			rows, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Action: "recover_interrupted_apply"})
			if len(rows) != 1 || rows[0].Workspace != "alice" || (tc.tamper != (rows[0].Severity == "critical")) {
				t.Fatalf("audit rows = %+v", rows)
			}
		})
	}
}

func TestListShowsStaleSecrets(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	a := deployApply(t, h, p)
	h.setSecret(p, "db-pass", newPass)
	if err := h.st.ClearStale(h.ctx, a.ID, []string{"web"}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetSecretApply(h.ctx, a.ID, "failed", "login failed for "+newPass+" in "+h.root+"/alice/shop"); err != nil {
		t.Fatal(err)
	}
	res, err := h.svc.List(h.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Apps) != 1 || !slices.Equal(res.Apps[0].StaleSecrets, []string{"db.K"}) {
		t.Fatalf("apps = %+v", res.Apps)
	}
	msg := res.Apps[0].SecretApplyError
	if msg == "" || strings.Contains(msg, newPass) || strings.Contains(msg, h.root) || !strings.Contains(msg, "***") {
		t.Fatalf("secret_apply_error = %q", msg)
	}
	// Nothing stale and no failure: both fields are omitted.
	if err := h.st.ClearStale(h.ctx, a.ID, []string{"db"}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.SetSecretApply(h.ctx, a.ID, "ok", ""); err != nil {
		t.Fatal(err)
	}
	res, _ = h.svc.List(h.ctx, p)
	if res.Apps[0].StaleSecrets != nil || res.Apps[0].SecretApplyError != "" {
		t.Fatalf("apps = %+v", res.Apps)
	}
}

// A value saved while the recreate runs is not in the recreated container:
// its refs stay stale for the pass that save queues.
func TestApplyKeepsStaleWhenValueChangesDuringRecreate(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployApply(t, h, p)
	h.startApplier()
	wired := h.compose.OnRecreate
	h.compose.OnRecreate = func(file, project string, services []string) error {
		if _, err := h.st.SetSecretValue(h.ctx, p.Workspace.ID, "db-pass", "newest-password-3"); err != nil {
			return err
		}
		return wired(file, project, services)
	}
	h.changeSecret(p, "db-pass", newPass)
	if stale := staleByService(h.appRefs(p, "shop")); !stale["web"] || !stale["db"] {
		t.Fatalf("stale = %v", stale)
	}
	if a := h.app(p, "shop"); a.SecretApplyState != "ok" {
		t.Fatalf("apply state = %q %q", a.SecretApplyState, a.SecretApplyError)
	}
}
