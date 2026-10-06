package service_test

import (
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
