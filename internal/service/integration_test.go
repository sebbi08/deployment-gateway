//go:build integration

package service_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/runner"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/service/servicetest"
	"github.com/sebbi08/deployment-gateway/internal/store"
	"github.com/sebbi08/deployment-gateway/internal/tmpl"
)

func detail(err error) string {
	var se *service.Error
	if errors.As(err, &se) {
		return se.Detail()
	}
	return ""
}

func TestIntegrationDeployLifecycle(t *testing.T) {
	bin := os.Getenv("COMPOSE_BIN")
	if bin == "" {
		t.Skip("COMPOSE_BIN not set")
	}
	ctx := context.Background()
	dc, err := dockerx.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := dc.Ping(ctx); err != nil {
		t.Skipf("docker unreachable: %v", err)
	}
	if out, err := exec.Command("docker", "network", "create", "dgtest-apps").CombinedOutput(); err != nil && !strings.Contains(string(out), "already exists") {
		t.Fatalf("network create: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command("docker", "network", "rm", "dgtest-apps").Run() })

	root := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "it.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dockerHost := os.Getenv("DOCKER_HOST")
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}
	run := runner.New(runner.Options{Bin: bin, DockerConfig: filepath.Join(os.Getenv("HOME"), ".docker"), DockerHost: dockerHost,
		Timeout: 2 * time.Minute, Concurrency: 1})
	svc := service.New(st, run, dc, root)

	tplID, err := svc.SaveTemplate(ctx, "itest", 0, tmpl.Template{Name: "itest-web", HostPattern: "{{app}}.{{workspace}}.itest.example",
		Networks: []string{"dgtest-apps"}, Labels: map[string]string{"itest.host": "{{host}}"}})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string, start int) service.Principal {
		w, err := svc.CreateWorkspace(ctx, "itest", name, store.BuiltinDefaultQuota(), service.PortSettings{Start: start, End: start + 9, BindIP: "127.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.SetGrants(ctx, "itest", w, []int64{tplID}); err != nil {
			t.Fatal(err)
		}
		return servicetest.Principal(t, svc, st, name, "127.0.0.1")
	}
	p := mk("itest", 28080)
	other := mk("itother", 28090)
	t.Cleanup(func() { svc.Delete(context.Background(), p, service.DeleteInput{App: "web", Confirm: "web"}) })

	stk := mustStack(t, `{"services": {"nginx": {
		"image": "nginxinc/nginx-unprivileged:1.27-alpine",
		"environment": {"GREETING": "${HOME} costs $5"},
		"expose": {"template": "itest-web", "port": 8080},
		"ports": [{"container": 8080, "host": 28080}],
		"healthcheck": {"command": ["wget", "-qO-", "http://127.0.0.1:8080/"], "interval": "5s"}}}}`)
	res, err := svc.Deploy(ctx, p, service.DeployInput{App: "web", Stack: stk})
	if err != nil {
		t.Fatalf("deploy: %v\ndetail: %s", err, detail(err))
	}
	if res.Services[0].State != "running" || res.Services[0].Health != "healthy" {
		t.Fatalf("status = %+v", res)
	}

	resp, err := http.Get("http://127.0.0.1:28080/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "nginx") {
		t.Fatalf("published port: %d %s", resp.StatusCode, body)
	}

	out, err := exec.Command("docker", "exec", "itest-web-nginx", "printenv", "GREETING").Output()
	if err != nil || strings.TrimSpace(string(out)) != "${HOME} costs $5" {
		t.Fatalf("env value must arrive literally, got %q (%v)", out, err)
	}
	insp, err := exec.Command("docker", "inspect", "--format", "{{.HostConfig.ReadonlyRootfs}} {{.HostConfig.CapDrop}} {{.HostConfig.Privileged}}", "itest-web-nginx").Output()
	if err != nil || strings.TrimSpace(string(insp)) != "true [ALL] false" {
		t.Fatalf("hardening not applied: %q %v", insp, err)
	}

	logs, err := svc.Logs(ctx, p, service.LogsInput{App: "web", Service: "nginx", Tail: 20})
	if err != nil || logs.Logs == "" {
		t.Fatalf("logs: %+v %v", logs, err)
	}
	if _, err := svc.Restart(ctx, p, service.ServiceInput{App: "web"}); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if _, err := svc.Logs(ctx, other, service.LogsInput{App: "web", Service: "nginx"}); code(err) != service.CodeNotFound {
		t.Fatalf("cross-workspace logs: %v", err)
	}

	// A second app of the workspace may not publish the same host port.
	clash := mustStack(t, `{"services": {"nginx": {"image": "nginxinc/nginx-unprivileged:1.27-alpine",
		"ports": [{"container": 8080, "host": 28080}]}}}`)
	if _, err := svc.Deploy(ctx, p, service.DeployInput{App: "web2", Stack: clash}); code(err) != service.CodePortTaken {
		t.Fatalf("port conflict: %v", err)
	}

	stopped, err := svc.Stop(ctx, p, service.ServiceInput{App: "web"})
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if stopped.Services[0].State == "running" {
		t.Fatalf("still running after stop: %+v", stopped)
	}
	if _, err := http.Get("http://127.0.0.1:28080/"); err == nil {
		t.Fatal("published port still answers after stop")
	}

	if _, err := svc.Delete(ctx, p, service.DeleteInput{App: "web", Confirm: "web"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	left, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=deploy-gateway.workspace=itest").Output()
	if strings.TrimSpace(string(left)) != "" {
		t.Fatalf("containers left after delete: %s", left)
	}
	if _, err := os.Stat(filepath.Join(root, "itest", "web")); !os.IsNotExist(err) {
		t.Fatal("app folder left after delete")
	}
}

func TestIntegrationSecrets(t *testing.T) {
	bin := os.Getenv("COMPOSE_BIN")
	if bin == "" {
		t.Skip("COMPOSE_BIN not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dc, err := dockerx.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := dc.Ping(ctx); err != nil {
		t.Skipf("docker unreachable: %v", err)
	}
	root := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "it.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dockerHost := os.Getenv("DOCKER_HOST")
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}
	run := runner.New(runner.Options{Bin: bin, DockerConfig: filepath.Join(os.Getenv("HOME"), ".docker"), DockerHost: dockerHost,
		Timeout: 2 * time.Minute, Concurrency: 1})
	svc := service.New(st, run, dc, root)
	svc.ApplyLockWait = 30 * time.Second
	svc.StartSecretApplier(ctx)

	w, err := svc.CreateWorkspace(ctx, "itest", "itsecret", store.BuiltinDefaultQuota(), service.PortSettings{Start: 28100, End: 28109, BindIP: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	p := servicetest.Principal(t, svc, st, "itsecret", "127.0.0.1")
	t.Cleanup(func() { svc.Delete(context.Background(), p, service.DeleteInput{App: "sec", Confirm: "sec"}) })

	const newValue = "new-secret-value"
	const value = "pa$$ss \"q\" #x = y"
	if err := svc.CreateSecret(ctx, "itest", w, "s", value); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	stk := mustStack(t, `{"services": {
		"sleeper": {"image": "busybox:1.37", "command": ["sleep", "300"], "secrets": {"A": "s"}},
		"echoer": {"image": "busybox:1.37", "command": ["sh", "-c", "echo \"value=$A\"; sleep 300"], "secrets": {"A": "s"}}}}`)
	if _, err := svc.Deploy(ctx, p, service.DeployInput{App: "sec", Stack: stk}); err != nil {
		t.Fatalf("deploy: %v\ndetail: %s", err, detail(err))
	}

	const ctr = "itsecret-sec-sleeper"
	readA := func() string {
		out, err := exec.Command("docker", "exec", ctr, "sh", "-c", `printf %s "$A"`).Output()
		if err != nil {
			t.Fatalf("docker exec: %v", err)
		}
		return string(out)
	}
	id := func() string {
		out, err := exec.Command("docker", "inspect", "--format", "{{.Id}}", ctr).Output()
		if err != nil {
			t.Fatalf("docker inspect: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	if got := readA(); got != value {
		t.Fatalf("$A = %q, want %q", got, value)
	}
	before := id()

	if _, err := svc.SetSecret(ctx, "itest", w, "s", newValue); err != nil {
		t.Fatalf("set secret: %v", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		app, err := st.GetApp(ctx, w.ID, "sec")
		if err != nil {
			t.Fatal(err)
		}
		// The container name briefly does not exist during a recreate: tolerate inspect errors.
		cur, _ := exec.Command("docker", "inspect", "--format", "{{.Id}}", ctr).Output()
		if cid := strings.TrimSpace(string(cur)); app.SecretApplyState == "ok" && cid != "" && cid != before {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("apply did not finish: state=%q err=%q", app.SecretApplyState, app.SecretApplyError)
		}
		time.Sleep(time.Second)
	}
	if got := readA(); got != newValue {
		t.Fatalf("$A after apply = %q, want %q", got, newValue)
	}

	logs, err := svc.Logs(ctx, p, service.LogsInput{App: "sec", Service: "echoer", Tail: 20})
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if !strings.Contains(logs.Logs, "***") || strings.Contains(logs.Logs, newValue) || strings.Contains(logs.Logs, value) {
		t.Fatalf("logs not redacted: %q", logs.Logs)
	}
}
