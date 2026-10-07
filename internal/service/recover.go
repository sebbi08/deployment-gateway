package service

import (
	"context"
	"strings"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/names"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

// RecoverInterrupted handles apps a stopped gateway left "pending": their
// containers may never have passed the runtime check. It runs at startup,
// applies the plan-independent runtime checks (the stack is not stored),
// removes violating containers, and marks the deploy "interrupted" so the
// next deploy does not rotate the unverified render into prev.
func (s *Service) RecoverInterrupted(ctx context.Context) error {
	pending, err := s.Store.AppsWithResult(ctx, "pending")
	if err != nil {
		return err
	}
	for _, ref := range pending {
		ws, app := ref.Workspace, ref.App.Name
		e := store.AuditEntry{Time: time.Now(), ActorType: "system", Actor: "startup", Workspace: ws,
			Action: "recover_interrupted", Params: redact(map[string]string{"app": app})}
		result := "interrupted"
		violations := s.preExistingViolations(ctx, ws, app)
		if len(violations) > 0 {
			result = CodeSecurity
			e.Error, e.Severity = strings.Join(violations, "; "), "critical"
			if err := s.removeAppContainers(ctx, ws, app); err != nil {
				e.Error += " | direct container removal failed: " + err.Error()
			}
			if _, err := s.Compose.Down(ctx, s.composeFile(ws, app), names.Project(ws, app)); err != nil {
				e.Error += " | compose down failed: " + err.Error()
			}
			s.Logger.Error("security violation in an interrupted deploy; containers removed", "workspace", ws, "app", app, "error", e.Error)
		} else {
			s.Logger.Warn("deploy was interrupted by a gateway stop; redeploy the app", "workspace", ws, "app", app)
		}
		e.Result = result
		if e.Severity == "" {
			e.Severity = "warn"
		}
		if err := s.Store.RecordDeploy(ctx, ref.App.ID, result); err != nil {
			return err
		}
		if err := s.Store.InsertAudit(ctx, e); err != nil {
			s.Logger.Error("audit write failed", "err", err, "action", e.Action)
		}
	}
	return nil
}

// preExistingViolations applies the runtime checks that do not need the
// rendered plan to every container of the app.
func (s *Service) preExistingViolations(ctx context.Context, ws, app string) []string {
	containers, err := s.Docker.ProjectContainers(ctx, ws, app)
	if err != nil {
		return []string{"cannot list containers: " + err.Error()}
	}
	var v []string
	for _, c := range containers {
		facts, err := s.Docker.Inspect(ctx, c.ID)
		if err != nil {
			v = append(v, c.Name+": inspect failed: "+err.Error())
			continue
		}
		v = append(v, dockerx.CheckRuntime(facts, dockerx.RuntimePolicy{Workspace: ws, App: app,
			DataDir: s.dataDir(ws, app), PreExisting: true})...)
	}
	return v
}
