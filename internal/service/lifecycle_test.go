package service_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/names"
	"github.com/sebbi08/deployment-gateway/internal/service"
)

func deployExample(t *testing.T, h *harness, p service.Principal) {
	t.Helper()
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)}); err != nil {
		t.Fatal(err)
	}
}

func TestListSurvivesTemplateRevocation(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	if err := h.svc.SetGrants(h.ctx, "admin", p.Workspace, nil); err != nil {
		t.Fatal(err)
	}
	res, err := h.svc.List(h.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Apps) != 1 || res.Apps[0].Services[1].URL != "https://shop.alice.apps.example.com" || res.Apps[0].LastDeployResult != "ok" {
		t.Fatalf("list = %+v", res)
	}
	_, err = h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if code(err) != service.CodeValidation || !strings.Contains(err.Error(), "public-web") {
		t.Fatalf("redeploy with revoked template: %v", err)
	}
}

func TestCrossWorkspaceAccessIsNotFound(t *testing.T) {
	h := newHarness(t)
	alice := h.workspace("alice", 20000, 20099)
	bob := h.workspace("bob", 20100, 20199)
	bobStack := strings.Replace(exampleStack, `"host": 20001`, `"host": 20101`, 1)
	if _, err := h.svc.Deploy(h.ctx, bob, service.DeployInput{App: "shop", Stack: mustStack(t, bobStack)}); err != nil {
		t.Fatal(err)
	}
	before := len(h.docker.Touched())
	if _, err := h.svc.Logs(h.ctx, alice, service.LogsInput{App: "shop", Service: "web"}); code(err) != service.CodeNotFound {
		t.Fatalf("logs: %v", err)
	}
	if _, err := h.svc.Restart(h.ctx, alice, service.ServiceInput{App: "shop"}); code(err) != service.CodeNotFound {
		t.Fatalf("restart: %v", err)
	}
	if _, err := h.svc.Delete(h.ctx, alice, service.DeleteInput{App: "shop", Confirm: "shop"}); code(err) != service.CodeNotFound {
		t.Fatalf("delete: %v", err)
	}
	deployExample(t, h, alice)
	if _, err := h.svc.Logs(h.ctx, alice, service.LogsInput{App: "shop", Service: "admin"}); code(err) != service.CodeNotFound {
		t.Fatalf("unknown service: %v", err)
	}
	for _, id := range h.docker.Touched()[before:] {
		if strings.HasPrefix(id, "bob-") {
			t.Fatalf("alice's calls touched bob's container %s", id)
		}
	}
	if _, ok := h.docker.Containers["bob-shop-web"]; !ok {
		t.Fatal("bob's container was removed")
	}
}

func TestLogs(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	h.docker.LogsOut = "hello from web\n"
	res, err := h.svc.Logs(h.ctx, p, service.LogsInput{App: "shop", Service: "web", Tail: 5000, Since: "10m"})
	if err != nil || res.Logs != "hello from web\n" {
		t.Fatalf("logs = %+v, %v", res, err)
	}
	if _, err := h.svc.Logs(h.ctx, p, service.LogsInput{App: "shop", Service: "web", Since: "yesterday"}); code(err) != service.CodeValidation {
		t.Fatalf("bad since: %v", err)
	}
}

func TestRestartAndStop(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	res, err := h.svc.Stop(h.ctx, p, service.ServiceInput{App: "shop", Service: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Services[1].State != "exited" || res.Services[0].State != "running" {
		t.Fatalf("after stop = %+v", res)
	}
	if !slices.Contains(h.compose.Calls(), "stop dg-alice-shop alice-shop-web") {
		t.Fatalf("calls = %v", h.compose.Calls())
	}
	res, err = h.svc.Restart(h.ctx, p, service.ServiceInput{App: "shop"})
	if err != nil || res.Services[1].State != "running" {
		t.Fatalf("after restart = %+v, %v", res, err)
	}
}

func TestDelete(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	if _, err := h.svc.Delete(h.ctx, p, service.DeleteInput{App: "shop", Confirm: "shp"}); code(err) != service.CodeValidation {
		t.Fatalf("confirm mismatch: %v", err)
	}
	res, err := h.svc.Delete(h.ctx, p, service.DeleteInput{App: "shop", Confirm: "shop"})
	if err != nil || !res.Deleted {
		t.Fatalf("delete = %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "alice", "shop")); !os.IsNotExist(err) {
		t.Fatal("app folder not removed")
	}
	if claims, _ := h.st.ListPortClaims(h.ctx, p.Workspace.ID); len(claims) != 0 {
		t.Fatal("port claims not released")
	}
	if len(h.docker.Containers) != 0 {
		t.Fatalf("containers left: %v", h.docker.Containers)
	}
}

// Control bytes and invalid UTF-8 inflate the encoded response many times
// over; logs keep printable text, newlines and tabs only.
func TestLogsAreNormalised(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	h.docker.LogsOut = "a\x01b\x1b[31mred\n\xff\tok\r\n"
	res, err := h.svc.Logs(h.ctx, p, service.LogsInput{App: "shop", Service: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "ab[31mred\n\uFFFD\tok\n"; res.Logs != want {
		t.Fatalf("logs = %q, want %q", res.Logs, want)
	}
}

// Logs are bounded for the agent: tail defaults to 200 lines and is capped
// at 1000, and the output is capped at 256 KiB.
func TestLogsBoundTailAndSize(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	var gotTail, gotMax int
	h.hookDocker().onLogs = func(_ string, tail int, _ string, maxBytes int) (string, error) {
		gotTail, gotMax = tail, maxBytes
		return "", nil
	}
	for in, want := range map[int]int{0: 200, -5: 200, 50: 50, 1000: 1000, 5000: 1000} {
		if _, err := h.svc.Logs(h.ctx, p, service.LogsInput{App: "shop", Service: "web", Tail: in}); err != nil {
			t.Fatal(err)
		}
		if gotTail != want || gotMax != 256<<10 {
			t.Errorf("tail %d: docker asked for tail %d, max %d bytes; want %d, %d", in, gotTail, gotMax, want, 256<<10)
		}
	}
}

// Restart, stop and delete take the per-app lock like deploy: compose must
// never run two operations on one project at once.
func TestLifecycleConflictWhenLocked(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	unlock, _ := h.compose.TryLock("alice/shop")
	defer unlock()
	before := len(h.compose.Calls())
	if _, err := h.svc.Restart(h.ctx, p, service.ServiceInput{App: "shop"}); code(err) != service.CodeConflict {
		t.Errorf("restart: %v", err)
	}
	if _, err := h.svc.Stop(h.ctx, p, service.ServiceInput{App: "shop", Service: "web"}); code(err) != service.CodeConflict {
		t.Errorf("stop: %v", err)
	}
	if _, err := h.svc.Delete(h.ctx, p, service.DeleteInput{App: "shop", Confirm: "shop"}); code(err) != service.CodeConflict {
		t.Errorf("delete: %v", err)
	}
	if calls := h.compose.Calls(); len(calls) != before || len(h.docker.Containers) != 2 {
		t.Fatalf("compose ran or containers changed while locked: %v", calls[before:])
	}
}

// Logs need the app record: containers the gateway no longer tracks (an
// interrupted delete, a lost DB row) are not readable through logs.
func TestLogsRequireAppRecord(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	labels := map[string]string{names.LabelWorkspace: "alice", names.LabelApp: "ghost", names.LabelService: "web", names.LabelManaged: "true"}
	h.docker.Add(dockerx.ContainerInfo{ID: "alice-ghost-web", Name: "alice-ghost-web", Labels: labels}, dockerx.Facts{})
	if _, err := h.svc.Logs(h.ctx, p, service.LogsInput{App: "ghost", Service: "web"}); code(err) != service.CodeNotFound {
		t.Fatalf("logs of an untracked app: %v", err)
	}
	if slices.Contains(h.docker.Touched(), "alice-ghost-web") {
		t.Fatal("logs read a container without an app record")
	}
}

// Service lookups re-check the owner labels instead of trusting Docker's
// label filter: another workspace's container never answers for this app.
func TestServiceLookupRechecksOwnerLabels(t *testing.T) {
	h := newHarness(t)
	alice := h.workspace("alice", 20000, 20099)
	bob := h.workspace("bob", 20100, 20199)
	bobStack := strings.Replace(exampleStack, `"host": 20001`, `"host": 20101`, 1)
	if _, err := h.svc.Deploy(h.ctx, bob, service.DeployInput{App: "shop", Stack: mustStack(t, bobStack)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Deploy(h.ctx, alice, service.DeployInput{App: "shop", Stack: mustStack(t, `{"services": {"db": {"image": "postgres:16.4"}}}`)}); err != nil {
		t.Fatal(err)
	}
	// A label filter that leaks every managed container.
	h.hookDocker().onProjectContainers = func(string, string) ([]dockerx.ContainerInfo, error) {
		return h.docker.ManagedContainers(h.ctx)
	}
	before := len(h.docker.Touched())
	if _, err := h.svc.Logs(h.ctx, alice, service.LogsInput{App: "shop", Service: "web"}); code(err) != service.CodeNotFound {
		t.Errorf("logs: %v", err)
	}
	if _, err := h.svc.Restart(h.ctx, alice, service.ServiceInput{App: "shop", Service: "web"}); code(err) != service.CodeNotFound {
		t.Errorf("restart: %v", err)
	}
	for _, id := range h.docker.Touched()[before:] {
		if strings.HasPrefix(id, "bob-") {
			t.Fatalf("alice's calls touched bob's container %s", id)
		}
	}
}

// Restart and stop of a service the app does not run are not_found and
// never reach compose.
func TestLifecycleUnknownServiceIsNotFound(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	before := len(h.compose.Calls())
	if _, err := h.svc.Restart(h.ctx, p, service.ServiceInput{App: "shop", Service: "cache"}); code(err) != service.CodeNotFound {
		t.Errorf("restart: %v", err)
	}
	if _, err := h.svc.Stop(h.ctx, p, service.ServiceInput{App: "shop", Service: "cache"}); code(err) != service.CodeNotFound {
		t.Errorf("stop: %v", err)
	}
	if calls := h.compose.Calls(); len(calls) != before {
		t.Fatalf("compose ran: %v", calls[before:])
	}
}

// With several apps in a workspace, each app's status and list entry show
// only that app's published ports.
func TestStatusShowsOnlyOwnPorts(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p) // shop/web publishes 20001
	blog := `{"services": {"web": {"image": "a:1", "ports": [{"container": 80, "host": 20002}]}}}`
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "blog", Stack: mustStack(t, blog)}); err != nil {
		t.Fatal(err)
	}
	webPorts := func(svcs []service.ServiceStatus) []int {
		var out []int
		for _, s := range svcs {
			for _, c := range s.Ports {
				if s.Service == "web" {
					out = append(out, c.HostPort)
				}
			}
		}
		return out
	}
	res, err := h.svc.Restart(h.ctx, p, service.ServiceInput{App: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if got := webPorts(res.Services); !slices.Equal(got, []int{20001}) {
		t.Errorf("restart status ports = %v, want [20001]", got)
	}
	list, err := h.svc.List(h.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]int{"blog": {20002}, "shop": {20001}}
	for _, a := range list.Apps {
		if got := webPorts(a.Services); !slices.Equal(got, want[a.App]) {
			t.Errorf("list %s ports = %v, want %v", a.App, got, want[a.App])
		}
	}
}
