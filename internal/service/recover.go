package service

import (
	"context"
	"strings"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/names"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

// RecoverInterrupted runs at startup. It handles apps a stopped gateway left
// "pending" (their containers may never have passed the runtime check) and
// apps whose secret apply was running (recreated containers may not have
// been checked). It applies the plan-independent runtime checks (the stack
// is not stored) and removes violating containers. A pending deploy becomes
// "interrupted" so the next deploy does not rotate the unverified render
// into prev; a running apply becomes "interrupted" and its refs stay stale.
func (s *Service) RecoverInterrupted(ctx context.Context) error {
	pending, err := s.Store.AppsWithResult(ctx, "pending")
	if err != nil {
		return err
	}
	for _, ref := range pending {
		e, violated := s.recoverApp(ctx, ref, "recover_interrupted")
		result := "interrupted"
		if violated {
			result = CodeSecurity
			s.Logger.Error("security violation in an interrupted deploy; containers removed", "workspace", ref.Workspace, "app", ref.App.Name, "error", e.Error)
		} else {
			s.Logger.Warn("deploy was interrupted by a gateway stop; redeploy the app", "workspace", ref.Workspace, "app", ref.App.Name)
		}
		e.Result = result
		if err := s.Store.RecordDeploy(ctx, ref.App.ID, result); err != nil {
			return err
		}
		s.insertAudit(ctx, e)
	}
	applying, err := s.Store.AppsWithApplyState(ctx, "running")
	if err != nil {
		return err
	}
	for _, ref := range applying {
		e, violated := s.recoverApp(ctx, ref, "recover_interrupted_apply")
		e.Result = "interrupted"
		if violated {
			e.Result = CodeSecurity
			s.Logger.Error("security violation in an interrupted secret apply; containers removed", "workspace", ref.Workspace, "app", ref.App.Name, "error", e.Error)
		} else {
			s.Logger.Warn("secret apply was interrupted by a gateway stop; re-apply the secret", "workspace", ref.Workspace, "app", ref.App.Name)
		}
		if err := s.Store.SetSecretApply(ctx, ref.App.ID, "interrupted", "gateway stopped during apply"); err != nil {
			return err
		}
		s.insertAudit(ctx, e)
	}
	return nil
}

// recoverApp checks the containers of an app left in an unfinished
// operation and removes them on a violation. It returns the audit entry
// (without result) and whether a violation was found.
func (s *Service) recoverApp(ctx context.Context, ref store.AppRef, action string) (store.AuditEntry, bool) {
	ws, app := ref.Workspace, ref.App.Name
	e := store.AuditEntry{Time: time.Now(), ActorType: "system", Actor: "startup", Workspace: ws,
		Action: action, Params: redact(map[string]string{"app": app}), Severity: "warn"}
	violations := s.preExistingViolations(ctx, ws, app)
	if len(violations) == 0 {
		return e, false
	}
	e.Error, e.Severity = strings.Join(violations, "; "), "critical"
	if err := s.removeAppContainers(ctx, ws, app); err != nil {
		e.Error += " | direct container removal failed: " + err.Error()
	}
	if _, err := s.Compose.Down(ctx, s.composeFile(ws, app), names.Project(ws, app)); err != nil {
		e.Error += " | compose down failed: " + err.Error()
	}
	return e, true
}

func (s *Service) insertAudit(ctx context.Context, e store.AuditEntry) {
	if err := s.Store.InsertAudit(ctx, e); err != nil {
		s.Logger.Error("audit write failed", "err", err, "action", e.Action)
	}
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
			DataDir: s.dataDir(ws, app), PreExisting: true, ExtraRuntimes: s.ExtraRuntimes})...)
	}
	return v
}
