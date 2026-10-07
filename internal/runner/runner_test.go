package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	wantArgs := "-f\n/ws/alice/shop/docker-compose.yml\n-p\ndg-alice-shop\nup\n-d\n--pull\nnever\n--remove-orphans\n--wait\n--wait-timeout\n120\n"
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
		{func() (string, error) { return r.Pull(ctx, "/f.yml", "dg-a-b") }, "pull\n"},
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

// One workspace queueing many operations must not push another workspace's
// operation behind all of them: each workspace has at most one operation
// waiting for the global slot.
func TestRunnerIsFairAcrossWorkspaces(t *testing.T) {
	order := filepath.Join(t.TempDir(), "order")
	bin, _ := fakeCompose(t, `echo "$4" >> `+order+`; sleep 0.3`)
	r := New(Options{Bin: bin, Timeout: time.Minute, Concurrency: 1})
	var wg sync.WaitGroup
	start := func(project string, delay time.Duration) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(delay)
			if _, err := r.Stop(context.Background(), "/nonexistent/x.yml", project, ""); err != nil {
				t.Error(err)
			}
		}()
	}
	start("dg-alice-a1", 0)
	start("dg-alice-a2", 20*time.Millisecond)
	start("dg-alice-a3", 40*time.Millisecond)
	start("dg-bob-b1", 80*time.Millisecond)
	wg.Wait()
	got := strings.Fields(read(t, order))
	if slices.Index(got, "dg-bob-b1") > slices.Index(got, "dg-alice-a3") {
		t.Fatalf("bob waited behind all of alice's operations: %v", got)
	}
}

func TestRunnerQueueWaitIsBounded(t *testing.T) {
	bin, _ := fakeCompose(t, "sleep 2")
	r := New(Options{Bin: bin, Timeout: time.Minute, Concurrency: 1, QueueTimeout: 200 * time.Millisecond})
	go r.Stop(context.Background(), "/nonexistent/x.yml", "dg-alice-a1", "")
	time.Sleep(50 * time.Millisecond)
	begin := time.Now()
	_, err := r.Stop(context.Background(), "/nonexistent/x.yml", "dg-bob-b1", "")
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
	if d := time.Since(begin); d > time.Second {
		t.Fatalf("waited %v, want about 200ms", d)
	}
}

// Shutdown must not kill a compose operation half-way: it refuses new
// operations and waits for running ones.
func TestShutdownWaitsForRunningOperations(t *testing.T) {
	bin, _ := fakeCompose(t, "sleep 0.5")
	r := New(Options{Bin: bin, Timeout: time.Minute, Concurrency: 1})
	done := make(chan error, 1)
	go func() { _, err := r.Stop(context.Background(), "/nonexistent/x.yml", "dg-alice-a1", ""); done <- err }()
	time.Sleep(100 * time.Millisecond)
	begin := time.Now()
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(begin) < 300*time.Millisecond {
		t.Fatal("Shutdown returned while an operation was still running")
	}
	if err := <-done; err != nil {
		t.Fatalf("running operation failed: %v", err)
	}
	if _, err := r.Stop(context.Background(), "/nonexistent/x.yml", "dg-alice-a1", ""); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("new operation after Shutdown: %v", err)
	}
}

// Callers detach compose from the request context, so the runner's own
// timeout is the only thing that stops a hanging compose.
func TestRunnerTimeoutStopsHangingCompose(t *testing.T) {
	bin, _ := fakeCompose(t, "sleep 30")
	r := New(Options{Bin: bin, Timeout: 200 * time.Millisecond, Grace: time.Millisecond, Concurrency: 1})
	begin := time.Now()
	_, err := r.Stop(context.WithoutCancel(context.Background()), "/nonexistent/x.yml", "dg-alice-a1", "")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want a timeout error, got %v", err)
	}
	if d := time.Since(begin); d > 10*time.Second {
		t.Fatalf("compose ran %v despite the timeout", d)
	}
}
