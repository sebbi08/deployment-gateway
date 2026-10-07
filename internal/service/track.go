package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sebbi08/deployment-gateway/internal/store"
)

// track writes one audit row per agent call: when the call starts (result
// "started", so a killed gateway still leaves a trace) and completed by the
// returned function. Use as: defer s.track(ctx, p, action, params, &err)().
// Non-*Error errors are replaced by a generic internal error so internals
// never leak to agents.
func (s *Service) track(ctx context.Context, p Principal, action string, params any, errp *error) func() {
	e := store.AuditEntry{
		Time: time.Now(), ActorType: "agent", Actor: fmt.Sprintf("token:%d", p.TokenID), Workspace: p.Workspace.Name,
		TokenID: p.TokenID, Action: action, Params: redact(params), ClientIP: p.ClientIP,
	}
	return s.begin(ctx, e, errp)
}

func (s *Service) adminTrack(ctx context.Context, actor, workspace, action string, params any, errp *error) func() {
	e := store.AuditEntry{Time: time.Now(), ActorType: "admin", Actor: actor, Workspace: workspace, Action: action, Params: redact(params)}
	return s.begin(ctx, e, errp)
}

func (s *Service) begin(ctx context.Context, e store.AuditEntry, errp *error) func() {
	e.Result, e.Severity = "started", "info"
	id, err := s.Store.InsertAuditID(context.WithoutCancel(ctx), e)
	if err != nil {
		s.Logger.Error("audit write failed", "err", err, "action", e.Action)
	}
	return func() { s.finish(ctx, id, &e, errp) }
}

func (s *Service) finish(ctx context.Context, id int64, e *store.AuditEntry, errp *error) {
	e.DurationMS = time.Since(e.Time).Milliseconds()
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
	ctx = context.WithoutCancel(ctx)
	var err error
	if id != 0 {
		err = s.Store.FinishAudit(ctx, id, *e)
	} else {
		err = s.Store.InsertAudit(ctx, *e)
	}
	if err != nil {
		s.Logger.Error("audit write failed", "err", err, "action", e.Action)
	}
}

// AuditRejected records an agent call that was rejected before it reached
// the service (malformed body, unknown fields, input schema violations).
func (s *Service) AuditRejected(ctx context.Context, p Principal, action string, raw []byte, reason string) {
	var params any
	if err := json.Unmarshal(raw, &params); err != nil {
		params = map[string]any{"unparsable_body_bytes": len(raw)}
	}
	if len(reason) > 1024 {
		reason = reason[:1024] + "…"
	}
	e := store.AuditEntry{
		Time: time.Now(), ActorType: "agent", Actor: fmt.Sprintf("token:%d", p.TokenID), Workspace: p.Workspace.Name,
		TokenID: p.TokenID, Action: action, Params: redact(params), ClientIP: p.ClientIP,
		Result: CodeValidation, Error: reason, Severity: "warn",
	}
	if err := s.Store.InsertAudit(context.WithoutCancel(ctx), e); err != nil {
		s.Logger.Error("audit write failed", "err", err, "action", action)
	}
}

// maxAuditParams bounds the stored params of one audit row. Every call,
// rejected ones included, is audited, so unbounded rows would let a single
// token fill the data disk.
const maxAuditParams = 8 << 10

// redact serialises params with every environment value replaced by ***.
// Params larger than maxAuditParams are replaced by a summary (app, images,
// size and SHA-256 of the full redacted params).
func redact(params any) string {
	b, err := marshalPlain(params)
	if err != nil {
		return `{"error":"unserializable params"}`
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return string(b)
	}
	redactEnv(v)
	out, _ := marshalPlain(v)
	if len(out) <= maxAuditParams {
		return string(out)
	}
	sum := sha256.Sum256(out)
	summary := map[string]any{"truncated": true, "size": len(out), "sha256": hex.EncodeToString(sum[:])}
	if m, ok := v.(map[string]any); ok {
		for _, k := range []string{"app", "service", "confirm"} {
			if s, ok := m[k].(string); ok && len(s) <= 256 {
				summary[k] = s
			}
		}
		if st, ok := m["stack"].(map[string]any); ok {
			if svcs, ok := st["services"].(map[string]any); ok {
				images := map[string]string{}
				for name, svc := range svcs {
					if sm, ok := svc.(map[string]any); ok && len(name) <= 64 {
						if img, ok := sm["image"].(string); ok && len(img) <= 512 {
							images[name] = img
						}
					}
					if len(images) >= 16 {
						break
					}
				}
				summary["images"] = images
			}
		}
	}
	s, _ := marshalPlain(summary)
	return string(s)
}

// marshalPlain is json.Marshal without HTML escaping ('<' would be stored as
// a 6-byte escape) and without the trailing newline.
func marshalPlain(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// redactEnv masks environment values at their schema path only
// (stack.services.<svc>.environment), so service names cannot hide other
// fields.
func redactEnv(v any) {
	m, _ := v.(map[string]any)
	st, _ := m["stack"].(map[string]any)
	services, _ := st["services"].(map[string]any)
	for _, svc := range services {
		sm, _ := svc.(map[string]any)
		env, ok := sm["environment"].(map[string]any)
		if !ok {
			continue
		}
		for k := range env {
			env[k] = "***"
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
