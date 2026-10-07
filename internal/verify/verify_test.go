package verify

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const validDoc = `name: dg-alice-shop
services:
  alice-shop-web:
    image: ghcr.io/me/shop:1.4.2
    container_name: alice-shop-web
    hostname: alice-shop-web
    command: ["run", "$$JOB"]
    environment:
      GREETING: "cost $$5"
    user: "1000:1000"
    restart: unless-stopped
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    read_only: true
    tmpfs: [/tmp]
    mem_limit: 268435456
    memswap_limit: 268435456
    cpus: 0.5
    pids_limit: 256
    logging:
      driver: json-file
      options: {max-size: 10m, max-file: "3"}
    volumes:
      - type: bind
        source: DATADIR/web
        target: /data
        bind: {create_host_path: false}
    ports:
      - {target: 8080, published: "20001", host_ip: 0.0.0.0, protocol: tcp}
    healthcheck:
      test: [CMD, wget, -qO-, http://localhost:8080/]
      interval: 30s
      retries: 3
    depends_on:
      alice-shop-db: {condition: service_started}
    networks: [dg-alice-shop, apps]
    labels:
      deploy-gateway.workspace: alice
      deploy-gateway.app: shop
      deploy-gateway.service: web
      deploy-gateway.managed: "true"
      traefik.enable: "true"
  alice-shop-db:
    image: postgres:16.4
    container_name: alice-shop-db
    hostname: alice-shop-db
    restart: unless-stopped
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    read_only: false
    tmpfs: [/tmp]
    mem_limit: 134217728
    memswap_limit: 134217728
    cpus: 0.25
    pids_limit: 256
    logging:
      driver: json-file
      options: {max-size: 10m, max-file: "3"}
    networks: [dg-alice-shop]
    labels:
      deploy-gateway.workspace: alice
      deploy-gateway.app: shop
      deploy-gateway.service: db
      deploy-gateway.managed: "true"
networks:
  dg-alice-shop: {name: dg-alice-shop}
  apps: {name: apps, external: true}
`

func setup(t *testing.T) (string, Policy) {
	t.Helper()
	dataDir := filepath.Join(t.TempDir(), "alice", "shop", "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := strings.ReplaceAll(validDoc, "DATADIR", dataDir)
	return doc, Policy{
		Workspace: "alice", App: "shop", DataDir: dataDir,
		Networks:       map[string]bool{"dg-alice-shop": true, "apps": true},
		MaxMemoryBytes: 512 << 20, MaxCPUs: 1, MaxPids: 512,
		PortRangeStart: 20000, PortRangeEnd: 20099, BindIP: "0.0.0.0",
	}
}

// mutate parses doc, applies f to the alice-shop-web service map (and the
// whole document), and re-marshals.
func mutate(t *testing.T, doc string, f func(root, web map[string]any)) []byte {
	t.Helper()
	var root map[string]any
	if err := yaml.Unmarshal([]byte(doc), &root); err != nil {
		t.Fatal(err)
	}
	web := root["services"].(map[string]any)["alice-shop-web"].(map[string]any)
	f(root, web)
	out, err := yaml.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestVerifyAcceptsValid(t *testing.T) {
	doc, p := setup(t)
	if err := Verify([]byte(doc), p); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
}

func TestVerifyRejectsMutations(t *testing.T) {
	cases := []struct {
		name string
		f    func(root, web map[string]any)
		want string
	}{
		{"privileged", func(_, w map[string]any) { w["privileged"] = true }, `forbidden key "privileged"`},
		{"cap_add", func(_, w map[string]any) { w["cap_add"] = []any{"SYS_ADMIN"} }, `forbidden key "cap_add"`},
		{"network_mode host", func(_, w map[string]any) { w["network_mode"] = "host" }, `forbidden key "network_mode"`},
		{"pid host", func(_, w map[string]any) { w["pid"] = "host" }, `forbidden key "pid"`},
		{"devices", func(_, w map[string]any) { w["devices"] = []any{"/dev/sda"} }, `forbidden key "devices"`},
		{"unknown key", func(_, w map[string]any) { w["stdin_open"] = true }, `key "stdin_open" not allowed`},
		{"cap_drop missing", func(_, w map[string]any) { delete(w, "cap_drop") }, "cap_drop"},
		{"extra security_opt", func(_, w map[string]any) {
			w["security_opt"] = []any{"no-new-privileges:true", "seccomp=unconfined"}
		}, "security_opt"},
		{"memory over max", func(_, w map[string]any) { w["mem_limit"] = 1 << 30; w["memswap_limit"] = 1 << 30 }, "mem_limit"},
		{"swap differs", func(_, w map[string]any) { w["memswap_limit"] = 1 << 29 }, "memswap_limit"},
		{"pids missing", func(_, w map[string]any) { delete(w, "pids_limit") }, "pids_limit"},
		{"bind outside data dir", func(_, w map[string]any) {
			w["volumes"].([]any)[0].(map[string]any)["source"] = "/etc"
		}, "bind source"},
		{"docker.sock", func(_, w map[string]any) {
			w["volumes"] = []any{map[string]any{"type": "bind", "source": "/var/run/docker.sock", "target": "/var/run/docker.sock"}}
		}, "docker.sock"},
		{"short volume syntax", func(_, w map[string]any) { w["volumes"] = []any{"/:/host"} }, "long syntax"},
		{"named volume", func(_, w map[string]any) {
			w["volumes"] = []any{map[string]any{"type": "volume", "source": "data", "target": "/data"}}
		}, `type must be "bind"`},
		{"short port syntax", func(_, w map[string]any) { w["ports"] = []any{"8080:80"} }, "long syntax"},
		{"port range string", func(_, w map[string]any) {
			w["ports"].([]any)[0].(map[string]any)["published"] = "20000-20010"
		}, "published"},
		{"port outside range", func(_, w map[string]any) {
			w["ports"].([]any)[0].(map[string]any)["published"] = "22"
		}, "published"},
		{"wrong host_ip", func(_, w map[string]any) {
			w["ports"].([]any)[0].(map[string]any)["host_ip"] = "192.168.1.10"
		}, "host_ip"},
		{"foreign network", func(_, w map[string]any) { w["networks"] = []any{"dg-alice-shop", "all"} }, `network "all"`},
		{"network aliases", func(_, w map[string]any) {
			w["networks"] = map[string]any{"dg-alice-shop": map[string]any{"aliases": []any{"db"}}}
		}, "aliases"},
		{"own network external", func(r, _ map[string]any) {
			r["networks"].(map[string]any)["dg-alice-shop"] = map[string]any{"name": "dg-alice-shop", "external": true}
		}, "must not be external"},
		{"foreign top-level network", func(r, _ map[string]any) {
			r["networks"].(map[string]any)["all"] = map[string]any{"name": "all", "external": true}
		}, `network "all"`},
		{"template network not external", func(r, _ map[string]any) {
			r["networks"].(map[string]any)["apps"] = map[string]any{"name": "apps"}
		}, "must be external"},
		{"foreign owner", func(_, w map[string]any) { w["labels"].(map[string]any)["deploy-gateway.workspace"] = "bob" }, "deploy-gateway.workspace"},
		{"extra gateway label", func(_, w map[string]any) { w["labels"].(map[string]any)["deploy-gateway.admin"] = "true" }, "deploy-gateway.admin"},
		{"unescaped dollar", func(_, w map[string]any) { w["environment"].(map[string]any)["X"] = "${HOME}" }, "unescaped $"},
		{"top-level volumes", func(r, _ map[string]any) { r["volumes"] = map[string]any{"data": nil} }, `top-level key "volumes"`},
		{"wrong project name", func(r, _ map[string]any) { r["name"] = "dg-bob-shop" }, "name must be"},
		{"other workspace service", func(r, w map[string]any) {
			r["services"].(map[string]any)["bob-shop-web"] = w
		}, "bob-shop-web"},
		{"healthcheck shell form", func(_, w map[string]any) {
			w["healthcheck"].(map[string]any)["test"] = []any{"CMD-SHELL", "curl localhost"}
		}, "healthcheck"},
		{"depends on unknown", func(_, w map[string]any) {
			w["depends_on"] = map[string]any{"bob-shop-db": map[string]any{"condition": "service_started"}}
		}, "depends_on"},
		{"user name", func(_, w map[string]any) { w["user"] = "root" }, "user"},
		{"restart policy", func(_, w map[string]any) { w["restart"] = "always" }, "restart"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, p := setup(t)
			err := Verify(mutate(t, doc, c.f), p)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestVerifyRejectsPortsWhenWorkspaceHasNoRange(t *testing.T) {
	doc, p := setup(t)
	p.PortRangeStart, p.PortRangeEnd = 0, 0
	if err := Verify([]byte(doc), p); err == nil || !strings.Contains(err.Error(), "ports not allowed") {
		t.Fatalf("got %v", err)
	}
}

func TestVerifyRejectsYAMLFeatures(t *testing.T) {
	doc, p := setup(t)
	withAlias := strings.Replace(doc, "  alice-shop-db:\n    image: postgres:16.4", "  alice-shop-db: &db\n    image: postgres:16.4", 1)
	if err := Verify([]byte(withAlias), p); err == nil || !strings.Contains(err.Error(), "anchor") {
		t.Fatalf("anchor not rejected: %v", err)
	}
	withTag := strings.Replace(doc, "image: postgres:16.4", "image: !custom postgres:16.4", 1)
	if err := Verify([]byte(withTag), p); err == nil || !strings.Contains(err.Error(), "tag") {
		t.Fatalf("custom tag not rejected: %v", err)
	}
}

func TestVerifyRejectsSymlinkedDataDir(t *testing.T) {
	doc, p := setup(t)
	web := filepath.Join(p.DataDir, "web")
	if err := os.Remove(web); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", web); err != nil {
		t.Fatal(err)
	}
	if err := Verify([]byte(doc), p); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink not rejected: %v", err)
	}
}

func TestVerifyRejectsMultipleDocuments(t *testing.T) {
	doc, p := setup(t)
	doc += "---\nservices:\n  alice-shop-web:\n    privileged: true\n"
	if err := Verify([]byte(doc), p); err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("got %v", err)
	}
}

func TestHealthcheckStartKeysAllowed(t *testing.T) {
	doc, p := setup(t)
	ok := strings.Replace(doc, "      retries: 3\n", "      retries: 3\n      start_period: 30s\n      start_interval: 2s\n", 1)
	if err := Verify([]byte(ok), p); err != nil {
		t.Fatalf("start_period/start_interval rejected: %v", err)
	}
	bad := strings.Replace(doc, "      retries: 3\n", "      retries: 3\n      timeout: 1s\n", 1)
	if err := Verify([]byte(bad), p); err == nil {
		t.Fatal("other healthcheck keys must stay rejected")
	}
}

// Every forbidden key is rejected on its own (spec §19).
func TestEveryForbiddenKeyIsRejected(t *testing.T) {
	doc, p := setup(t)
	for k := range forbiddenKeys {
		bad := strings.Replace(doc, "  alice-shop-web:\n", "  alice-shop-web:\n    "+k+": x\n", 1)
		err := Verify([]byte(bad), p)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("forbidden key %q", k)) {
			t.Errorf("%s: %v", k, err)
		}
	}
}
