package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sebbi08/deployment-gateway/internal/dockerx"
	"github.com/sebbi08/deployment-gateway/internal/service"
	"github.com/sebbi08/deployment-gateway/internal/store"
	"github.com/sebbi08/deployment-gateway/internal/tmpl"
)

func TestDeployPullsBeforeUp(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	file := filepath.Join(h.root, "alice", "shop", "docker-compose.yml")
	if calls := h.compose.Calls(); !slices.Equal(calls, []string{"pull dg-alice-shop", "up dg-alice-shop " + file}) {
		t.Fatalf("compose calls = %v", calls)
	}
}

func TestDeployRejectsReservedImageLabels(t *testing.T) {
	cases := map[string]string{
		"traefik router":       "traefik.http.routers.pwn.rule",
		"traefik mixed case":   "Traefik.HTTP.Routers.pwn.rule",
		"owner label":          "deploy-gateway.workspace",
		"compose label":        "com.docker.compose.project",
		"template namespace":   "wud.tag.include",
		"bare template prefix": "wud",
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			// A template that is not granted still reserves its namespace.
			if _, err := h.svc.SaveTemplate(h.ctx, "admin", 0, tmpl.Template{Name: "watch", Labels: map[string]string{"wud.watch": "true"}}); err != nil {
				t.Fatal(err)
			}
			p := h.workspace("alice", 20000, 20099)
			h.docker.Images["postgres:16.4"] = map[string]string{key: "Host(`admin.example.com`)", "org.opencontainers.image.title": "pg"}
			_, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
			var se *service.Error
			if !errors.As(err, &se) || se.Code != service.CodeValidation || len(se.Fields) != 1 ||
				se.Fields[0].Field != "services.db.image" || !strings.Contains(se.Fields[0].Message, key) {
				t.Fatalf("expected validation_failed on services.db.image naming %q, got %v", key, err)
			}
			if calls := h.compose.Calls(); !slices.Equal(calls, []string{"pull dg-alice-shop"}) {
				t.Fatalf("up must not run: %v", calls)
			}
			if len(h.docker.Containers) != 0 {
				t.Fatal("containers were created")
			}
			if _, err := h.st.GetApp(h.ctx, p.Workspace.ID, "shop"); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("rejected first deploy must leave no app record: %v", err)
			}
		})
	}
}

func TestDeployAcceptsHarmlessImageLabels(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.docker.Images["ghcr.io/me/shop:1.4.2"] = map[string]string{"org.opencontainers.image.title": "shop", "maintainer": "me", "traefikish": "x"}
	deployExample(t, h, p)
	if got := h.docker.Facts["alice-shop-web"].Labels["org.opencontainers.image.title"]; got != "shop" {
		t.Fatalf("image label not merged into container (fake broken?): %q", got)
	}
}

func TestDeployImageLabelRejectionKeepsLastGoodPrev(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	version := func(v string) service.DeployInput {
		return service.DeployInput{App: "shop", Stack: mustStack(t, strings.Replace(exampleStack, "shop:1.4.2", "shop:"+v, 1))}
	}
	if _, err := h.svc.Deploy(h.ctx, p, version("1.0.0")); err != nil {
		t.Fatal(err)
	}
	h.docker.Images["ghcr.io/me/shop:2.0.0"] = map[string]string{"traefik.http.routers.x.rule": "Host(`x`)"}
	if _, err := h.svc.Deploy(h.ctx, p, version("2.0.0")); code(err) != service.CodeValidation {
		t.Fatalf("expected validation_failed, got %v", err)
	}
	if _, err := h.svc.Deploy(h.ctx, p, version("3.0.0")); err != nil {
		t.Fatal(err)
	}
	prev, _ := os.ReadFile(filepath.Join(h.root, "alice", "shop", "docker-compose.prev.yml"))
	if !strings.Contains(string(prev), "shop:1.0.0") {
		t.Fatalf("prev must hold the last successful render (1.0.0):\n%s", prev)
	}
}

func TestDeployPullFailure(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.compose.PullErr = errors.New("compose pull failed with exit code 1")
	h.compose.PullOut = "pull access denied for " + h.root + "/secret"
	_, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if code(err) != service.CodeDeployFailed || !strings.Contains(err.Error(), "pull access denied") {
		t.Fatalf("expected deploy_failed with pull output, got %v", err)
	}
	if strings.Contains(err.Error(), h.root) {
		t.Fatalf("host path leaked to agent: %v", err)
	}
	if calls := h.compose.Calls(); !slices.Equal(calls, []string{"pull dg-alice-shop"}) {
		t.Fatalf("up must not run after failed pull: %v", calls)
	}
	if _, err := h.st.GetApp(h.ctx, p.Workspace.ID, "shop"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed first pull must leave no app record: %v", err)
	}
}

// Layer 3: a reserved-namespace label that appears on the container without
// being rendered (e.g. the image changed between inspect and up) is caught.
func TestDeployRuntimeRejectsUnrenderedReservedLabel(t *testing.T) {
	for _, key := range []string{"traefik.http.routers.pwn.rule", "wud.watch", "deploy-gateway.extra"} {
		t.Run(key, func(t *testing.T) {
			h := newHarness(t)
			if _, err := h.svc.SaveTemplate(h.ctx, "admin", 0, tmpl.Template{Name: "watch", Labels: map[string]string{"wud.watch": "true"}}); err != nil {
				t.Fatal(err)
			}
			p := h.workspace("alice", 20000, 20099)
			h.compose.AfterUp = func() {
				h.docker.MutateFacts("alice-shop-db", func(f *dockerx.Facts) { f.Labels[key] = "Host(`admin.example.com`)" })
			}
			_, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
			if code(err) != service.CodeSecurity {
				t.Fatalf("expected security_violation, got %v", err)
			}
			if calls := h.compose.Calls(); calls[len(calls)-1] != "down dg-alice-shop" {
				t.Fatalf("containers not torn down: %v", calls)
			}
		})
	}
}

// Label-driven host tools with Docker socket access act on image labels;
// ofelia's job-run labels, for example, start new containers with arbitrary
// options. Their namespaces are reserved like Traefik's, and operators can
// reserve more.
func TestDeployRejectsLabelDrivenToolNamespaces(t *testing.T) {
	for _, tc := range []struct{ key, extra string }{
		{"ofelia.job-run.pwn.image", ""},
		{"chadburn.job-run.pwn.volume", ""},
		{"com.centurylinklabs.watchtower.lifecycle.pre-update", ""},
		{"docker-volume-backup.archive-pre", ""},
		{"mytool.run", "mytool."},
	} {
		h := newHarness(t)
		if tc.extra != "" {
			h.svc.ExtraReservedPrefixes = []string{tc.extra}
		}
		p := h.workspace("alice", 20000, 20099)
		h.docker.Images["postgres:16.4"] = map[string]string{tc.key: "x"}
		_, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
		if code(err) != service.CodeValidation || !strings.Contains(err.Error(), tc.key) {
			t.Errorf("%s: %v", tc.key, err)
		}
	}
}
