package service_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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
