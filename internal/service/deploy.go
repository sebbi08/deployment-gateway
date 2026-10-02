package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/names"
	"github.com/sebbi08/deployment-gateway/internal/stack"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

func (s *Service) Validate(ctx context.Context, p Principal, in DeployInput) (res ValidateResult, err error) {
	defer s.track(ctx, p, "validate", in, &err, time.Now())
	pl, err := s.plan(ctx, p.Workspace, in.App, &in.Stack)
	if err != nil {
		return ValidateResult{}, err
	}
	if err := s.checkPortConflicts(ctx, p.Workspace, in.App, pl.claims); err != nil {
		return ValidateResult{}, err
	}
	return ValidateResult{App: in.App, Services: pl.servicePlans(p.Workspace.Name), TotalMemoryMB: pl.memBytes >> 20, TotalCPUs: pl.cpus}, nil
}

func (s *Service) Deploy(ctx context.Context, p Principal, in DeployInput) (res AppStatus, err error) {
	defer s.track(ctx, p, "deploy", in, &err, time.Now())
	ws, app := p.Workspace, in.App
	if !names.ValidApp(app) {
		return AppStatus{}, validationError("app", appRule)
	}
	unlock, ok := s.Compose.TryLock(lockKey(ws.Name, app))
	if !ok {
		return AppStatus{}, &Error{Code: CodeConflict, Message: "another operation on this app is in progress"}
	}
	defer unlock()

	appRec, pl, err := s.prepare(ctx, ws, app, &in.Stack)
	if err != nil {
		return AppStatus{}, err
	}
	file, project := s.composeFile(ws.Name, app), names.Project(ws.Name, app)
	// Pull, up, teardown and status bookkeeping must survive client
	// disconnects; the runner still enforces its own timeout.
	bg := context.WithoutCancel(ctx)
	if out, err := s.Compose.Pull(bg, file, project); err != nil {
		_ = s.Store.RecordDeploy(bg, appRec.ID, "failed")
		msg := "pulling images failed: " + s.sanitize(ws.Name, err.Error()) + "\n" + s.sanitize(ws.Name, out) + s.prevHint(ws.Name, app)
		return AppStatus{}, &Error{Code: CodeDeployFailed, Message: msg, detail: out}
	}
	if err := s.checkImageLabels(bg, pl); err != nil {
		_ = s.Store.RecordDeploy(bg, appRec.ID, "failed")
		return AppStatus{}, err
	}
	out, upErr := s.Compose.Up(bg, file, project)
	// Layer 3 runs even when up failed: --wait can fail while containers exist.
	if violations := s.runtimeCheck(bg, ws.Name, app, pl); len(violations) > 0 {
		detail := strings.Join(violations, "; ")
		msg := "deployment rejected by runtime security check; containers were removed"
		downOut, downErr := s.Compose.Down(bg, file, project)
		if downErr != nil {
			detail += " | compose down failed: " + downErr.Error()
			msg = "deployment rejected by runtime security check; removing containers failed - an administrator must intervene"
		}
		_ = s.Store.RecordDeploy(bg, appRec.ID, CodeSecurity)
		return AppStatus{}, &Error{Code: CodeSecurity, Message: msg, detail: detail + " | " + downOut, critical: true}
	}
	if upErr != nil {
		_ = s.Store.RecordDeploy(bg, appRec.ID, "failed")
		msg := "deployment failed: " + upErr.Error() + "\n" + s.sanitize(ws.Name, out) + s.failureLogs(bg, ws.Name, app) + s.prevHint(ws.Name, app)
		return AppStatus{}, &Error{Code: CodeDeployFailed, Message: msg, detail: out}
	}
	if err := s.Store.RecordDeploy(bg, appRec.ID, "ok"); err != nil {
		return AppStatus{}, err
	}
	return s.status(ctx, ws, app, appRec.Hosts, pl.claims, stack.ServiceNames(pl.stack))
}

func (s *Service) prevHint(ws, app string) string {
	if _, err := os.Stat(s.prevFile(ws, app)); err == nil {
		return "\nThe previous successful configuration is kept as docker-compose.prev.yml; redeploy the previous stack to roll back."
	}
	return ""
}

// checkImageLabels rejects images whose LABELs fall into a reserved
// namespace. Docker merges image labels into the container's labels, and
// Traefik (or another label consumer) would act on them, so an image could
// otherwise smuggle in routers for foreign hosts. Runs after pull, before up.
func (s *Service) checkImageLabels(ctx context.Context, pl *planned) error {
	var fields stack.Errors
	for _, svc := range stack.ServiceNames(pl.stack) {
		ref := pl.stack.Services[svc].Image
		labels, err := s.Docker.ImageLabels(ctx, ref)
		if err != nil {
			return &Error{Code: CodeDeployFailed, Message: fmt.Sprintf("cannot inspect the image of service %q", svc), detail: err.Error()}
		}
		var bad []string
		for _, k := range slices.Sorted(maps.Keys(labels)) {
			if dockerx.HasReservedPrefix(k, pl.imagePrefixes) {
				bad = append(bad, strconv.Quote(k))
			}
		}
		if len(bad) > 0 {
			fields = append(fields, stack.FieldError{Field: "services." + svc + ".image", Message: fmt.Sprintf(
				"image carries label(s) %s in a reserved namespace (%s); rebuild the image without them",
				strings.Join(bad, ", "), strings.Join(pl.imagePrefixes, ", "))})
		}
	}
	if len(fields) > 0 {
		return &Error{Code: CodeValidation, Message: "image labels rejected", Fields: fields}
	}
	return nil
}

// prepare runs under the workspace mutex so concurrent deploys of different
// apps cannot both pass the quota check. It writes files and DB state.
func (s *Service) prepare(ctx context.Context, ws store.Workspace, app string, st *stack.Stack) (store.App, *planned, error) {
	mu := s.wsMutex(ws.Name)
	mu.Lock()
	defer mu.Unlock()
	// DeleteWorkspace holds the same mutex; the principal may have been
	// authenticated before the workspace was deleted.
	if _, err := s.Store.GetWorkspace(ctx, ws.ID); errors.Is(err, store.ErrNotFound) {
		return store.App{}, nil, notFoundError("workspace")
	} else if err != nil {
		return store.App{}, nil, err
	}
	pl, err := s.plan(ctx, ws, app, st)
	if err != nil {
		return store.App{}, nil, err
	}
	prev, err := s.Store.GetApp(ctx, ws.ID, app)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.App{}, nil, err
	}
	lastOK := err == nil && prev.LastDeployResult == "ok"
	if err := s.prepareDirs(ws.Name, app, st); err != nil {
		return store.App{}, nil, err
	}
	next := filepath.Join(s.appDir(ws.Name, app), "docker-compose.next.yml")
	if err := os.WriteFile(next, pl.out.YAML, 0o640); err != nil {
		return store.App{}, nil, err
	}
	appRec, err := s.Store.SaveDeployment(ctx, ws.ID, app, pl.memBytes, pl.cpus, pl.out.Hosts, pl.claims)
	if err != nil {
		os.Remove(next)
		var taken *store.PortTakenError
		if errors.As(err, &taken) {
			return store.App{}, nil, &Error{Code: CodePortTaken, Message: taken.Error()}
		}
		return store.App{}, nil, err
	}
	// Persist "pending" so a lost "failed" update can never make the next
	// deploy rotate a never-successful render into prev.
	if err := s.Store.RecordDeploy(context.WithoutCancel(ctx), appRec.ID, "pending"); err != nil {
		return store.App{}, nil, err
	}
	cur := s.composeFile(ws.Name, app)
	if lastOK {
		if _, err := os.Stat(cur); err == nil {
			if err := os.Rename(cur, s.prevFile(ws.Name, app)); err != nil {
				return store.App{}, nil, err
			}
		}
	}
	if err := os.Rename(next, cur); err != nil {
		return store.App{}, nil, err
	}
	return appRec, pl, nil
}

func (s *Service) prepareDirs(ws, app string, st *stack.Stack) error {
	data := s.dataDir(ws, app)
	if err := os.MkdirAll(data, 0o750); err != nil {
		return err
	}
	for _, name := range stack.ServiceNames(st) {
		svc := st.Services[name]
		if svc.Volume == nil {
			continue
		}
		dir := filepath.Join(data, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			return err
		}
		if svc.User != "" {
			uidStr, gidStr, _ := strings.Cut(svc.User, ":")
			uid, _ := strconv.Atoi(uidStr)
			gid, _ := strconv.Atoi(gidStr)
			if err := s.Chown(dir, uid, gid); err != nil {
				return fmt.Errorf("chown volume of %s: %w", name, err)
			}
		}
	}
	return nil
}

// runtimeCheck inspects every container of the app (spec §10 layer 3).
func (s *Service) runtimeCheck(ctx context.Context, ws, app string, pl *planned) []string {
	containers, err := s.Docker.ProjectContainers(ctx, ws, app)
	if err != nil {
		return []string{"cannot list containers: " + err.Error()}
	}
	var v []string
	for _, c := range containers {
		svc := c.Labels[names.LabelService]
		facts, err := s.Docker.Inspect(ctx, c.ID)
		if err != nil {
			v = append(v, c.Name+": inspect failed: "+err.Error())
			continue
		}
		var ports []dockerx.PortFact
		for _, cl := range portsFor(pl.claims, svc) {
			ports = append(ports, dockerx.PortFact{ContainerPort: cl.ContainerPort, Protocol: cl.Protocol, HostIP: pl.policy.BindIP, HostPort: cl.HostPort})
		}
		v = append(v, dockerx.CheckRuntime(facts, dockerx.RuntimePolicy{Workspace: ws, App: app, DataDir: pl.policy.DataDir, Ports: ports,
			Labels: pl.out.Labels[svc], ReservedPrefixes: pl.runtimePrefixes})...)
	}
	return v
}

// status merges container state with recorded hosts and port claims.
func (s *Service) status(ctx context.Context, ws store.Workspace, app string, hosts map[string]string, claims []store.PortClaim, services []string) (AppStatus, error) {
	containers, err := s.Docker.ProjectContainers(ctx, ws.Name, app)
	if err != nil {
		return AppStatus{}, err
	}
	byService := map[string]dockerx.ContainerInfo{}
	set := map[string]bool{}
	for _, c := range containers {
		svc := c.Labels[names.LabelService]
		byService[svc] = c
		set[svc] = true
	}
	for _, svc := range services {
		set[svc] = true
	}
	for svc := range hosts {
		set[svc] = true
	}
	for _, c := range claims {
		set[c.Service] = true
	}
	res := AppStatus{App: app, Services: []ServiceStatus{}}
	for _, svc := range slices.Sorted(maps.Keys(set)) {
		st := ServiceStatus{Service: svc, InternalHost: names.Container(ws.Name, app, svc), URL: hostURL(hosts[svc]),
			State: "missing", Ports: portsFor(claims, svc)}
		if c, ok := byService[svc]; ok {
			st.State, st.Health = c.State, c.Health
		}
		res.Services = append(res.Services, st)
	}
	return res, nil
}

// failureLogs returns the last log lines of containers that are not healthy.
func (s *Service) failureLogs(ctx context.Context, ws, app string) string {
	containers, err := s.Docker.ProjectContainers(ctx, ws, app)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, c := range containers {
		if c.State == "running" && c.Health != "unhealthy" {
			continue
		}
		logs, err := s.Docker.Logs(ctx, c.ID, 50, "", 8<<10)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "\n--- %s (%s) last log lines ---\n%s", c.Labels[names.LabelService], c.State, s.sanitize(ws, logs))
	}
	return b.String()
}
