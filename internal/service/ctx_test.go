package service_test

import (
	"context"
	"sync"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/service"
)

// A client disconnect must not abort compose half-way: pull and up run
// detached from the request context (the runner enforces its own timeout).
func TestDeployComposeSurvivesRequestCancel(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	var mu sync.Mutex
	errs := map[string]error{}
	h.compose.ObserveCtx = func(op string, c context.Context) {
		if op == "pull" {
			cancel() // the client goes away while images are pulled
		}
		mu.Lock()
		errs[op] = c.Err()
		mu.Unlock()
	}
	_, _ = h.svc.Deploy(ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	for _, op := range []string{"pull", "up"} {
		if e, ok := errs[op]; !ok || e != nil {
			t.Fatalf("%s: ran=%v ctx err=%v", op, ok, e)
		}
	}
	app, err := h.st.GetApp(h.ctx, p.Workspace.ID, "shop")
	if err != nil || app.LastDeployResult != "ok" {
		t.Fatalf("deploy result = %q (%v), want ok", app.LastDeployResult, err)
	}
}

func TestLifecycleComposeIsDetachedFromRequest(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	detached := map[string]bool{}
	h.compose.ObserveCtx = func(op string, c context.Context) { detached[op] = c.Done() == nil }
	if _, err := h.svc.Restart(ctx, p, service.ServiceInput{App: "shop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Stop(ctx, p, service.ServiceInput{App: "shop", Service: "web"}); err != nil {
		t.Fatal(err)
	}
	if !detached["restart"] || !detached["stop"] {
		t.Fatalf("compose ops must not be cancellable by the request: %v", detached)
	}
}
