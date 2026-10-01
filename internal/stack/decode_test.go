package stack

import (
	"strings"
	"testing"
)

const exampleJSON = `{
  "services": {
    "web": {
      "image": "ghcr.io/me/shop:1.4.2",
      "command": ["node", "server.js"],
      "environment": {"DATABASE_URL": "postgres://alice-shop-db:5432/shop"},
      "expose": {"template": "public-web", "port": 8080},
      "ports": [{"container": 9090, "host": 20001}],
      "healthcheck": {"command": ["wget", "-qO-", "http://localhost:8080/health"], "interval": "10s", "retries": 3},
      "depends_on": ["db"],
      "resources": {"memory": "256m", "cpus": 0.5}
    },
    "db": {
      "image": "postgres:16.4",
      "user": "999:999",
      "volume": {"path": "/var/lib/postgresql/data"},
      "writable_rootfs": true
    }
  }
}`

func TestDecodeExample(t *testing.T) {
	s, err := Decode([]byte(exampleJSON))
	if err != nil {
		t.Fatal(err)
	}
	web := s.Services["web"]
	if web.Image != "ghcr.io/me/shop:1.4.2" || web.Expose.Port != 8080 || web.Ports[0].Host != 20001 {
		t.Fatalf("unexpected web: %+v", web)
	}
	if !s.Services["db"].WritableRootfs || s.Services["db"].Volume.Path != "/var/lib/postgresql/data" {
		t.Fatalf("unexpected db: %+v", s.Services["db"])
	}
	if got := ServiceNames(s); strings.Join(got, ",") != "db,web" {
		t.Fatalf("ServiceNames = %v", got)
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := map[string]string{
		"unknown top-level":   `{"services": {}, "volumes": {}}`,
		"privileged":          `{"services": {"web": {"image": "a:1", "privileged": true}}}`,
		"cap_add":             `{"services": {"web": {"image": "a:1", "cap_add": ["SYS_ADMIN"]}}}`,
		"host bind in volume": `{"services": {"web": {"image": "a:1", "volume": {"path": "/x", "source": "/"}}}}`,
		"host_ip in ports":    `{"services": {"web": {"image": "a:1", "ports": [{"container": 1, "host": 2, "host_ip": "0.0.0.0"}]}}}`,
		"labels":              `{"services": {"web": {"image": "a:1", "labels": {"traefik.enable": "true"}}}}`,
		"trailing data":       `{"services": {}} {"services": {}}`,
		"not an object":       `["services"]`,
		"empty":               ``,
	}
	for name, raw := range cases {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestDecodeRejectsOversize(t *testing.T) {
	raw := `{"services": {"web": {"image": "a:1", "environment": {"X": "` + strings.Repeat("x", MaxRequestBytes) + `"}}}}`
	_, err := Decode([]byte(raw))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size error, got %v", err)
	}
}

func TestErrorsFormatting(t *testing.T) {
	e := Errors{{Field: "services.web.image", Message: "image is required"}, {Message: "general"}}
	if got := e.Error(); got != "services.web.image: image is required; general" {
		t.Fatalf("Error() = %q", got)
	}
}
