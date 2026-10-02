package service_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/service/servicetest"
	"github.com/sebbi08/deployment-gateway/internal/stack"
	"github.com/sebbi08/deployment-gateway/internal/store"
	"github.com/sebbi08/deployment-gateway/internal/tmpl"
)

const exampleStack = `{"services": {
  "web": {"image": "ghcr.io/me/shop:1.4.2", "environment": {"SECRET_KEY": "s3cr3t-value"},
          "expose": {"template": "public-web", "port": 8080}, "ports": [{"container": 9090, "host": 20001}],
          "healthcheck": {"command": ["wget", "-qO-", "http://localhost:8080/health"]}, "depends_on": ["db"]},
  "db": {"image": "postgres:16.4", "user": "999:999", "volume": {"path": "/var/lib/postgresql/data"}, "writable_rootfs": true}
}}`

type harness struct {
	t       *testing.T
	ctx     context.Context
	svc     *service.Service
	st      *store.Store
	compose *servicetest.FakeCompose
	docker  *servicetest.FakeDocker
	root    string
	chowns  []string
	tplID   int64
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "gw.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	compose := &servicetest.FakeCompose{}
	docker := servicetest.NewFakeDocker()
	servicetest.Wire(compose, docker)
	docker.Networks["apps"] = true
	h := &harness{t: t, ctx: context.Background(), st: st, compose: compose, docker: docker, root: root}
	h.svc = service.New(st, compose, docker, root)
	h.svc.Chown = func(path string, uid, gid int) error {
		h.chowns = append(h.chowns, fmt.Sprintf("%s %d:%d", filepath.Base(path), uid, gid))
		return nil
	}
	h.tplID, err = h.svc.SaveTemplate(h.ctx, "admin", 0, tmpl.Template{
		Name: "public-web", HostPattern: "{{app}}.{{workspace}}.apps.example.com", Networks: []string{"apps"},
		Labels: map[string]string{"traefik.enable": "true", "traefik.http.routers.{{id}}.rule": "Host(`{{host}}`)"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) workspaceWithQuota(name string, start, end int, q store.Quota) service.Principal {
	h.t.Helper()
	w, err := h.svc.CreateWorkspace(h.ctx, "admin", name, q, service.PortSettings{Start: start, End: end, BindIP: "0.0.0.0"})
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.svc.SetGrants(h.ctx, "admin", w, []int64{h.tplID}); err != nil {
		h.t.Fatal(err)
	}
	return h.principal(name)
}

func (h *harness) workspace(name string, start, end int) service.Principal {
	return h.workspaceWithQuota(name, start, end, store.BuiltinDefaultQuota())
}

func (h *harness) principal(name string) service.Principal {
	h.t.Helper()
	w, err := h.st.GetWorkspaceByName(h.ctx, name)
	if err != nil {
		h.t.Fatal(err)
	}
	return service.Principal{Workspace: w, TokenID: 1, ClientIP: "203.0.113.1"}
}

func mustStack(t *testing.T, raw string) stack.Stack {
	t.Helper()
	s, err := stack.Decode([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return *s
}

func code(err error) string {
	var se *service.Error
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}
