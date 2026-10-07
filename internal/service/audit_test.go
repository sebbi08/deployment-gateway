package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/stack"
	"github.com/sebbi08/deployment-gateway/internal/store"
)

func lastAuditParams(t *testing.T, h *harness, action string) string {
	t.Helper()
	entries, err := h.st.ListAudit(h.ctx, store.AuditFilter{Action: action})
	if err != nil || len(entries) == 0 {
		t.Fatalf("no %s audit row: %v", action, err)
	}
	return entries[0].Params
}

// Every call, rejected ones included, writes an audit row; its params must
// stay small or one token can fill the data disk.
func TestAuditParamsAreBounded(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	huge := stack.Stack{Services: map[string]stack.Service{
		"web": {Image: "ghcr.io/me/shop:1.0.0", Command: []string{strings.Repeat("<x>", 30000)}},
	}}
	h.svc.Validate(h.ctx, p, service.DeployInput{App: "shop", Stack: huge})
	params := lastAuditParams(t, h, "validate")
	if len(params) > 8<<10 {
		t.Fatalf("audit params are %d bytes, want <= 8 KiB", len(params))
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(params), &v); err != nil {
		t.Fatalf("summary is not JSON: %v", err)
	}
	if v["app"] != "shop" || v["sha256"] == nil || v["size"] == nil || !strings.Contains(params, "ghcr.io/me/shop:1.0.0") {
		t.Fatalf("summary lacks app, size, sha256 or images: %s", params)
	}
}

func TestAuditParamsAreNotHTMLEscaped(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	s := stack.Stack{Services: map[string]stack.Service{"web": {Image: "a:1", Command: []string{"a<b>&c"}}}}
	h.svc.Validate(h.ctx, p, service.DeployInput{App: "shop", Stack: s})
	if params := lastAuditParams(t, h, "validate"); !strings.Contains(params, "a<b>&c") {
		t.Fatalf("params HTML-escaped: %s", params)
	}
}

// A gateway killed mid-call (SIGKILL on upgrade, OOM) must still leave a
// trace: the audit row is written when the call starts and completed when
// it ends.
func TestAuditRowWrittenAtCallStart(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	var during []store.AuditEntry
	h.compose.ObserveCtx = func(op string, _ context.Context) {
		if op == "pull" {
			during, _ = h.st.ListAudit(h.ctx, store.AuditFilter{Action: "deploy"})
		}
	}
	deployExample(t, h, p)
	if len(during) != 1 || during[0].Result != "started" {
		t.Fatalf("audit during the deploy = %+v, want one 'started' row", during)
	}
	after, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Action: "deploy"})
	if len(after) != 1 || after[0].Result != "ok" || after[0].ID != during[0].ID {
		t.Fatalf("audit after the deploy = %+v, want the same row completed as ok", after)
	}
}

// Redaction follows the stack schema: a service named "environment" must
// not hide its image and command in the audit log.
func TestAuditRedactsOnlyEnvironmentValues(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	s := stack.Stack{Services: map[string]stack.Service{"environment": {Image: "ghcr.io/evil/miner:1.0", Command: []string{"mine"},
		Environment: map[string]string{"KEY": "s3cr3t"}}}}
	h.svc.Validate(h.ctx, p, service.DeployInput{App: "shop", Stack: s})
	params := lastAuditParams(t, h, "validate")
	if !strings.Contains(params, "ghcr.io/evil/miner:1.0") || !strings.Contains(params, "mine") {
		t.Fatalf("service config hidden: %s", params)
	}
	if strings.Contains(params, "s3cr3t") || !strings.Contains(params, `"KEY":"***"`) {
		t.Fatalf("environment value not redacted: %s", params)
	}
}
