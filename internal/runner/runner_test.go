package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCompose writes a script that records its argv and environment into
// dir, then behaves according to body.
func fakeCompose(t *testing.T, body string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "docker-compose")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + dir + "/args\nenv > " + dir + "/env\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpArgvAndScrubbedEnv(t *testing.T) {
	t.Setenv("COMPOSE_PROJECT_NAME", "evil")
	t.Setenv("COMPOSE_FILE", "/etc/evil.yml")
	bin, dir := fakeCompose(t, "echo started")
	r := New(Options{Bin: bin, DockerConfig: "/docker", DockerHost: "unix:///var/run/docker.sock", Timeout: 120 * time.Second, Concurrency: 1})
	out, err := r.Up(context.Background(), "/ws/alice/shop/docker-compose.yml", "dg-alice-shop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "started") {
		t.Fatalf("output = %q", out)
	}
	wantArgs := "-f\n/ws/alice/shop/docker-compose.yml\n-p\ndg-alice-shop\nup\n-d\n--pull\nalways\n--remove-orphans\n--wait\n--wait-timeout\n120\n"
	if got := read(t, dir+"/args"); got != wantArgs {
		t.Fatalf("args =\n%s\nwant\n%s", got, wantArgs)
	}
	env := read(t, dir+"/env")
	if strings.Contains(env, "COMPOSE_") {
		t.Fatalf("COMPOSE_* leaked into env:\n%s", env)
	}
	if !strings.Contains(env, "DOCKER_CONFIG=/docker") || !strings.Contains(env, "DOCKER_HOST=unix:///var/run/docker.sock") {
		t.Fatalf("env missing docker settings:\n%s", env)
	}
}

func TestDownRestartStopArgv(t *testing.T) {
	bin, dir := fakeCompose(t, "")
	r := New(Options{Bin: bin, Timeout: time.Minute, Concurrency: 1})
	ctx := context.Background()
	cases := []struct {
		run  func() (string, error)
		want string
	}{
		{func() (string, error) { return r.Down(ctx, "/f.yml", "dg-a-b") }, "down\n-v\n--remove-orphans\n"},
		{func() (string, error) { return r.Restart(ctx, "/f.yml", "dg-a-b", "") }, "restart\n"},
		{func() (string, error) { return r.Restart(ctx, "/f.yml", "dg-a-b", "a-b-web") }, "restart\na-b-web\n"},
		{func() (string, error) { return r.Stop(ctx, "/f.yml", "dg-a-b", "a-b-web") }, "stop\na-b-web\n"},
	}
	for _, c := range cases {
		if _, err := c.run(); err != nil {
			t.Fatal(err)
		}
		if got := read(t, dir+"/args"); got != "-f\n/f.yml\n-p\ndg-a-b\n"+c.want {
			t.Errorf("args = %q", got)
		}
	}
}

func TestFailureReturnsOutput(t *testing.T) {
	bin, _ := fakeCompose(t, "echo 'pull access denied' >&2; exit 1")
	r := New(Options{Bin: bin, Timeout: time.Minute, Concurrency: 1})
	out, err := r.Up(context.Background(), "/f.yml", "dg-a-b")
	if err == nil || !strings.Contains(out, "pull access denied") {
		t.Fatalf("err=%v out=%q", err, out)
	}
}

func TestTimeoutKillsProcess(t *testing.T) {
	bin, _ := fakeCompose(t, "sleep 30")
	r := New(Options{Bin: bin, Timeout: time.Minute, Concurrency: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := r.Up(ctx, "/f.yml", "dg-a-b")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("process was not killed promptly")
	}
}

func TestTryLock(t *testing.T) {
	r := New(Options{Concurrency: 1})
	unlock, ok := r.TryLock("alice/shop")
	if !ok {
		t.Fatal("first lock failed")
	}
	if _, ok := r.TryLock("alice/shop"); ok {
		t.Fatal("second lock must fail")
	}
	if u2, ok := r.TryLock("alice/blog"); !ok {
		t.Fatal("other key must lock")
	} else {
		u2()
	}
	unlock()
	unlock() // idempotent
	if u3, ok := r.TryLock("alice/shop"); !ok {
		t.Fatal("lock not released")
	} else {
		u3()
	}
}
