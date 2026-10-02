package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/store"
)

// track writes one audit row per agent call. Non-*Error errors are replaced
// by a generic internal error so internals never leak to agents.
func (s *Service) track(ctx context.Context, p Principal, action string, params any, errp *error, start time.Time) {
	e := store.AuditEntry{
		Time: start, ActorType: "agent", Actor: fmt.Sprintf("token:%d", p.TokenID), Workspace: p.Workspace.Name,
		TokenID: p.TokenID, Action: action, Params: redact(params), ClientIP: p.ClientIP,
	}
	s.finish(ctx, &e, errp, start)
}

func (s *Service) adminTrack(ctx context.Context, actor, workspace, action string, params any, errp *error, start time.Time) {
	e := store.AuditEntry{Time: start, ActorType: "admin", Actor: actor, Workspace: workspace, Action: action, Params: redact(params)}
	s.finish(ctx, &e, errp, start)
}

func (s *Service) finish(ctx context.Context, e *store.AuditEntry, errp *error, start time.Time) {
	e.DurationMS = time.Since(start).Milliseconds()
	e.Result, e.Severity = "ok", "info"
	if *errp != nil {
		var se *Error
		if !errors.As(*errp, &se) {
			se = &Error{Code: CodeInternal, Message: "internal error", detail: (*errp).Error()}
			*errp = se
		}
		e.Result, e.Error, e.Severity = se.Code, se.Message, "warn"
		if se.detail != "" {
			e.Error += " | " + se.detail
		}
		if se.critical {
			e.Severity = "critical"
			s.Logger.Error("security violation", "workspace", e.Workspace, "action", e.Action, "error", e.Error)
		}
	}
	if err := s.Store.InsertAudit(context.WithoutCancel(ctx), *e); err != nil {
		s.Logger.Error("audit write failed", "err", err, "action", e.Action)
	}
}

// redact serialises params with every environment value replaced by ***.
func redact(params any) string {
	b, err := json.Marshal(params)
	if err != nil {
		return `{"error":"unserializable params"}`
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return string(b)
	}
	redactEnv(v)
	out, _ := json.Marshal(v)
	return string(out)
}

func redactEnv(v any) {
	switch n := v.(type) {
	case map[string]any:
		for k, child := range n {
			if env, ok := child.(map[string]any); ok && k == "environment" {
				for ek := range env {
					env[ek] = "***"
				}
				continue
			}
			redactEnv(child)
		}
	case []any:
		for _, c := range n {
			redactEnv(c)
		}
	}
}

const maxAgentOutput = 8 << 10

// sanitize hides host paths and truncates compose/docker output for agents.
func (s *Service) sanitize(ws, out string) string {
	out = strings.ReplaceAll(out, s.workspaceDir(ws), "<workspace>")
	out = strings.ReplaceAll(out, s.Root, "<root>")
	if len(out) > maxAgentOutput {
		out = "…" + out[len(out)-maxAgentOutput:]
	}
	return out
}
