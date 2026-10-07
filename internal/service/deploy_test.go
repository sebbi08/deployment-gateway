package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

func TestDeployHappyPath(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	res, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(h.root, "alice", "shop", "docker-compose.yml")
	if calls := h.compose.Calls(); len(calls) != 2 || calls[1] != "up dg-alice-shop "+file {
		t.Fatalf("compose calls = %v", calls)
	}
	data, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(data), "cap_drop") {
		t.Fatalf("compose file: %v\n%s", err, data)
	}
	if _, err := os.Stat(filepath.Join(h.root, "alice", "shop", "data", "db")); err != nil {
		t.Fatalf("volume dir missing: %v", err)
	}
	if !slices.Equal(h.chowns, []string{"db 999:999"}) {
		t.Fatalf("chowns = %v", h.chowns)
	}
	if len(res.Services) != 2 || res.Services[0].Service != "db" || res.Services[0].State != "running" ||
		res.Services[1].URL != "https://shop.alice.apps.example.com" || len(res.Services[1].Ports) != 1 {
		t.Fatalf("result = %+v", res)
	}
	app, _ := h.st.GetApp(h.ctx, p.Workspace.ID, "shop")
	if app.LastDeployResult != "ok" {
		t.Fatalf("app = %+v", app)
	}
}

func TestDeployRedeployReleasesRemovedServicePorts(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	two := `{"services": {"web": {"image": "a:1", "ports": [{"container": 80, "host": 20001}]}, "worker": {"image": "b:1", "ports": [{"container": 81, "host": 20002}]}}}`
	one := `{"services": {"web": {"image": "a:1", "ports": [{"container": 80, "host": 20001}]}}}`
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "app", Stack: mustStack(t, two)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "app", Stack: mustStack(t, one)}); err != nil {
		t.Fatal(err)
	}
	claims, _ := h.st.ListPortClaims(h.ctx, p.Workspace.ID)
	if len(claims) != 1 || claims[0].HostPort != 20001 {
		t.Fatalf("claims = %+v", claims)
	}
	u, _ := h.st.WorkspaceUsage(h.ctx, p.Workspace.ID, "")
	if u.CPUs != 0.5 {
		t.Fatalf("usage still counts removed service: %+v", u)
	}
	if _, ok := h.docker.Containers["alice-app-worker"]; ok {
		t.Fatal("removed service container still present")
	}
	other := `{"services": {"w": {"image": "a:1", "ports": [{"container": 80, "host": 20002}]}}}`
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "other", Stack: mustStack(t, other)}); err != nil {
		t.Fatalf("released port should be claimable: %v", err)
	}
}

func TestDeployFailureReleasesLockAndKeepsLastGoodPrev(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	version := func(v string) service.DeployInput {
		return service.DeployInput{App: "shop", Stack: mustStack(t, strings.Replace(exampleStack, "shop:1.4.2", "shop:"+v, 1))}
	}
	if _, err := h.svc.Deploy(h.ctx, p, version("1.0.0")); err != nil {
		t.Fatal(err)
	}
	h.compose.UpErr = errors.New("compose up failed with exit code 1")
	h.compose.UpOut = "pull access denied for " + h.root + "/secret"
	_, err := h.svc.Deploy(h.ctx, p, version("2.0.0"))
	if code(err) != service.CodeDeployFailed || !strings.Contains(err.Error(), "docker-compose.prev.yml") {
		t.Fatalf("expected deploy_failed mentioning prev: %v", err)
	}
	if strings.Contains(err.Error(), h.root) {
		t.Fatalf("host path leaked to agent: %v", err)
	}
	h.compose.UpErr, h.compose.UpOut = nil, ""
	if _, err := h.svc.Deploy(h.ctx, p, version("3.0.0")); err != nil {
		t.Fatalf("retry after failure must succeed (lock released): %v", err)
	}
	dir := filepath.Join(h.root, "alice", "shop")
	prev, _ := os.ReadFile(filepath.Join(dir, "docker-compose.prev.yml"))
	cur, _ := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if !strings.Contains(string(prev), "shop:1.0.0") {
		t.Fatalf("prev must hold the last successful render (1.0.0):\n%s", prev)
	}
	if !strings.Contains(string(cur), "shop:3.0.0") {
		t.Fatalf("current must be 3.0.0:\n%s", cur)
	}
}

func TestDeployRuntimeViolationTearsDown(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.compose.AfterUp = func() {
		h.docker.MutateFacts("alice-shop-web", func(f *dockerx.Facts) { f.Privileged = true })
	}
	_, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if code(err) != service.CodeSecurity {
		t.Fatalf("expected security_violation, got %v", err)
	}
	if strings.Contains(err.Error(), "privileged") {
		t.Fatal("violation details must not be sent to the agent")
	}
	calls := h.compose.Calls()
	if calls[len(calls)-1] != "down dg-alice-shop" {
		t.Fatalf("containers not torn down: %v", calls)
	}
	crit, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Severity: "critical"})
	if len(crit) != 1 || !strings.Contains(crit[0].Error, "privileged") {
		t.Fatalf("critical audit = %+v", crit)
	}
}

func TestDeployConflictWhenLocked(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	unlock, _ := h.compose.TryLock("alice/shop")
	defer unlock()
	_, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if code(err) != service.CodeConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestDeployPortTaken(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)}); err != nil {
		t.Fatal(err)
	}
	blog := service.DeployInput{App: "blog", Stack: mustStack(t, `{"services": {"web": {"image": "a:1", "ports": [{"container": 80, "host": 20001}]}}}`)}
	if _, err := h.svc.Validate(h.ctx, p, blog); code(err) != service.CodePortTaken {
		t.Fatalf("validate: expected port_taken, got %v", err)
	}
	if _, err := h.svc.Deploy(h.ctx, p, blog); code(err) != service.CodePortTaken {
		t.Fatalf("deploy: expected port_taken, got %v", err)
	}
	if _, err := h.st.GetApp(h.ctx, p.Workspace.ID, "blog"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("failed deploy left an app record")
	}
}

func TestDeployQuotaCountsOtherApps(t *testing.T) {
	h := newHarness(t)
	q := store.BuiltinDefaultQuota()
	q.MaxApps = 1
	p := h.workspaceWithQuota("alice", 0, 0, q)
	simple := `{"services": {"web": {"image": "a:1"}}}`
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, simple)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "blog", Stack: mustStack(t, simple)}); code(err) != service.CodeQuota {
		t.Fatalf("expected quota_exceeded, got %v", err)
	}
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, simple)}); err != nil {
		t.Fatalf("redeploy of existing app must not count twice: %v", err)
	}
}

func TestDeployCancelledUpKeepsLastGoodPrev(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	version := func(v string) service.DeployInput {
		return service.DeployInput{App: "shop", Stack: mustStack(t, strings.Replace(exampleStack, "shop:1.4.2", "shop:"+v, 1))}
	}
	if _, err := h.svc.Deploy(h.ctx, p, version("1.0.0")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	var during string
	h.compose.OnUp = func(file, project string) error {
		app, _ := h.st.GetApp(h.ctx, p.Workspace.ID, "shop")
		during = app.LastDeployResult
		cancel()
		return errors.New("context canceled")
	}
	if _, err := h.svc.Deploy(ctx, p, version("2.0.0")); code(err) != service.CodeDeployFailed {
		t.Fatalf("expected deploy_failed, got %v", err)
	}
	if during != "pending" {
		t.Fatalf("status during up = %q, want pending", during)
	}
	app, _ := h.st.GetApp(h.ctx, p.Workspace.ID, "shop")
	if app.LastDeployResult != "failed" {
		t.Fatalf("status after cancelled deploy = %q", app.LastDeployResult)
	}
	h.compose.OnUp = nil
	if _, err := h.svc.Deploy(h.ctx, p, version("3.0.0")); err != nil {
		t.Fatal(err)
	}
	prev, _ := os.ReadFile(filepath.Join(h.root, "alice", "shop", "docker-compose.prev.yml"))
	if !strings.Contains(string(prev), "shop:1.0.0") {
		t.Fatalf("prev must still hold 1.0.0:\n%s", prev)
	}
}

func TestDeployFailedUpStillRuntimeChecked(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)}); err != nil {
		t.Fatal(err)
	}
	h.docker.MutateFacts("alice-shop-web", func(f *dockerx.Facts) { f.Privileged = true })
	h.compose.UpErr = errors.New("unhealthy")
	_, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if code(err) != service.CodeSecurity {
		t.Fatalf("expected security_violation, got %v", err)
	}
	calls := h.compose.Calls()
	if calls[len(calls)-1] != "down dg-alice-shop" {
		t.Fatalf("containers not torn down: %v", calls)
	}
}

// compose down waits for the global compose slot, so containers that failed
// the runtime check must be removed directly first.
func TestDeployRuntimeViolationRemovesContainersBeforeComposeDown(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.compose.AfterUp = func() {
		h.docker.MutateFacts("alice-shop-web", func(f *dockerx.Facts) { f.Privileged = true })
	}
	wiredDown := h.compose.OnDown
	remaining := -1
	h.compose.OnDown = func(project string) {
		remaining = len(h.docker.Containers)
		wiredDown(project)
	}
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)}); code(err) != service.CodeSecurity {
		t.Fatalf("expected security_violation, got %v", err)
	}
	if remaining != 0 {
		t.Fatalf("%d containers still existed when compose down ran", remaining)
	}
}

// compose up --wait can exit 0 although a service is not healthy (seen with
// compose v5.2.0 at the wait timeout, and for restarting containers without
// a healthcheck); the deploy must not be recorded as ok then.
func TestDeployRequiresRunningAndHealthyServices(t *testing.T) {
	cases := map[string]func(h *harness){
		"health still starting": func(h *harness) {
			c := h.docker.Containers["alice-shop-web"]
			c.Health = "starting"
			h.docker.Containers["alice-shop-web"] = c
		},
		"crash looping without healthcheck": func(h *harness) {
			c := h.docker.Containers["alice-shop-db"]
			c.State = "restarting"
			h.docker.Containers["alice-shop-db"] = c
		},
		"service without container": func(h *harness) { delete(h.docker.Containers, "alice-shop-db") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			p := h.workspace("alice", 20000, 20099)
			h.compose.AfterUp = func() { mutate(h) }
			_, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
			if code(err) != service.CodeDeployFailed {
				t.Fatalf("want deploy_failed, got %v", err)
			}
			app, _ := h.st.GetApp(h.ctx, p.Workspace.ID, "shop")
			if app.LastDeployResult != "failed" {
				t.Fatalf("last result = %q, want failed", app.LastDeployResult)
			}
		})
	}
}

// ageContainers marks every existing container as created an hour ago, like
// containers of an earlier deploy on a real daemon.
func ageContainers(h *harness) {
	for id, c := range h.docker.Containers {
		c.Created = time.Now().Add(-time.Hour)
		h.docker.Containers[id] = c
	}
}

// up can fail before it replaces the old containers (a host port in use, no
// free network pool). Those containers passed their own runtime check; they
// must not be reported as a critical violation and torn down.
func TestDeployFailedUpKeepsOldContainersWithoutFalseViolation(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	ageContainers(h)
	h.compose.UpErr = errors.New("port is already allocated")
	moved := strings.Replace(exampleStack, `"host": 20001`, `"host": 20002`, 1)
	_, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, moved)})
	if code(err) != service.CodeDeployFailed {
		t.Fatalf("want deploy_failed, got %v", err)
	}
	if slices.Contains(h.compose.Calls(), "down dg-alice-shop") || len(h.docker.Containers) != 2 {
		t.Fatalf("old containers were torn down: calls=%v containers=%d", h.compose.Calls(), len(h.docker.Containers))
	}
}

func TestDeployFailedUpStillChecksOldContainersInvariants(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	ageContainers(h)
	h.docker.MutateFacts("alice-shop-db", func(f *dockerx.Facts) { f.Privileged = true })
	h.compose.UpErr = errors.New("port is already allocated")
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)}); code(err) != service.CodeSecurity {
		t.Fatalf("privileged pre-existing container: want security_violation, got %v", err)
	}
}

// A busy compose slot must not turn a successful direct removal into an
// "administrator must intervene" alarm.
func TestDeployViolationReportsRemovalWhenComposeDownIsBusy(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.compose.AfterUp = func() {
		h.docker.MutateFacts("alice-shop-web", func(f *dockerx.Facts) { f.Privileged = true })
	}
	h.compose.DownErr = errors.New("compose down: the gateway is busy with other operations; retry later")
	_, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if code(err) != service.CodeSecurity || strings.Contains(err.Error(), "administrator must intervene") || len(h.docker.Containers) != 0 {
		t.Fatalf("got %v with %d containers left", err, len(h.docker.Containers))
	}
}

// compose down can fail (empty compose file after a crash, busy compose
// slot); delete must still remove the app, removing its containers by label.
func TestDeleteSucceedsWhenComposeDownFails(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	h.compose.DownErr = errors.New("compose down: the gateway is busy with other operations; retry later")
	if _, err := h.svc.Delete(h.ctx, p, service.DeleteInput{App: "shop", Confirm: "shop"}); err != nil {
		t.Fatalf("delete with failing compose down: %v", err)
	}
	if len(h.docker.Containers) != 0 {
		t.Fatalf("%d containers left", len(h.docker.Containers))
	}
	if _, err := h.st.GetApp(h.ctx, p.Workspace.ID, "shop"); err == nil {
		t.Fatal("app record left")
	}
}

// A service still starting when --wait gives up is usually the culprit; its
// logs belong in the failure message.
func TestFailureLogsIncludeStartingContainers(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.compose.AfterUp = func() {
		c := h.docker.Containers["alice-shop-web"]
		c.Health = "starting"
		h.docker.Containers["alice-shop-web"] = c
	}
	h.docker.LogsOut = "web: waiting for database\n"
	_, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if code(err) != service.CodeDeployFailed || !strings.Contains(err.Error(), "waiting for database") {
		t.Fatalf("failure message lacks the starting container's logs: %v", err)
	}
}
