package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/sebbi08/deployment-gateway/internal/names"
	"github.com/sebbi08/deployment-gateway/internal/render"
	"github.com/sebbi08/deployment-gateway/internal/store"
	"github.com/sebbi08/deployment-gateway/internal/verify"
)

// Applying a changed secret value (spec §7): after the operator replaces a
// value, a background job recreates the running services that use it. Jobs
// are kept in memory only; refs stay stale until a pass succeeds.

const (
	skipFailedDeploy = "applied on the next successful deploy"
	skipPolicy       = "configuration no longer allowed by current policy; the agent must redeploy"
	skipBusy         = "app busy"
	skipNotRunning   = "no running service uses the changed secret; applied on the next deploy"
)

// applyQueue holds the apps waiting for an apply pass, per workspace, and
// which workspaces have a worker. One worker per workspace processes its
// apps one at a time, so passes of one workspace never run concurrently.
type applyQueue struct {
	mu      sync.Mutex
	ctx     context.Context // nil until StartSecretApplier
	pending map[string][]int64
	running map[string]bool
}

// StartSecretApplier lets queued and future apply jobs run. Workers start on
// demand, one per workspace, and stop with ctx (queued jobs are dropped).
func (s *Service) StartSecretApplier(ctx context.Context) {
	q := &s.applies
	q.mu.Lock()
	defer q.mu.Unlock()
	q.ctx = ctx
	for ws := range q.pending {
		s.startApplyWorkerLocked(ws)
	}
}

// enqueueApply queues an apply pass for the apps. An app already waiting is
// not queued twice; an app whose pass is running gets one more pass
// afterwards, which picks up any value saved in the meantime.
func (s *Service) enqueueApply(ws string, appIDs []int64) {
	q := &s.applies
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending == nil {
		q.pending = map[string][]int64{}
	}
	for _, id := range appIDs {
		if !slices.Contains(q.pending[ws], id) {
			q.pending[ws] = append(q.pending[ws], id)
		}
	}
	if len(q.pending[ws]) == 0 {
		delete(q.pending, ws)
		return
	}
	if q.ctx != nil {
		s.startApplyWorkerLocked(ws)
	}
}

// startApplyWorkerLocked starts the workspace's worker unless it runs; the
// caller holds the queue mutex.
func (s *Service) startApplyWorkerLocked(ws string) {
	q := &s.applies
	if q.running[ws] {
		return
	}
	if q.running == nil {
		q.running = map[string]bool{}
	}
	q.running[ws] = true
	go s.applyWorker(q.ctx, ws)
}

func (s *Service) applyWorker(ctx context.Context, ws string) {
	q := &s.applies
	for {
		q.mu.Lock()
		ids := q.pending[ws]
		if len(ids) == 0 || ctx.Err() != nil {
			delete(q.running, ws)
			if len(ids) == 0 {
				delete(q.pending, ws)
			}
			q.mu.Unlock()
			return
		}
		q.pending[ws] = ids[1:]
		q.mu.Unlock()
		s.applyApp(ctx, ws, ids[0])
	}
}

// applyApp resolves a queued job. A workspace or app deleted since it was
// queued is not an error.
func (s *Service) applyApp(ctx context.Context, wsName string, appID int64) {
	ws, err := s.Store.GetWorkspaceByName(ctx, wsName)
	if errors.Is(err, store.ErrNotFound) {
		s.Logger.Info("secret apply dropped: the workspace no longer exists", "workspace", wsName)
		return
	}
	if err != nil {
		s.Logger.Warn("secret apply: loading the workspace failed", "workspace", wsName, "err", err)
		return
	}
	apps, err := s.Store.ListApps(ctx, ws.ID)
	if err != nil {
		s.Logger.Warn("secret apply: listing apps failed", "workspace", wsName, "err", err)
		return
	}
	i := slices.IndexFunc(apps, func(a store.App) bool { return a.ID == appID })
	if i < 0 {
		s.Logger.Info("secret apply dropped: the app no longer exists", "workspace", wsName, "app_id", appID)
		return
	}
	if err := s.applySecrets(ctx, ws, apps[i]); err != nil {
		s.Logger.Warn("applying a changed secret value failed", "workspace", wsName, "app", apps[i].Name, "err", err)
	}
}

// applyOutcome describes one apply pass for the audit log.
type applyOutcome struct {
	secrets  []string // names of the changed secrets
	services []string // short names of the recreated services
	skipped  string
	detail   string // why it was skipped (verifier output)
	noop     bool   // app gone or nothing stale: nothing to record
}

func (o applyOutcome) params(app string) map[string]any {
	p := map[string]any{"app": app, "services": o.services}
	if o.services == nil {
		p["services"] = []string{}
	}
	if len(o.secrets) > 0 {
		p["secret"] = strings.Join(o.secrets, ",")
	}
	if o.skipped != "" {
		p["skipped"] = o.skipped
	}
	if o.detail != "" {
		p["detail"] = o.detail
	}
	return p
}

// applySecrets recreates the app's running services that use a secret with
// a newer value (spec §7). The outcome goes to the app's apply status, never
// to last_deploy_*.
func (s *Service) applySecrets(ctx context.Context, ws store.Workspace, app store.App) error {
	ws, ok, err := s.reloadWorkspace(ctx, ws)
	if err != nil || !ok {
		return err
	}
	started := time.Now()
	unlock, err := s.waitAppLock(ctx, lockKey(ws.Name, app.Name))
	if err != nil {
		return err
	}
	// Recreate, checks and bookkeeping must not be cut off by a shutdown;
	// the runner enforces its own timeout.
	bg := context.WithoutCancel(ctx)
	var out applyOutcome
	if unlock == nil {
		out, err = s.applyBusy(bg, app)
	} else {
		defer unlock()
		out, err = s.applyLocked(bg, ws, app)
	}
	if out.noop {
		return err
	}
	if out.detail != "" {
		out.detail = s.masker(bg, ws.ID)(out.detail)
	}
	e := store.AuditEntry{Time: started, ActorType: "system", Actor: "secret-applier", Workspace: ws.Name,
		Action: "secret_apply", Params: redact(out.params(app.Name))}
	s.systemFinish(bg, e, &err)
	return err
}

// reloadWorkspace re-reads the workspace row. ok is false when it was
// deleted (IDs are reused, so name and creation time must match too).
func (s *Service) reloadWorkspace(ctx context.Context, ws store.Workspace) (store.Workspace, bool, error) {
	cur, err := s.Store.GetWorkspace(ctx, ws.ID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (cur.Name != ws.Name || !cur.CreatedAt.Equal(ws.CreatedAt))) {
		return store.Workspace{}, false, nil
	}
	if err != nil {
		return store.Workspace{}, false, err
	}
	return cur, true, nil
}

// systemFinish records a finished background job (actor_type system) as one
// audit row, with the same result and severity rules as agent calls.
func (s *Service) systemFinish(ctx context.Context, e store.AuditEntry, errp *error) {
	s.finish(ctx, 0, &e, errp)
}

// waitAppLock polls the app lock every applyPoll until ApplyLockWait has
// passed. It returns a nil unlock on timeout and an error only when ctx ends.
func (s *Service) waitAppLock(ctx context.Context, key string) (func(), error) {
	poll := s.applyPoll
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	deadline := time.Now().Add(s.ApplyLockWait)
	for {
		if unlock, ok := s.Compose.TryLock(key); ok {
			return unlock, nil
		}
		wait := min(poll, time.Until(deadline))
		if wait <= 0 {
			return nil, nil
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// applyBusy records that the app lock could not be taken in time.
func (s *Service) applyBusy(ctx context.Context, app store.App) (applyOutcome, error) {
	refs, err := s.Store.AppSecretRefs(ctx, app.ID)
	if err != nil {
		return applyOutcome{}, err
	}
	out := applyOutcome{secrets: staleSecrets(refs), skipped: skipBusy}
	return out, s.Store.SetSecretApply(ctx, app.ID, "skipped", skipBusy)
}

// applyLocked runs one pass under the app lock. The workspace row is read
// again here: quota, port range and bind IP may have changed while the pass
// waited for the lock, and policy and runtime checks must use current ones.
func (s *Service) applyLocked(ctx context.Context, ws store.Workspace, app store.App) (out applyOutcome, err error) {
	ws, ok, err := s.reloadWorkspace(ctx, ws)
	if err != nil {
		return applyOutcome{}, err
	}
	if !ok {
		return applyOutcome{noop: true}, nil
	}
	a, err := s.Store.GetApp(ctx, ws.ID, app.Name)
	if errors.Is(err, store.ErrNotFound) || (err == nil && a.ID != app.ID) {
		return applyOutcome{noop: true}, nil
	}
	if err != nil {
		return applyOutcome{}, err
	}
	refs, err := s.Store.AppSecretRefs(ctx, a.ID)
	if err != nil {
		return applyOutcome{}, err
	}
	affected := staleServices(refs)
	if len(affected) == 0 {
		// A deploy since the value change already applied it.
		return applyOutcome{noop: true}, nil
	}
	out.secrets = staleSecrets(refs)
	skip := func(reason string) (applyOutcome, error) {
		out.skipped = reason
		return out, s.Store.SetSecretApply(ctx, a.ID, "skipped", reason)
	}
	// Every later failure is recorded in the apply status.
	defer func() {
		if err == nil {
			return
		}
		msg := err.Error()
		var se *Error
		if errors.As(err, &se) {
			msg = se.Message
		}
		if serr := s.Store.SetSecretApply(ctx, a.ID, "failed", s.masker(ctx, ws.ID)(msg)); serr != nil {
			err = errors.Join(err, serr)
		}
	}()

	// After a failed, interrupted or rejected deploy the compose file is not
	// what runs; an apply must never finish such a deploy.
	if a.LastDeployResult != "ok" {
		return skip(skipFailedDeploy)
	}
	pl, err := s.currentPlanFacts(ctx, ws, a.Name)
	if err != nil {
		return out, err
	}
	if verr := verify.Verify(pl.out.YAML, pl.policy); verr != nil {
		out.detail = verr.Error()
		s.Logger.Info("secret apply skipped: the compose file no longer passes current policy", "workspace", ws.Name, "app", a.Name)
		return skip(skipPolicy)
	}
	if err := s.rewriteEnvFiles(ctx, ws, a, affected); err != nil {
		return out, err
	}
	containers, err := s.Docker.ProjectContainers(ctx, ws.Name, a.Name)
	if err != nil {
		return out, err
	}
	state := map[string]string{}
	for _, c := range containers {
		state[c.Labels[names.LabelService]] = c.State
	}
	var keys []string
	for _, svc := range affected {
		// A crash loop ("restarting") is often caused by the very credential
		// being fixed. Stopped or missing services are not started.
		if st := state[svc]; st == "running" || st == "restarting" {
			out.services = append(out.services, svc)
			keys = append(keys, names.Container(ws.Name, a.Name, svc))
		}
	}
	if len(keys) == 0 {
		out.skipped = skipNotRunning
		return out, nil
	}
	if err := s.Store.SetSecretApply(ctx, a.ID, "running", ""); err != nil {
		return out, err
	}
	file, project := s.composeFile(ws.Name, a.Name), names.Project(ws.Name, a.Name)
	volsBefore := s.appVolumes(ctx, ws.Name, a.Name)
	upStarted := time.Now()
	composeOut, upErr := s.Compose.Recreate(ctx, file, project, keys)
	// Layer 3 runs even when recreate failed, as for a deploy.
	if violations := s.runtimeCheck(ctx, ws.Name, a.Name, pl, upStarted); len(violations) > 0 {
		return out, s.rejectRuntime(ctx, ws, a.Name, violations, volsBefore, "secret update")
	}
	if upErr == nil {
		if problems := s.unhealthyServices(ctx, ws.Name, a.Name, out.services, pl.healthchecks); len(problems) > 0 {
			upErr = errors.New("services not running and healthy after recreate: " + strings.Join(problems, "; "))
		}
	}
	if upErr != nil {
		mask := s.masker(ctx, ws.ID)
		msg := "applying the changed secret value failed: " + mask(upErr.Error()) + "\n" + s.sanitize(ws.Name, mask(composeOut)) +
			s.failureLogs(ctx, ws.Name, a.Name, mask)
		return out, &Error{Code: CodeDeployFailed, Message: msg, detail: mask(composeOut)}
	}
	if err := s.clearApplied(ctx, ws, a, out.services); err != nil {
		return out, err
	}
	if err := s.Store.SetSecretApply(ctx, a.ID, "ok", ""); err != nil {
		return out, err
	}
	s.removeVolumes(ctx, volsBefore, s.appVolumes(ctx, ws.Name, a.Name))
	return out, nil
}

// envFiles returns the env file content of each given service from the
// app's refs and the workspace's current values.
func envFiles(refs []store.SecretRef, values map[string]string, services []string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for _, svc := range services {
		out[svc] = map[string]string{}
	}
	for _, r := range refs {
		m, ok := out[r.Service]
		if !ok {
			continue
		}
		v, ok := values[r.Secret]
		if !ok {
			return nil, fmt.Errorf("secret %q of services.%s.secrets.%s has no value", r.Secret, r.Service, r.EnvKey)
		}
		m[r.EnvKey] = v
	}
	return out, nil
}

// rewriteEnvFiles rewrites the env files of the given services only; the
// other services' files are left alone. It runs under the workspace mutex,
// which admin secret changes take too.
func (s *Service) rewriteEnvFiles(ctx context.Context, ws store.Workspace, app store.App, services []string) error {
	mu := s.wsMutex(ws.Name)
	mu.Lock()
	defer mu.Unlock()
	refs, err := s.Store.AppSecretRefs(ctx, app.ID)
	if err != nil {
		return err
	}
	values, err := s.Store.SecretValues(ctx, ws.ID)
	if err != nil {
		return err
	}
	files, err := envFiles(refs, values, services)
	if err != nil {
		return err
	}
	// Refuse a bad value before any file changes.
	for _, svc := range services {
		if err := checkEnvValues(svc, files[svc]); err != nil {
			return err
		}
	}
	for _, svc := range services {
		if err := s.writeEnvFile(ws.Name, app.Name, svc, files[svc]); err != nil {
			return err
		}
	}
	return syncDir(s.secretsDir(ws.Name, app.Name))
}

// clearApplied clears the stale flag of the recreated services whose env
// file still matches the current values. A value saved after the files were
// written keeps its refs stale for the pass it queued.
func (s *Service) clearApplied(ctx context.Context, ws store.Workspace, app store.App, services []string) error {
	mu := s.wsMutex(ws.Name)
	mu.Lock()
	defer mu.Unlock()
	refs, err := s.Store.AppSecretRefs(ctx, app.ID)
	if err != nil {
		return err
	}
	values, err := s.Store.SecretValues(ctx, ws.ID)
	if err != nil {
		return err
	}
	files, err := envFiles(refs, values, services)
	if err != nil {
		return err
	}
	dir := s.secretsDir(ws.Name, app.Name)
	var current []string
	for _, svc := range services {
		data, err := os.ReadFile(render.EnvFilePath(dir, svc))
		if err == nil && bytes.Equal(data, render.EnvFile(files[svc])) {
			current = append(current, svc)
		}
	}
	return s.Store.ClearStale(ctx, app.ID, current)
}

// staleServices returns the sorted short names of services with stale refs.
func staleServices(refs []store.SecretRef) []string {
	set := map[string]bool{}
	for _, r := range refs {
		if r.Stale {
			set[r.Service] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// staleSecrets returns the sorted names of secrets with stale refs.
func staleSecrets(refs []store.SecretRef) []string {
	set := map[string]bool{}
	for _, r := range refs {
		if r.Stale {
			set[r.Secret] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// currentPlanFacts derives the facts the runtime and health checks need from
// the app's current docker-compose.yml (the stack is not stored): labels and
// healthcheck presence per service, port claims from the store, and the
// verifier policy from the current workspace and grants. pl.stack is nil.
func (s *Service) currentPlanFacts(ctx context.Context, ws store.Workspace, app string) (*planned, error) {
	_, pol, err := s.currentPolicy(ctx, ws, app)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.composeFile(ws.Name, app))
	if err != nil {
		return nil, err
	}
	var doc struct {
		Services map[string]struct {
			Labels      map[string]string `yaml:"labels"`
			Healthcheck any               `yaml:"healthcheck"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parsing the compose file: %w", err)
	}
	labels := map[string]map[string]string{}
	health := map[string]bool{}
	for key, svc := range doc.Services {
		short := svc.Labels[names.LabelService]
		if !names.ValidService(short) || key != names.Container(ws.Name, app, short) {
			return nil, fmt.Errorf("compose service %q does not match its service label", key)
		}
		l := make(map[string]string, len(svc.Labels))
		for k, v := range svc.Labels { // compose removes $$ escaping
			l[k] = strings.ReplaceAll(v, "$$", "$")
		}
		labels[short] = l
		health[short] = svc.Healthcheck != nil
	}
	all, err := s.Store.ListPortClaims(ctx, ws.ID)
	if err != nil {
		return nil, err
	}
	claims := []store.PortClaim{}
	for _, c := range all {
		if c.App == app {
			claims = append(claims, c)
		}
	}
	templates, err := s.Store.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	return &planned{app: app, out: render.Output{YAML: raw, Labels: labels}, policy: pol, claims: claims, healthchecks: health,
		runtimePrefixes: reservedPrefixes(slices.Concat(fixedRuntimePrefixes, s.ExtraReservedPrefixes), templates)}, nil
}
