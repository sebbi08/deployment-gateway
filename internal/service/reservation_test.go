package service_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

// bigStack uses 2x1024m / 2x1 CPU and host port 20001; smallStack 64m / 0.1 CPU
// on the given host port.
const bigStack = `{"services": {
  "a": {"image": "busybox:1.36", "resources": {"memory": "1024m", "cpus": 1}, "ports": [{"container": 80, "host": 20001}]},
  "b": {"image": "busybox:1.36", "resources": {"memory": "1024m", "cpus": 1}}
}}`

func smallStack(image string, port int) string {
	return fmt.Sprintf(`{"services": {"s": {"image": %q, "resources": {"memory": "64m", "cpus": 0.1},
  "ports": [{"container": 80, "host": %d}]}}}`, image, port)
}

func (h *harness) usage(p service.Principal) store.Usage {
	h.t.Helper()
	u, err := h.st.WorkspaceUsage(h.ctx, p.Workspace.ID, "")
	if err != nil {
		h.t.Fatal(err)
	}
	return u
}

func (h *harness) hostPorts(p service.Principal) []int {
	h.t.Helper()
	cs, err := h.st.ListPortClaims(h.ctx, p.Workspace.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	out := []int{}
	for _, c := range cs {
		out = append(out, c.HostPort)
	}
	return out
}

// A redeploy that fails before up leaves the old containers running, so the
// old reservation (memory, CPUs, port claims) must stay in place.
func TestDeployFailureBeforeUpKeepsReservation(t *testing.T) {
	for name, fail := range map[string]func(h *harness){
		"pull":         func(h *harness) { h.compose.PullErr = errors.New("pull failed") },
		"image labels": func(h *harness) { h.docker.Images["busybox:1.37"] = map[string]string{"traefik.enable": "true"} },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			p := h.workspace("alice", 20000, 20099)
			if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, bigStack)}); err != nil {
				t.Fatal(err)
			}
			fail(h)
			if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, smallStack("busybox:1.37", 20002))}); err == nil {
				t.Fatal("redeploy should fail")
			}
			if u := h.usage(p); u.MemoryBytes != 2048<<20 || u.CPUs != 2 {
				t.Fatalf("reservation lowered while old containers run: %+v", u)
			}
			if got := h.hostPorts(p); len(got) != 1 || got[0] != 20001 {
				t.Fatalf("port claims = %v, want the running stack's [20001]", got)
			}
			app, _ := h.st.GetApp(h.ctx, p.Workspace.ID, "shop")
			if app.LastDeployResult != "ok" {
				t.Fatalf("last result = %q: the running stack is still the last good one", app.LastDeployResult)
			}
		})
	}
}

// A first deploy that fails before up must not use an app slot or quota.
func TestDeployFailureBeforeUpOfNewAppLeavesNoRecord(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.compose.PullErr = errors.New("pull failed")
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, bigStack)}); code(err) != service.CodeDeployFailed {
		t.Fatalf("want deploy_failed, got %v", err)
	}
	if _, err := h.st.GetApp(h.ctx, p.Workspace.ID, "shop"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed first deploy left an app record: %v", err)
	}
	if u := h.usage(p); u.Apps != 0 || u.MemoryBytes != 0 || len(h.hostPorts(p)) != 0 {
		t.Fatalf("usage = %+v, claims = %v", u, h.hostPorts(p))
	}
}

// The same services and ports as bigStack, with small limits: if up fails
// before recreating them, the old 1024m containers pass the runtime check.
const shrunkStack = `{"services": {
  "a": {"image": "busybox:1.36", "resources": {"memory": "64m", "cpus": 0.1}, "ports": [{"container": 80, "host": 20001}]},
  "b": {"image": "busybox:1.36", "resources": {"memory": "64m", "cpus": 0.1}}
}}`

// When up fails, containers with the old limits may still run: keep charging
// the larger reservation until a deploy succeeds.
func TestDeployUpFailureKeepsLargerReservation(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, bigStack)}); err != nil {
		t.Fatal(err)
	}
	h.compose.UpErr = errors.New("unhealthy")
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, shrunkStack)}); code(err) != service.CodeDeployFailed {
		t.Fatalf("want deploy_failed, got %v", err)
	}
	if u := h.usage(p); u.MemoryBytes != 2048<<20 || u.CPUs != 2 {
		t.Fatalf("usage after failed up = %+v, want the larger (old) reservation", u)
	}
	h.compose.UpErr = nil
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, shrunkStack)}); err != nil {
		t.Fatal(err)
	}
	if u := h.usage(p); u.MemoryBytes != 128<<20 || u.CPUs < 0.199 || u.CPUs > 0.201 {
		t.Fatalf("usage after successful deploy = %+v, want exactly the new stack", u)
	}
}
