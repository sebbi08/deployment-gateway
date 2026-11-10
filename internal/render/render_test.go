package render

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v4"

	"github.com/sebbi08/deployment-gateway/internal/stack"
	"github.com/sebbi08/deployment-gateway/internal/tmpl"
)

var update = flag.Bool("update", false, "rewrite golden files")

const inputJSON = `{"services": {
  "web": {"image": "ghcr.io/me/shop:1.4.2",
          "environment": {"GREETING": "cost ${HOME} $5"},
          "expose": {"template": "public-web", "port": 8080},
          "ports": [{"container": 9090, "host": 20001}],
          "healthcheck": {"command": ["wget", "-qO-", "http://localhost:8080/health"]},
          "depends_on": ["db"],
          "resources": {"memory": "256m", "cpus": 0.5}},
  "db": {"image": "postgres:16.4", "user": "999:999",
         "volume": {"path": "/var/lib/postgresql/data"}, "writable_rootfs": true,
         "healthcheck": {"command": ["pg_isready"], "interval": "10s", "retries": 5}},
  "worker": {"image": "ghcr.io/me/worker:1.0.0", "command": ["run", "$JOB"], "depends_on": ["web"]}
}}`

func testInput(t *testing.T) Input {
	t.Helper()
	s, err := stack.Decode([]byte(inputJSON))
	if err != nil {
		t.Fatal(err)
	}
	return Input{
		Workspace: "alice",
		App:       "shop",
		Stack:     s,
		Templates: map[string]tmpl.Template{"public-web": {
			Name:        "public-web",
			HostPattern: "{{app}}.{{workspace}}.apps.example.com",
			Networks:    []string{"apps"},
			Labels: map[string]string{
				"traefik.enable":                                        "true",
				"traefik.http.routers.{{id}}.rule":                      "Host(`{{host}}`)",
				"traefik.http.services.{{id}}.loadbalancer.server.port": "{{port}}",
				"x.regex": "^/api$",
			},
		}},
		Defaults: stack.Defaults{MemoryBytes: 128 << 20, CPUs: 0.25, Pids: 256},
		DataDir:  "/srv/ws/alice/shop/data",
		BindIP:   "0.0.0.0",
	}
}

func parse(t *testing.T, y []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(y, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func service(t *testing.T, doc map[string]any, key string) map[string]any {
	t.Helper()
	s, ok := doc["services"].(map[string]any)[key].(map[string]any)
	if !ok {
		t.Fatalf("service %q missing", key)
	}
	return s
}

func mustRender(t *testing.T, in Input) Output {
	t.Helper()
	out, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRenderHardening(t *testing.T) {
	doc := parse(t, mustRender(t, testInput(t)).YAML)
	if doc["name"] != "dg-alice-shop" {
		t.Fatalf("name = %v", doc["name"])
	}
	for _, key := range []string{"alice-shop-web", "alice-shop-db", "alice-shop-worker"} {
		s := service(t, doc, key)
		checks := map[string]any{
			"container_name": key,
			"hostname":       key,
			"restart":        "unless-stopped",
			"cap_drop":       []any{"ALL"},
			"security_opt":   []any{"no-new-privileges:true"},
			"tmpfs":          []any{"/tmp"},
			"logging":        map[string]any{"driver": "json-file", "options": map[string]any{"max-size": "10m", "max-file": "3"}},
		}
		for k, want := range checks {
			if !reflect.DeepEqual(s[k], want) {
				t.Errorf("%s.%s = %v, want %v", key, k, s[k], want)
			}
		}
		if s["mem_limit"] != s["memswap_limit"] || s["mem_limit"] == nil || s["pids_limit"] == nil || s["cpus"] == nil {
			t.Errorf("%s: limits missing: %v", key, s)
		}
		labels := s["labels"].(map[string]any)
		if labels["deploy-gateway.workspace"] != "alice" || labels["deploy-gateway.app"] != "shop" || labels["deploy-gateway.managed"] != "true" {
			t.Errorf("%s: owner labels = %v", key, labels)
		}
	}
	if service(t, doc, "alice-shop-web")["read_only"] != true || service(t, doc, "alice-shop-db")["read_only"] != false {
		t.Error("read_only not rendered from writable_rootfs")
	}
	if service(t, doc, "alice-shop-web")["mem_limit"] != 256<<20 || service(t, doc, "alice-shop-db")["mem_limit"] != 128<<20 {
		t.Error("memory limits not resolved from resources/defaults")
	}
}

func TestRenderEscapesDollar(t *testing.T) {
	doc := parse(t, mustRender(t, testInput(t)).YAML)
	env := service(t, doc, "alice-shop-web")["environment"].(map[string]any)
	if env["GREETING"] != "cost $${HOME} $$5" {
		t.Errorf("GREETING = %q", env["GREETING"])
	}
	cmd := service(t, doc, "alice-shop-worker")["command"]
	if !reflect.DeepEqual(cmd, []any{"run", "$$JOB"}) {
		t.Errorf("command = %v", cmd)
	}
	labels := service(t, doc, "alice-shop-web")["labels"].(map[string]any)
	if labels["x.regex"] != "^/api$$" {
		t.Errorf("template label not escaped: %q", labels["x.regex"])
	}
}

func TestRenderNetworksAndExpose(t *testing.T) {
	out := mustRender(t, testInput(t))
	doc := parse(t, out.YAML)
	if got := service(t, doc, "alice-shop-web")["networks"]; !reflect.DeepEqual(got, []any{"dg-alice-shop", "apps"}) {
		t.Errorf("web networks = %v", got)
	}
	if got := service(t, doc, "alice-shop-db")["networks"]; !reflect.DeepEqual(got, []any{"dg-alice-shop"}) {
		t.Errorf("db networks = %v", got)
	}
	nets := doc["networks"].(map[string]any)
	if !reflect.DeepEqual(nets["apps"], map[string]any{"name": "apps", "external": true}) ||
		!reflect.DeepEqual(nets["dg-alice-shop"], map[string]any{"name": "dg-alice-shop"}) {
		t.Errorf("top-level networks = %v", nets)
	}
	if !reflect.DeepEqual(out.Hosts, map[string]string{"web": "shop.alice.apps.example.com"}) {
		t.Errorf("hosts = %v", out.Hosts)
	}
	labels := service(t, doc, "alice-shop-web")["labels"].(map[string]any)
	if labels["traefik.http.routers.alice-shop-web.rule"] != "Host(`shop.alice.apps.example.com`)" ||
		labels["traefik.http.services.alice-shop-web.loadbalancer.server.port"] != "8080" {
		t.Errorf("template labels = %v", labels)
	}
}

func TestRenderVolumeAndPorts(t *testing.T) {
	out := mustRender(t, testInput(t))
	doc := parse(t, out.YAML)
	vols := service(t, doc, "alice-shop-db")["volumes"]
	want := []any{map[string]any{
		"type": "bind", "source": "/srv/ws/alice/shop/data/db", "target": "/var/lib/postgresql/data",
		"bind": map[string]any{"create_host_path": false},
	}}
	if !reflect.DeepEqual(vols, want) {
		t.Errorf("volumes = %v", vols)
	}
	ports := service(t, doc, "alice-shop-web")["ports"]
	wantPorts := []any{map[string]any{"target": 9090, "published": "20001", "host_ip": "0.0.0.0", "protocol": "tcp"}}
	if !reflect.DeepEqual(ports, wantPorts) {
		t.Errorf("ports = %v", ports)
	}
	if !reflect.DeepEqual(out.Ports, []PublishedPort{{Service: "web", Container: 9090, Host: 20001, Protocol: "tcp", HostIP: "0.0.0.0"}}) {
		t.Errorf("out.Ports = %v", out.Ports)
	}
}

func TestRenderDependsOnAndHealthcheck(t *testing.T) {
	doc := parse(t, mustRender(t, testInput(t)).YAML)
	if got := service(t, doc, "alice-shop-web")["depends_on"]; !reflect.DeepEqual(got, map[string]any{"alice-shop-db": map[string]any{"condition": "service_healthy"}}) {
		t.Errorf("web depends_on = %v", got)
	}
	if got := service(t, doc, "alice-shop-worker")["depends_on"]; !reflect.DeepEqual(got, map[string]any{"alice-shop-web": map[string]any{"condition": "service_healthy"}}) {
		t.Errorf("worker depends_on = %v", got)
	}
	hc := service(t, doc, "alice-shop-db")["healthcheck"]
	if !reflect.DeepEqual(hc, map[string]any{"test": []any{"CMD", "pg_isready"}, "interval": "10s", "retries": 5, "start_period": "10s", "start_interval": "2s"}) {
		t.Errorf("db healthcheck = %v", hc)
	}
	hc = service(t, doc, "alice-shop-web")["healthcheck"]
	if hc.(map[string]any)["interval"] != "30s" || hc.(map[string]any)["retries"] != 3 {
		t.Errorf("web healthcheck defaults = %v", hc)
	}
}

func TestRenderMissingTemplate(t *testing.T) {
	in := testInput(t)
	in.Templates = nil
	if _, err := Render(in); err == nil {
		t.Fatal("expected error for missing template")
	}
}

func TestRenderGolden(t *testing.T) {
	a := mustRender(t, testInput(t)).YAML
	b := mustRender(t, testInput(t)).YAML
	if !bytes.Equal(a, b) {
		t.Fatal("render output is not deterministic")
	}
	golden := filepath.Join("testdata", "basic.golden.yml")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, a, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with -update once): %v", err)
	}
	if !bytes.Equal(a, want) {
		t.Fatalf("output differs from %s; rerun with -update and review the diff\n%s", golden, a)
	}
}

func TestRenderOutputLabelsAreUnescaped(t *testing.T) {
	in := testInput(t)
	tp := in.Templates["public-web"]
	tp.Labels["x.cost"] = "$5"
	in.Templates["public-web"] = tp
	out := mustRender(t, in)
	web := out.Labels["web"]
	want := map[string]string{
		"traefik.enable": "true",
		"traefik.http.routers.alice-shop-web.rule":                      "Host(`shop.alice.apps.example.com`)",
		"traefik.http.services.alice-shop-web.loadbalancer.server.port": "8080",
		"x.regex":                  "^/api$",
		"x.cost":                   "$5",
		"deploy-gateway.workspace": "alice",
		"deploy-gateway.app":       "shop",
		"deploy-gateway.service":   "web",
		"deploy-gateway.managed":   "true",
	}
	if !reflect.DeepEqual(web, want) {
		t.Fatalf("web labels = %v", web)
	}
	if db := out.Labels["db"]; len(db) != 4 || db["deploy-gateway.service"] != "db" {
		t.Fatalf("db labels = %v", db)
	}
	if got := service(t, parse(t, out.YAML), "alice-shop-web")["labels"].(map[string]any)["x.cost"]; got != "$$5" {
		t.Fatalf("yaml label must stay escaped, got %v", got)
	}
}

// Docker runs the first probe only after a full interval; with an interval
// near DEPLOY_TIMEOUT, up --wait gives up first. During start_period probes
// run every start_interval.
func TestRenderHealthcheckProbesEarly(t *testing.T) {
	out, err := Render(testInput(t))
	if err != nil {
		t.Fatal(err)
	}
	hc := parse(t, out.YAML)["services"].(map[string]any)["alice-shop-db"].(map[string]any)["healthcheck"].(map[string]any)
	if hc["start_period"] != "10s" || hc["start_interval"] != "2s" {
		t.Fatalf("healthcheck = %v, want start_period = interval (10s) and start_interval 2s", hc)
	}
}

// Owner labels are written after template labels, so a template label key
// that renders to an owner-label key cannot change the owner.
func TestRenderOwnerLabelsOverrideTemplateLabels(t *testing.T) {
	in := testInput(t)
	in.Templates["public-web"].Labels["deploy-gateway.workspace"] = "bob"
	in.Templates["public-web"].Labels["deploy-gateway.app"] = "blog"
	in.Templates["public-web"].Labels["deploy-gateway.service"] = "db"
	in.Templates["public-web"].Labels["deploy-gateway.managed"] = "false"
	out := mustRender(t, in)
	want := map[string]string{
		"deploy-gateway.workspace": "alice", "deploy-gateway.app": "shop",
		"deploy-gateway.service": "web", "deploy-gateway.managed": "true",
	}
	labels := service(t, parse(t, out.YAML), "alice-shop-web")["labels"].(map[string]any)
	for k, v := range want {
		if labels[k] != v || out.Labels["web"][k] != v {
			t.Errorf("%s = %v (yaml) / %q (out), want %q", k, labels[k], out.Labels["web"][k], v)
		}
	}
}
