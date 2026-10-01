package render_test

import (
	"testing"

	"github.com/sebmahr/deploy-gateway/internal/render"
	"github.com/sebmahr/deploy-gateway/internal/stack"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
	"github.com/sebmahr/deploy-gateway/internal/verify"
)

var seeds = []string{
	`{"services": {"web": {"image": "nginx:1.27.0"}}}`,
	`{"services": {"web": {"image": "ghcr.io/me/shop:1.4.2", "environment": {"A": "${HOME} $$ $"}, "expose": {"template": "public-web", "port": 8080}, "ports": [{"container": 80, "host": 20000}, {"container": 53, "host": 20001, "protocol": "udp"}]}}}`,
	`{"services": {"db": {"image": "postgres:16.4", "user": "999:999", "volume": {"path": "/var/lib/postgresql/data"}, "writable_rootfs": true, "healthcheck": {"command": ["pg_isready"]}}, "web": {"image": "a:1", "depends_on": ["db"], "command": ["$x"], "entrypoint": ["/bin/$y"], "resources": {"memory": "64m", "cpus": 0.1, "pids": 64}}}}`,
	`{"services": {"a": {"image": "a:1", "expose": {"template": "internal", "port": 1}}, "b": {"image": "b@sha256:0000000000000000000000000000000000000000000000000000000000000000", "expose": {"template": "internal", "port": 2}}}}`,
}

func FuzzRenderVerify(f *testing.F) {
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	templates := map[string]tmpl.Template{
		"public-web": {Name: "public-web", HostPattern: "{{app}}.{{workspace}}.apps.example.com", Networks: []string{"apps"},
			Labels: map[string]string{"traefik.http.routers.{{id}}.rule": "Host(`{{host}}`)", "x": "^/a$"}},
		"internal": {Name: "internal", Networks: []string{"apps"}, Labels: map[string]string{"traefik.enable": "false"}},
	}
	grants := stack.Grants{}
	for n, t := range templates {
		grants[n] = t.Shareable()
	}
	lim := stack.Limits{MaxServices: 4, MaxMemoryBytes: 1 << 30, MaxCPUs: 2, MaxPids: 2048, PortRangeStart: 20000, PortRangeEnd: 20099}
	defaults := stack.Defaults{MemoryBytes: 128 << 20, CPUs: 0.25, Pids: 256}
	dataDir := "/nonexistent-dg-fuzz/alice/shop/data"
	policy := verify.Policy{
		Workspace: "alice", App: "shop", DataDir: dataDir,
		Networks:       map[string]bool{"dg-alice-shop": true, "apps": true},
		MaxMemoryBytes: lim.MaxMemoryBytes, MaxCPUs: lim.MaxCPUs, MaxPids: lim.MaxPids,
		PortRangeStart: lim.PortRangeStart, PortRangeEnd: lim.PortRangeEnd, BindIP: "127.0.0.1",
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		s, err := stack.Decode(raw)
		if err != nil {
			return
		}
		if errs := stack.Validate(s, lim, grants); errs != nil {
			return
		}
		out, err := render.Render(render.Input{
			Workspace: "alice", App: "shop", Stack: s, Templates: templates,
			Defaults: defaults, DataDir: dataDir, BindIP: "127.0.0.1",
		})
		if err != nil {
			t.Fatalf("render failed on a valid stack: %v", err)
		}
		if err := verify.Verify(out.YAML, policy); err != nil {
			t.Fatalf("verifier rejected rendered output: %v\n%s", err, out.YAML)
		}
	})
}
