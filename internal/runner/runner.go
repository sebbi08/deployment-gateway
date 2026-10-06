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
	"strings"
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
	// QueueTimeout bounds the wait for a compose slot (default: Timeout).
	QueueTimeout time.Duration
}

// ErrBusy is returned when no compose slot became free within QueueTimeout.
var ErrBusy = errors.New("the gateway is busy with other operations; retry later")

type Runner struct {
	opts  Options
	sem   chan struct{}
	mu    sync.Mutex
	locks map[string]bool
	// wsQueues lets each workspace have at most one operation waiting for
	// the global slot, so one tenant cannot queue all others behind it.
	wsQueues map[string]chan struct{}
}

func New(o Options) *Runner {
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	if o.DockerHost == "" {
		o.DockerHost = "unix:///var/run/docker.sock"
	}
	if o.QueueTimeout <= 0 {
		o.QueueTimeout = o.Timeout
	}
	return &Runner{opts: o, sem: make(chan struct{}, o.Concurrency), locks: map[string]bool{}, wsQueues: map[string]chan struct{}{}}
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

// Pull fetches every service image. It runs before Up so the gateway can
// inspect image labels before any container exists.
func (r *Runner) Pull(ctx context.Context, file, project string) (string, error) {
	return r.run(ctx, file, project, "pull")
}

// Up never pulls: the images were pulled and inspected by the caller.
func (r *Runner) Up(ctx context.Context, file, project string) (string, error) {
	return r.run(ctx, file, project, "up", "-d", "--pull", "never", "--remove-orphans",
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

// workspaceQueue returns the one-slot queue of the project's workspace
// (project names are dg-<ws>-<app>; workspace names contain no hyphen).
func (r *Runner) workspaceQueue(project string) chan struct{} {
	ws, _, _ := strings.Cut(strings.TrimPrefix(project, "dg-"), "-")
	r.mu.Lock()
	defer r.mu.Unlock()
	q, ok := r.wsQueues[ws]
	if !ok {
		q = make(chan struct{}, 1)
		r.wsQueues[ws] = q
	}
	return q
}

// acquire takes the workspace queue and then a global slot, waiting at most
// QueueTimeout in total.
func (r *Runner) acquire(ctx context.Context, project, op string) (func(), error) {
	wait := time.NewTimer(r.opts.QueueTimeout)
	defer wait.Stop()
	q := r.workspaceQueue(project)
	select {
	case q <- struct{}{}:
	case <-wait.C:
		return nil, fmt.Errorf("compose %s: %w", op, ErrBusy)
	case <-ctx.Done():
		return nil, fmt.Errorf("compose %s: %w", op, ctx.Err())
	}
	select {
	case r.sem <- struct{}{}:
		return func() { <-r.sem; <-q }, nil
	case <-wait.C:
		<-q
		return nil, fmt.Errorf("compose %s: %w", op, ErrBusy)
	case <-ctx.Done():
		<-q
		return nil, fmt.Errorf("compose %s: %w", op, ctx.Err())
	}
}

func (r *Runner) run(ctx context.Context, file, project string, args ...string) (string, error) {
	release, err := r.acquire(ctx, project, args[0])
	if err != nil {
		return "", err
	}
	defer release()

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
	err = cmd.Run()
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
