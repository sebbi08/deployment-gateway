// Package runner executes the standalone docker-compose binary with a fixed
// argv and a scrubbed environment (spec §11). No shell is ever involved.
package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type Options struct {
	Bin          string
	DockerConfig string
	DockerHost   string
	Timeout      time.Duration
	Concurrency  int
}

type Runner struct {
	opts  Options
	sem   chan struct{}
	mu    sync.Mutex
	locks map[string]bool
}

func New(o Options) *Runner {
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	if o.DockerHost == "" {
		o.DockerHost = "unix:///var/run/docker.sock"
	}
	return &Runner{opts: o, sem: make(chan struct{}, o.Concurrency), locks: map[string]bool{}}
}

// TryLock takes a non-blocking per-app lock. The returned unlock is idempotent.
func (r *Runner) TryLock(key string) (func(), bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locks[key] {
		return nil, false
	}
	r.locks[key] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.locks, key)
			r.mu.Unlock()
		})
	}, true
}

func (r *Runner) Up(ctx context.Context, file, project string) (string, error) {
	return r.run(ctx, file, project, "up", "-d", "--pull", "always", "--remove-orphans",
		"--wait", "--wait-timeout", strconv.Itoa(int(r.opts.Timeout.Seconds())))
}

func (r *Runner) Down(ctx context.Context, file, project string) (string, error) {
	return r.run(ctx, file, project, "down", "-v", "--remove-orphans")
}

func (r *Runner) Restart(ctx context.Context, file, project, service string) (string, error) {
	return r.run(ctx, file, project, withService("restart", service)...)
}

func (r *Runner) Stop(ctx context.Context, file, project, service string) (string, error) {
	return r.run(ctx, file, project, withService("stop", service)...)
}

func withService(cmd, service string) []string {
	if service == "" {
		return []string{cmd}
	}
	return []string{cmd, service}
}

func (r *Runner) run(ctx context.Context, file, project string, args ...string) (string, error) {
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return "", fmt.Errorf("compose %s: %w", args[0], ctx.Err())
	}
	defer func() { <-r.sem }()

	ctx, cancel := context.WithTimeout(ctx, r.opts.Timeout+30*time.Second)
	defer cancel()
	argv := append([]string{"-f", file, "-p", project}, args...)
	cmd := exec.CommandContext(ctx, r.opts.Bin, argv...)
	if dir := filepath.Dir(file); isDir(dir) {
		cmd.Dir = dir
	}
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=/tmp",
		"DOCKER_CONFIG=" + r.opts.DockerConfig,
		"DOCKER_HOST=" + r.opts.DockerHost,
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	buf := &tailBuffer{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = buf, buf
	err := cmd.Run()
	if ctx.Err() != nil {
		return buf.String(), fmt.Errorf("compose %s timed out", args[0])
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return buf.String(), fmt.Errorf("compose %s failed with exit code %d", args[0], exitErr.ExitCode())
		}
		return buf.String(), fmt.Errorf("compose %s: %w", args[0], err)
	}
	return buf.String(), nil
}

// tailBuffer keeps the last max bytes of output. exec guarantees a single
// writer when Stdout and Stderr are the same pointer.
type tailBuffer struct {
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = t.b[over:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.b) }

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
