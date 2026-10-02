# deploy-gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a self-hosted Go service that lets AI agents deploy containerized apps through MCP/HTTP tools into admin-managed workspaces, without ever letting agent input reach security-relevant container configuration.

**Architecture:** One Go binary with two listeners (agent `:8080` for MCP + JSON API, admin `:8081` for an admin JSON API plus the embedded React admin app). Agent input is a strict JSON compose subset → validated (`stack`) → rendered into a hardened `docker-compose.yml` (`render`) → independently re-verified (`verify`) → run with the standalone `docker-compose` binary (`runner`) → containers re-inspected via the Docker API (`dockerx`). State lives in SQLite (`store`); orchestration in `service`.

**Tech Stack:** Go 1.27, `github.com/modelcontextprotocol/go-sdk` v1.8.0, `github.com/moby/moby/client` v0.6.0 (+ `github.com/moby/moby/api` v1.56.0), `modernc.org/sqlite` v1.60.1, `go.yaml.in/yaml/v3` v3.0.5, `golang.org/x/crypto` v0.57.0, Docker Compose v5.5.1 standalone binary. Admin frontend: Node 24, React 19.3, TypeScript 7.0, Vite 8.3, Mantine 9.6, React Router 8.4, TanStack Query 5.104, Vitest 5.0 + React Testing Library 16.3.

**Spec:** `docs/superpowers/specs/2026-10-01-deploy-gateway-design.md`

## Global Constraints

- Module path: `github.com/sebmahr/deploy-gateway`; `go 1.27` in `go.mod`.
- Third-party dependencies are limited to: go-sdk v1.8.0, moby client v0.6.0 / api v1.56.0, `github.com/containerd/errdefs` (version pulled by moby client), modernc sqlite v1.60.1, `go.yaml.in/yaml/v3` v3.0.5 (maintained successor of `gopkg.in/yaml.v3`, identical API), `golang.org/x/crypto` v0.57.0. Nothing else.
- Names: workspace `^[a-z][a-z0-9]{1,15}$`; app `^[a-z][a-z0-9-]{0,22}[a-z0-9]$`; service `^[a-z][a-z0-9]{0,15}$`; template `^[a-z][a-z0-9-]{1,30}$`. Container/compose key/DNS name `<ws>-<app>-<svc>`; compose project and private network `dg-<ws>-<app>`. No short aliases on any network.
- Owner labels: `deploy-gateway.workspace`, `deploy-gateway.app`, `deploy-gateway.service`, `deploy-gateway.managed=true`.
- Every rendered service: `cap_drop: [ALL]`, `security_opt: ["no-new-privileges:true"]`, `read_only: true` unless `writable_rootfs`, `tmpfs: ["/tmp"]`, `mem_limit` = `memswap_limit`, `cpus`, `pids_limit`, `restart: unless-stopped`, json-file logging `max-size: 10m`, `max-file: "3"`.
- Volumes: bind mounts only, host side always `<WORKSPACES_ROOT>/<ws>/<app>/data/<svc>`; agents choose only the container path.
- Every `$` in agent-supplied strings and rendered template label values is written as `$$`.
- Agent input never reaches a shell; compose is executed with fixed argv and a scrubbed environment.
- Images: any registry, explicit tag or digest required, tag `latest` rejected.
- `go test ./...` passes without Docker. Tests needing Docker use the build tag `integration`.
- Every service result returned to MCP uses non-nil slices (MCP output-schema validation rejects `null` arrays).

- Frontend dependencies are pinned exactly in `web/package.json` (Task 22) and locked by `web/package-lock.json`; Node is used only to build (`internal/webui/dist` is embedded into the binary).
- `go test ./...` must pass without Node: `internal/webui/dist/.keep` is committed and the admin server answers "admin UI not built" until a frontend build exists.
- Admin API JSON shapes (Go `internal/admin/dto.go`, `templates.go`, `misc.go`) and `web/src/types.ts` must stay in sync.

Earlier simplifications (CSRF via `http.CrossOriginProtection`, env-only `DEPLOY_CONCURRENCY`, audit folded into `service`/`store`, `down -v`, extra template guards, startup network warnings, extra `apps` columns) are now part of the spec.

## Review Focus

1. **Redeploy with a service removed** — the removed service's port claim must be released and its quota no longer counted. Test: `TestDeployRedeployReleasesRemovedServicePorts` (Task 15).
2. **`compose up` fails** — the app lock must be released so an immediate retry is not `conflict`, and `docker-compose.prev.yml` must hold the last *successful* render, not the failed one. Test: `TestDeployFailureReleasesLockAndKeepsLastGoodPrev` (Task 15).
3. **Template ungranted or edited after a deploy** — `list` must still work and still show the URL recorded at deploy time; a redeploy must fail with `validation_failed` naming the template. Test: `TestListSurvivesTemplateRevocation` (Task 16).
4. **Agent targets a service or app it does not own** (another workspace's app with the same name, or a service not in its app) — must get `not_found` and no Docker call may touch the other workspace's containers. Test: `TestCrossWorkspaceAccessIsNotFound` (Task 16).
5. **Env value containing `$` or `${HOME}`** — must arrive in the container literally. Tests: `TestRenderEscapesDollar` (Task 5) and the `printenv` assertion in `TestIntegrationDeployLifecycle` (Task 26).

---

## File Structure

```
go.mod, go.sum, .gitignore, .dockerignore, Dockerfile, README.md
cmd/deploy-gateway/main.go            serve + hash-password subcommands, wiring, startup checks
cmd/deploy-gateway/main_test.go
deploy/docker-compose.yml             example stack for the host
deploy/.env.example
internal/names/names.go               naming rules, label keys
internal/stack/types.go               agent stack types
internal/stack/errors.go              FieldError / Errors
internal/stack/decode.go              strict JSON decode
internal/stack/validate.go            field validation
internal/stack/resources.go           memory parsing, defaults, totals
internal/tmpl/tmpl.go                 exposure template model, validation, rendering
internal/render/compose.go            typed compose structs
internal/render/render.go             stack → hardened compose YAML
internal/verify/verify.go             independent post-render verifier
internal/config/config.go             env config
internal/store/schema.sql             SQLite schema
internal/store/store.go               Open, errors, helpers
internal/store/workspaces.go          workspaces + quotas + defaults
internal/store/tokens.go
internal/store/templates.go           templates + grants
internal/store/apps.go                apps + port claims + usage
internal/store/audit.go               audit + settings
internal/auth/token.go                token generation/hash
internal/auth/password.go             argon2id
internal/auth/limiter.go              failure rate limiter
internal/auth/clientip.go             trusted-proxy aware client IP
internal/auth/sessions.go             admin sessions
internal/runner/runner.go             docker-compose execution
internal/dockerx/client.go            moby client wrapper
internal/dockerx/check.go             runtime HostConfig checks (pure)
internal/service/service.go           Service, interfaces, Principal, paths
internal/service/types.go             tool inputs/outputs
internal/service/errors.go            Error + codes
internal/service/track.go             audit tracking + redaction + sanitizing
internal/service/plan.go              validate+quota+render+verify pipeline
internal/service/deploy.go            Validate, Deploy
internal/service/read.go              Whoami, ListTemplates, List, Logs
internal/service/lifecycle.go         Restart, Stop, Delete
internal/service/admin.go             admin operations
internal/service/disk.go              disk usage cache
internal/service/servicetest/fakes.go FakeCompose, FakeDocker for tests in other packages
internal/agentauth/agentauth.go       bearer token authentication (API + MCP)
internal/api/api.go                   JSON HTTP API
internal/mcpserver/mcpserver.go       MCP tools + streamable HTTP handler
internal/admin/server.go              admin API routing, password/proxy auth, SPA serving, JSON helpers
internal/admin/dto.go                 admin JSON shapes (mirrored in web/src/types.ts)
internal/admin/workspaces.go          dashboard, workspaces, grants, tokens, apps
internal/admin/templates.go           templates + live preview
internal/admin/misc.go                audit, settings, orphans
internal/webui/embed.go               go:embed of the built React app (dist/.keep committed)
web/                                  React admin app (Vite + TypeScript + Mantine)
web/src/api.ts, types.ts              typed admin API client
web/src/App.tsx, main.tsx, theme.ts   routing, auth gate, providers
web/src/components/                   Layout, ErrorAlert, AuditTable, QuotaFields, PortFields, ConfirmDelete
web/src/pages/                        Login, Dashboard, Workspaces, WorkspaceDetail, Templates, TemplateEditor, Audit, Settings, Orphans
web/src/test/                         Vitest setup and helpers
```

---

### Task 1: Repository scaffold and naming rules

**Files:**
- Create: `go.mod`, `.gitignore`, `internal/names/names.go`
- Test: `internal/names/names_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `names.ValidWorkspace(string) bool`, `ValidApp`, `ValidService`, `ValidTemplate`
  - `names.Container(ws, app, svc string) string` → `ws-app-svc`
  - `names.Project(ws, app string) string`, `names.Network(ws, app string) string` → `dg-ws-app`
  - `names.Parse(name string) (ws, app, svc string, ok bool)`
  - consts `LabelWorkspace`, `LabelApp`, `LabelService`, `LabelManaged`

- [ ] **Step 1: Initialise the repository and module**

```bash
cd <repo>
git init
go mod init github.com/sebmahr/deploy-gateway
go mod edit -go=1.27
cat > .gitignore <<'EOF'
/deploy-gateway
/data/
/workspaces/
.env
*.db
*.db-wal
*.db-shm
EOF
git add CLAUDE.md docs .gitignore go.mod
git commit -m "chore: initial spec, plan and module"
```

- [ ] **Step 2: Write the failing test**

`internal/names/names_test.go`:

```go
package names

import (
	"strings"
	"testing"
)

func TestValidators(t *testing.T) {
	cases := []struct {
		kind string
		fn   func(string) bool
		in   string
		want bool
	}{
		{"ws", ValidWorkspace, "alice", true},
		{"ws", ValidWorkspace, "a1", true},
		{"ws", ValidWorkspace, "a", false},
		{"ws", ValidWorkspace, "al-ice", false},
		{"ws", ValidWorkspace, "Alice", false},
		{"ws", ValidWorkspace, "abcdefghijklmnop", true},
		{"ws", ValidWorkspace, "abcdefghijklmnopq", false},
		{"app", ValidApp, "shop", true},
		{"app", ValidApp, "my-shop", true},
		{"app", ValidApp, "s", false},
		{"app", ValidApp, "shop-", false},
		{"app", ValidApp, "-shop", false},
		{"app", ValidApp, "abcdefghijklmnopqrstuvwx", true},
		{"app", ValidApp, "abcdefghijklmnopqrstuvwxy", false},
		{"svc", ValidService, "web", true},
		{"svc", ValidService, "w", true},
		{"svc", ValidService, "my-web", false},
		{"svc", ValidService, "abcdefghijklmnopq", false},
		{"tpl", ValidTemplate, "public-web", true},
		{"tpl", ValidTemplate, "p", false},
	}
	for _, c := range cases {
		if got := c.fn(c.in); got != c.want {
			t.Errorf("%s %q: got %v want %v", c.kind, c.in, got, c.want)
		}
	}
}

func TestComposedNames(t *testing.T) {
	if got := Container("alice", "my-shop", "db"); got != "alice-my-shop-db" {
		t.Fatalf("Container = %q", got)
	}
	if got := Project("alice", "shop"); got != "dg-alice-shop" {
		t.Fatalf("Project = %q", got)
	}
	if got := Network("alice", "shop"); got != "dg-alice-shop" {
		t.Fatalf("Network = %q", got)
	}
	longest := Container(strings.Repeat("w", 16), strings.Repeat("a", 24), strings.Repeat("s", 16))
	if len(longest) != 58 {
		t.Fatalf("longest name has %d chars, want 58 (< 63 DNS label limit)", len(longest))
	}
}

func TestParseRoundTrip(t *testing.T) {
	ws, app, svc, ok := Parse("alice-my-shop-db")
	if !ok || ws != "alice" || app != "my-shop" || svc != "db" {
		t.Fatalf("Parse = %q %q %q %v", ws, app, svc, ok)
	}
	for _, bad := range []string{"alice", "alice-db", "Alice-shop-db", "alice-shop-my-db-", "alice--db"} {
		if _, _, _, ok := Parse(bad); ok {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}

func TestComposedNamesNeverCollide(t *testing.T) {
	wss := []string{"ab", "abc", "a1"}
	apps := []string{"x1", "x1-ab", "ab-x1", "x1-x1", "ab"}
	svcs := []string{"ab", "x1", "abc"}
	seen := map[string]string{}
	for _, w := range wss {
		for _, a := range apps {
			for _, s := range svcs {
				n := Container(w, a, s)
				triple := w + "|" + a + "|" + s
				if prev, dup := seen[n]; dup {
					t.Fatalf("collision: %s and %s both produce %s", prev, triple, n)
				}
				seen[n] = triple
			}
		}
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/names/`
Expected: FAIL — `undefined: ValidWorkspace` (and others).

- [ ] **Step 4: Write the implementation**

`internal/names/names.go`:

```go
// Package names defines the naming rules that make every gateway-created
// Docker object name unique and unambiguous (spec §5). Workspace and service
// names contain no hyphens, so <ws>-<app>-<svc> always parses back uniquely.
package names

import (
	"regexp"
	"strings"
)

const (
	LabelWorkspace = "deploy-gateway.workspace"
	LabelApp       = "deploy-gateway.app"
	LabelService   = "deploy-gateway.service"
	LabelManaged   = "deploy-gateway.managed"
	LabelPrefix    = "deploy-gateway."
)

var (
	workspaceRe = regexp.MustCompile(`^[a-z][a-z0-9]{1,15}$`)
	appRe       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,22}[a-z0-9]$`)
	serviceRe   = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)
	templateRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
)

func ValidWorkspace(s string) bool { return workspaceRe.MatchString(s) }
func ValidApp(s string) bool       { return appRe.MatchString(s) }
func ValidService(s string) bool   { return serviceRe.MatchString(s) }
func ValidTemplate(s string) bool  { return templateRe.MatchString(s) }

// Container is the container name, compose service key and DNS hostname.
func Container(ws, app, svc string) string { return ws + "-" + app + "-" + svc }

// Project is the compose project name.
func Project(ws, app string) string { return "dg-" + ws + "-" + app }

// Network is the per-app private network.
func Network(ws, app string) string { return "dg-" + ws + "-" + app }

// Parse splits a container name back into its parts.
func Parse(name string) (ws, app, svc string, ok bool) {
	first := strings.IndexByte(name, '-')
	last := strings.LastIndexByte(name, '-')
	if first < 0 || last <= first {
		return "", "", "", false
	}
	ws, app, svc = name[:first], name[first+1:last], name[last+1:]
	if !ValidWorkspace(ws) || !ValidApp(app) || !ValidService(svc) {
		return "", "", "", false
	}
	return ws, app, svc, true
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/names/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/names
git commit -m "feat(names): naming rules for workspaces, apps, services"
```

---

### Task 2: Agent stack types and strict decoding

**Files:**
- Create: `internal/stack/types.go`, `internal/stack/errors.go`, `internal/stack/decode.go`
- Test: `internal/stack/decode_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `stack.Stack{Services map[string]Service}` and `Service`, `Expose`, `Port`, `Volume`, `Healthcheck`, `Resources` (fields below)
  - `stack.FieldError{Field, Message string}`, `stack.Errors []FieldError` (implements `error`)
  - `stack.Decode(raw []byte) (*Stack, error)`; `stack.MaxRequestBytes = 128 << 10`
  - `stack.ServiceNames(s *Stack) []string` (sorted)

- [ ] **Step 1: Write the failing test**

`internal/stack/decode_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/stack/`
Expected: FAIL — `undefined: Decode`.

- [ ] **Step 3: Write the implementation**

`internal/stack/types.go`:

```go
// Package stack defines the agent-supplied stack format: a strict, JSON-only
// subset of compose (spec §7). Nothing in here is ever passed to Docker
// directly; the render package turns a validated Stack into compose YAML.
package stack

import (
	"maps"
	"slices"
)

type Stack struct {
	Services map[string]Service `json:"services" jsonschema:"services of this app keyed by short service name (^[a-z][a-z0-9]{0,15}$)"`
}

type Service struct {
	Image          string            `json:"image" jsonschema:"image reference with explicit tag or digest; :latest is rejected"`
	Command        []string          `json:"command,omitempty" jsonschema:"exec-form command"`
	Entrypoint     []string          `json:"entrypoint,omitempty" jsonschema:"exec-form entrypoint"`
	Environment    map[string]string `json:"environment,omitempty" jsonschema:"environment variables; values are passed literally (no interpolation)"`
	Expose         *Expose           `json:"expose,omitempty" jsonschema:"make this service reachable through an admin-defined exposure template"`
	Ports          []Port            `json:"ports,omitempty" jsonschema:"publish container ports on host ports inside the workspace port range"`
	Volume         *Volume           `json:"volume,omitempty" jsonschema:"persistent directory inside the workspace folder, mounted at path"`
	Healthcheck    *Healthcheck      `json:"healthcheck,omitempty"`
	DependsOn      []string          `json:"depends_on,omitempty" jsonschema:"short service names in this stack to start first"`
	Resources      *Resources        `json:"resources,omitempty"`
	User           string            `json:"user,omitempty" jsonschema:"numeric uid:gid, e.g. 999:999; required for images whose entrypoint drops privileges"`
	WritableRootfs bool              `json:"writable_rootfs,omitempty" jsonschema:"disable the read-only root filesystem"`
}

type Expose struct {
	Template string `json:"template" jsonschema:"name of a granted exposure template (see list_templates)"`
	Port     int    `json:"port" jsonschema:"container port the template routes to"`
}

type Port struct {
	Container int    `json:"container"`
	Host      int    `json:"host"`
	Protocol  string `json:"protocol,omitempty" jsonschema:"tcp (default) or udp"`
}

type Volume struct {
	Path string `json:"path" jsonschema:"absolute container path"`
}

type Healthcheck struct {
	Command  []string `json:"command" jsonschema:"exec-form command run inside the container"`
	Interval string   `json:"interval,omitempty" jsonschema:"Go duration 5s..5m, default 30s"`
	Retries  int      `json:"retries,omitempty" jsonschema:"1..10, default 3"`
}

type Resources struct {
	Memory string  `json:"memory,omitempty" jsonschema:"e.g. 256m or 1g"`
	CPUs   float64 `json:"cpus,omitempty"`
	Pids   int64   `json:"pids,omitempty"`
}

// ServiceNames returns the service names in sorted order so that rendering,
// validation messages and results are deterministic.
func ServiceNames(s *Stack) []string {
	return slices.Sorted(maps.Keys(s.Services))
}
```

`internal/stack/errors.go`:

```go
package stack

import (
	"fmt"
	"strings"
)

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Errors collects every validation problem, not just the first.
type Errors []FieldError

func (e Errors) Error() string {
	parts := make([]string, len(e))
	for i, fe := range e {
		if fe.Field == "" {
			parts[i] = fe.Message
		} else {
			parts[i] = fe.Field + ": " + fe.Message
		}
	}
	return strings.Join(parts, "; ")
}

func (e *Errors) add(field, format string, args ...any) {
	*e = append(*e, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}
```

`internal/stack/decode.go`:

```go
package stack

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const MaxRequestBytes = 128 << 10

// Decode parses raw JSON into a Stack, rejecting unknown fields at every
// level, trailing data and oversize payloads.
func Decode(raw []byte) (*Stack, error) {
	if len(raw) > MaxRequestBytes {
		return nil, Errors{{Message: fmt.Sprintf("stack exceeds %d bytes", MaxRequestBytes)}}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var s Stack
	if err := dec.Decode(&s); err != nil {
		return nil, Errors{{Message: "invalid stack JSON: " + err.Error()}}
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, Errors{{Message: "invalid stack JSON: trailing data after object"}}
	}
	return &s, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/stack/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/stack
git commit -m "feat(stack): agent stack types with strict JSON decoding"
```

---

### Task 3: Stack validation, resources and quotas math

**Files:**
- Create: `internal/stack/validate.go`, `internal/stack/resources.go`
- Test: `internal/stack/validate_test.go`

**Interfaces:**
- Consumes: `names.ValidService` (Task 1); `Stack`, `Errors` (Task 2)
- Produces:
  - `stack.Limits{MaxServices int; MaxMemoryBytes int64; MaxCPUs float64; MaxPids int64; PortRangeStart, PortRangeEnd int}` (range 0/0 = publishing disabled)
  - `stack.Grants map[string]bool` — granted template name → shareable (may be used by more than one service in a stack)
  - `stack.Validate(s *Stack, lim Limits, grants Grants) Errors` (nil when valid)
  - `stack.ParseMemory(string) (int64, error)`
  - `stack.Defaults{MemoryBytes int64; CPUs float64; Pids int64}`, `stack.Resolved{MemoryBytes int64; CPUs float64; Pids int64}`
  - `stack.Effective(svc Service, d Defaults) Resolved`, `stack.Totals(s *Stack, d Defaults) (mem int64, cpus float64)`
  - `stack.Protocol(p Port) string` (defaults to `tcp`)

- [ ] **Step 1: Write the failing test**

`internal/stack/validate_test.go`:

```go
package stack

import (
	"strings"
	"testing"
)

func testLimits() Limits {
	return Limits{MaxServices: 4, MaxMemoryBytes: 1 << 30, MaxCPUs: 1, MaxPids: 1024, PortRangeStart: 20000, PortRangeEnd: 20099}
}

func testGrants() Grants { return Grants{"public-web": false, "internal": true} }

func validStack(t *testing.T) *Stack {
	t.Helper()
	s, err := Decode([]byte(exampleJSON))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func edit(s *Stack, name string, f func(*Service)) {
	v := s.Services[name]
	f(&v)
	s.Services[name] = v
}

func TestValidateAcceptsExample(t *testing.T) {
	if errs := Validate(validStack(t), testLimits(), testGrants()); errs != nil {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestValidateAcceptsDigestAndShareableTemplate(t *testing.T) {
	s := validStack(t)
	edit(s, "db", func(v *Service) {
		v.Image = "postgres@sha256:" + strings.Repeat("a", 64)
		v.Expose = &Expose{Template: "internal", Port: 5432}
	})
	edit(s, "web", func(v *Service) { v.Expose = &Expose{Template: "internal", Port: 8080} })
	if errs := Validate(s, testLimits(), testGrants()); errs != nil {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *Stack, l *Limits)
		want   string
	}{
		{"no services", func(s *Stack, _ *Limits) { s.Services = nil }, "at least one service"},
		{"too many services", func(s *Stack, _ *Limits) {
			for _, n := range []string{"a", "b", "c"} {
				s.Services[n] = Service{Image: "a:1"}
			}
		}, "at most 4"},
		{"bad service name", func(s *Stack, _ *Limits) { s.Services["my-db"] = Service{Image: "a:1"} }, "services.my-db"},
		{"missing image", func(s *Stack, _ *Limits) { edit(s, "web", func(v *Service) { v.Image = "" }) }, "services.web.image"},
		{"latest tag", func(s *Stack, _ *Limits) { edit(s, "web", func(v *Service) { v.Image = "nginx:latest" }) }, `"latest"`},
		{"no tag", func(s *Stack, _ *Limits) { edit(s, "web", func(v *Service) { v.Image = "nginx" }) }, "explicit tag"},
		{"bad image", func(s *Stack, _ *Limits) { edit(s, "web", func(v *Service) { v.Image = "ghcr.io/UPPER:1" }) }, "invalid image"},
		{"env bad key", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Environment["BAD-KEY"] = "x" })
		}, "services.web.environment.BAD-KEY"},
		{"env NUL", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Environment["A"] = "a\x00b" })
		}, "NUL"},
		{"env value too large", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Environment["A"] = strings.Repeat("x", 9000) })
		}, "8192"},
		{"command too many items", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Command = make([]string, 33) })
		}, "services.web.command"},
		{"expose not granted", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Expose.Template = "nope" })
		}, "not granted"},
		{"expose port", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Expose.Port = 0 })
		}, "services.web.expose.port"},
		{"non-shareable template twice", func(s *Stack, _ *Limits) {
			edit(s, "db", func(v *Service) { v.Expose = &Expose{Template: "public-web", Port: 5432} })
		}, "only one service"},
		{"port outside range", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Ports[0].Host = 30000 })
		}, "20000-20099"},
		{"ports without range", func(s *Stack, l *Limits) { l.PortRangeStart, l.PortRangeEnd = 0, 0 }, "may not publish"},
		{"duplicate host port", func(s *Stack, _ *Limits) {
			edit(s, "db", func(v *Service) { v.Ports = []Port{{Container: 5432, Host: 20001}} })
		}, "already used"},
		{"bad protocol", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Ports[0].Protocol = "sctp" })
		}, "tcp or udp"},
		{"volume relative", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.Volume.Path = "data" }) }, "services.db.volume.path"},
		{"volume traversal", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.Volume.Path = "/data/../etc" }) }, "normalized"},
		{"volume proc", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.Volume.Path = "/proc/x" }) }, "/proc"},
		{"volume tmp", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.Volume.Path = "/tmp" }) }, "/tmp"},
		{"volume root", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.Volume.Path = "/" }) }, "must not be /"},
		{"volume dollar", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.Volume.Path = "/data$HOME" }) }, "services.db.volume.path"},
		{"healthcheck interval", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Healthcheck.Interval = "1s" })
		}, "between 5s and 5m"},
		{"healthcheck no command", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Healthcheck.Command = nil })
		}, "services.web.healthcheck.command"},
		{"depends unknown", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.DependsOn = []string{"cache"} })
		}, "unknown service"},
		{"depends self", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.DependsOn = []string{"web"} })
		}, "itself"},
		{"depends cycle", func(s *Stack, _ *Limits) {
			edit(s, "db", func(v *Service) { v.DependsOn = []string{"web"} })
		}, "cycle"},
		{"memory over max", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Resources.Memory = "2g" })
		}, "services.web.resources.memory"},
		{"memory format", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Resources.Memory = "256mb" })
		}, "like 256m"},
		{"cpus over max", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Resources.CPUs = 2 })
		}, "services.web.resources.cpus"},
		{"pids too low", func(s *Stack, _ *Limits) {
			edit(s, "web", func(v *Service) { v.Resources.Pids = 5 })
		}, "services.web.resources.pids"},
		{"user non numeric", func(s *Stack, _ *Limits) { edit(s, "db", func(v *Service) { v.User = "postgres" }) }, "uid:gid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, lim := validStack(t), testLimits()
			c.mutate(s, &lim)
			errs := Validate(s, lim, testGrants())
			if errs == nil || !strings.Contains(errs.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, errs)
			}
		})
	}
}

func TestParseMemory(t *testing.T) {
	cases := map[string]int64{"16m": 16 << 20, "1g": 1 << 30, "512k": 512 << 10, "256M": 256 << 20}
	for in, want := range cases {
		got, err := ParseMemory(in)
		if err != nil || got != want {
			t.Errorf("ParseMemory(%q) = %d, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0m", "256mb", "-1m", "1t", "1.5g"} {
		if _, err := ParseMemory(bad); err == nil {
			t.Errorf("ParseMemory(%q) should fail", bad)
		}
	}
}

func TestEffectiveAndTotals(t *testing.T) {
	s := validStack(t)
	d := Defaults{MemoryBytes: 128 << 20, CPUs: 0.25, Pids: 256}
	web := Effective(s.Services["web"], d)
	if web.MemoryBytes != 256<<20 || web.CPUs != 0.5 || web.Pids != 256 {
		t.Fatalf("web = %+v", web)
	}
	mem, cpus := Totals(s, d)
	if mem != (256+128)<<20 || cpus != 0.75 {
		t.Fatalf("totals = %d %v", mem, cpus)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/stack/`
Expected: FAIL — `undefined: Limits`, `undefined: Validate`, ...

- [ ] **Step 3: Write `resources.go`**

`internal/stack/resources.go`:

```go
package stack

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var memRe = regexp.MustCompile(`^([1-9][0-9]{0,6})([kmg])$`)

// ParseMemory parses sizes like 512k, 256m, 1g (case-insensitive).
func ParseMemory(s string) (int64, error) {
	m := memRe.FindStringSubmatch(strings.ToLower(s))
	if m == nil {
		return 0, fmt.Errorf("memory must look like 256m or 1g")
	}
	n, _ := strconv.ParseInt(m[1], 10, 64)
	switch m[2] {
	case "k":
		return n << 10, nil
	case "m":
		return n << 20, nil
	default:
		return n << 30, nil
	}
}

type Defaults struct {
	MemoryBytes int64
	CPUs        float64
	Pids        int64
}

type Resolved struct {
	MemoryBytes int64
	CPUs        float64
	Pids        int64
}

// Effective resolves a validated service's limits, falling back to defaults.
func Effective(svc Service, d Defaults) Resolved {
	r := Resolved{MemoryBytes: d.MemoryBytes, CPUs: d.CPUs, Pids: d.Pids}
	if svc.Resources != nil {
		if b, err := ParseMemory(svc.Resources.Memory); err == nil {
			r.MemoryBytes = b
		}
		if svc.Resources.CPUs > 0 {
			r.CPUs = svc.Resources.CPUs
		}
		if svc.Resources.Pids > 0 {
			r.Pids = svc.Resources.Pids
		}
	}
	return r
}

// Totals sums effective memory and CPUs over all services.
func Totals(s *Stack, d Defaults) (mem int64, cpus float64) {
	for _, svc := range s.Services {
		r := Effective(svc, d)
		mem += r.MemoryBytes
		cpus += r.CPUs
	}
	return mem, cpus
}

// Protocol returns the port protocol with the tcp default applied.
func Protocol(p Port) string {
	if p.Protocol == "" {
		return "tcp"
	}
	return p.Protocol
}
```

- [ ] **Step 4: Write `validate.go`**

`internal/stack/validate.go`:

```go
package stack

import (
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/names"
)

const (
	maxListItems   = 32
	maxArgBytes    = 4 << 10
	maxEnvEntries  = 100
	maxEnvValue    = 8 << 10
	maxEnvTotal    = 64 << 10
	maxPorts       = 10
	minMemoryBytes = 16 << 20
	minPids        = 16
)

var (
	envKeyRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	userRe    = regexp.MustCompile(`^[0-9]{1,10}:[0-9]{1,10}$`)
	volPathRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)
	imageRe   = regexp.MustCompile(`^(?:([a-zA-Z0-9][a-zA-Z0-9.-]*(?::[0-9]{1,5})?)/)?` +
		`([a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*)` +
		`(?::([A-Za-z0-9_][A-Za-z0-9_.-]{0,127}))?(?:@(sha256:[a-f0-9]{64}))?$`)
	forbiddenVolumePrefixes = []string{"/proc", "/sys", "/dev", "/tmp"}
)

// Limits are the per-workspace bounds a stack is validated against.
// PortRangeStart == 0 means the workspace may not publish ports.
type Limits struct {
	MaxServices    int
	MaxMemoryBytes int64
	MaxCPUs        float64
	MaxPids        int64
	PortRangeStart int
	PortRangeEnd   int
}

// Grants maps granted template names to whether more than one service of a
// stack may use them (true when the host pattern contains {{service}} or the
// template has no host pattern).
type Grants map[string]bool

// Validate checks every field of the stack and returns all problems found.
func Validate(s *Stack, lim Limits, grants Grants) Errors {
	var errs Errors
	if s == nil || len(s.Services) == 0 {
		errs.add("services", "at least one service is required")
		return errs
	}
	if len(s.Services) > lim.MaxServices {
		errs.add("services", "at most %d services allowed", lim.MaxServices)
	}
	hostPorts := map[string]string{}
	templateUse := map[string]int{}
	for _, name := range ServiceNames(s) {
		svc := s.Services[name]
		f := "services." + name
		if !names.ValidService(name) {
			errs.add(f, "service name must match ^[a-z][a-z0-9]{0,15}$")
			continue
		}
		validateImage(&errs, f+".image", svc.Image)
		validateArgs(&errs, f+".command", svc.Command)
		validateArgs(&errs, f+".entrypoint", svc.Entrypoint)
		validateEnv(&errs, f+".environment", svc.Environment)
		if e := svc.Expose; e != nil {
			if _, ok := grants[e.Template]; !ok {
				errs.add(f+".expose.template", "template %q is not granted to this workspace", e.Template)
			} else {
				templateUse[e.Template]++
			}
			if e.Port < 1 || e.Port > 65535 {
				errs.add(f+".expose.port", "must be 1-65535")
			}
		}
		validatePorts(&errs, f+".ports", svc.Ports, lim, hostPorts)
		if svc.Volume != nil {
			validateVolume(&errs, f+".volume.path", svc.Volume.Path)
		}
		if hc := svc.Healthcheck; hc != nil {
			if len(hc.Command) == 0 {
				errs.add(f+".healthcheck.command", "is required")
			}
			validateArgs(&errs, f+".healthcheck.command", hc.Command)
			if hc.Interval != "" {
				d, err := time.ParseDuration(hc.Interval)
				if err != nil || d < 5*time.Second || d > 5*time.Minute {
					errs.add(f+".healthcheck.interval", "must be a duration between 5s and 5m")
				}
			}
			if hc.Retries < 0 || hc.Retries > 10 {
				errs.add(f+".healthcheck.retries", "must be 1-10")
			}
		}
		for i, d := range svc.DependsOn {
			df := fmt.Sprintf("%s.depends_on[%d]", f, i)
			if d == name {
				errs.add(df, "a service cannot depend on itself")
			} else if _, ok := s.Services[d]; !ok {
				errs.add(df, "unknown service %q", d)
			}
		}
		if svc.Resources != nil {
			validateResources(&errs, f+".resources", *svc.Resources, lim)
		}
		if svc.User != "" && !userRe.MatchString(svc.User) {
			errs.add(f+".user", "must be numeric uid:gid, e.g. 999:999")
		}
	}
	for _, t := range slices.Sorted(maps.Keys(templateUse)) {
		if templateUse[t] > 1 && !grants[t] {
			errs.add("services", "template %q has no {{service}} in its host pattern; only one service per stack may use it", t)
		}
	}
	if c := findCycle(s); c != "" {
		errs.add("services", "depends_on cycle: %s", c)
	}
	return errs
}

func validateImage(errs *Errors, f, img string) {
	if img == "" {
		errs.add(f, "image is required")
		return
	}
	if len(img) > 255 {
		errs.add(f, "image reference too long")
		return
	}
	m := imageRe.FindStringSubmatch(img)
	if m == nil {
		errs.add(f, "invalid image reference")
		return
	}
	tag, digest := m[3], m[4]
	if tag == "" && digest == "" {
		errs.add(f, "image must have an explicit tag or digest (e.g. nginx:1.27.0)")
		return
	}
	if tag == "latest" {
		errs.add(f, `tag "latest" is not allowed; pin a version`)
	}
}

func validateArgs(errs *Errors, f string, args []string) {
	if len(args) > maxListItems {
		errs.add(f, "at most %d items allowed", maxListItems)
		return
	}
	for i, a := range args {
		if len(a) > maxArgBytes {
			errs.add(fmt.Sprintf("%s[%d]", f, i), "longer than %d bytes", maxArgBytes)
		}
		if strings.ContainsRune(a, 0) {
			errs.add(fmt.Sprintf("%s[%d]", f, i), "contains NUL byte")
		}
	}
}

func validateEnv(errs *Errors, f string, env map[string]string) {
	if len(env) > maxEnvEntries {
		errs.add(f, "at most %d variables allowed", maxEnvEntries)
	}
	total := 0
	for _, k := range slices.Sorted(maps.Keys(env)) {
		v := env[k]
		kf := f + "." + k
		total += len(k) + len(v)
		if !envKeyRe.MatchString(k) {
			errs.add(kf, "key must match ^[A-Za-z_][A-Za-z0-9_]{0,63}$")
		}
		if len(v) > maxEnvValue {
			errs.add(kf, "value longer than %d bytes", maxEnvValue)
		}
		if strings.ContainsRune(v, 0) {
			errs.add(kf, "value contains NUL byte")
		}
	}
	if total > maxEnvTotal {
		errs.add(f, "environment larger than %d bytes", maxEnvTotal)
	}
}

func validatePorts(errs *Errors, f string, ports []Port, lim Limits, seen map[string]string) {
	if len(ports) == 0 {
		return
	}
	if lim.PortRangeStart == 0 {
		errs.add(f, "this workspace may not publish ports")
		return
	}
	if len(ports) > maxPorts {
		errs.add(f, "at most %d ports per service", maxPorts)
	}
	for i, p := range ports {
		pf := fmt.Sprintf("%s[%d]", f, i)
		if p.Container < 1 || p.Container > 65535 {
			errs.add(pf+".container", "must be 1-65535")
		}
		if p.Host < lim.PortRangeStart || p.Host > lim.PortRangeEnd {
			errs.add(pf+".host", "must be within %d-%d", lim.PortRangeStart, lim.PortRangeEnd)
		}
		proto := Protocol(p)
		if proto != "tcp" && proto != "udp" {
			errs.add(pf+".protocol", "must be tcp or udp")
			continue
		}
		key := fmt.Sprintf("%s/%d", proto, p.Host)
		if prev, dup := seen[key]; dup {
			errs.add(pf+".host", "host port %s already used by %s", key, prev)
		} else {
			seen[key] = pf
		}
	}
}

func validateVolume(errs *Errors, f, p string) {
	switch {
	case !volPathRe.MatchString(p):
		errs.add(f, "must be an absolute path of letters, digits, . _ - /")
	case path.Clean(p) != p:
		errs.add(f, "must be normalized (no .., //, or trailing /)")
	case p == "/":
		errs.add(f, "must not be /")
	default:
		for _, pre := range forbiddenVolumePrefixes {
			if p == pre || strings.HasPrefix(p, pre+"/") {
				errs.add(f, "must not be under %s", pre)
			}
		}
	}
}

func validateResources(errs *Errors, f string, r Resources, lim Limits) {
	if r.Memory != "" {
		b, err := ParseMemory(r.Memory)
		switch {
		case err != nil:
			errs.add(f+".memory", "%v", err)
		case b < minMemoryBytes:
			errs.add(f+".memory", "must be at least 16m")
		case b > lim.MaxMemoryBytes:
			errs.add(f+".memory", "exceeds per-service maximum of %dm", lim.MaxMemoryBytes>>20)
		}
	}
	if r.CPUs != 0 && (r.CPUs < 0.01 || r.CPUs > lim.MaxCPUs) {
		errs.add(f+".cpus", "must be between 0.01 and %g", lim.MaxCPUs)
	}
	if r.Pids != 0 && (r.Pids < minPids || r.Pids > lim.MaxPids) {
		errs.add(f+".pids", "must be between %d and %d", minPids, lim.MaxPids)
	}
}

// findCycle returns a human-readable cycle like "a -> b -> a", or "".
func findCycle(s *Stack) string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stackPath []string
	var visit func(n string) string
	visit = func(n string) string {
		color[n] = grey
		stackPath = append(stackPath, n)
		for _, d := range s.Services[n].DependsOn {
			if _, ok := s.Services[d]; !ok || d == n {
				continue
			}
			switch color[d] {
			case grey:
				i := slices.Index(stackPath, d)
				return strings.Join(append(slices.Clone(stackPath[i:]), d), " -> ")
			case white:
				if c := visit(d); c != "" {
					return c
				}
			}
		}
		stackPath = stackPath[:len(stackPath)-1]
		color[n] = black
		return ""
	}
	for _, n := range ServiceNames(s) {
		if color[n] == white {
			if c := visit(n); c != "" {
				return c
			}
		}
	}
	return ""
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/stack/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/stack
git commit -m "feat(stack): field validation, resource defaults and totals"
```

---

### Task 4: Exposure templates

**Files:**
- Create: `internal/tmpl/tmpl.go`
- Test: `internal/tmpl/tmpl_test.go`

**Interfaces:**
- Consumes: `names.ValidTemplate`, `names.Container`, `names.LabelPrefix` (Task 1)
- Produces:
  - `tmpl.Template{Name, Description, HostPattern string; Networks []string; Labels map[string]string}` (JSON tags `name`, `description`, `host_pattern`, `networks`, `labels`)
  - `tmpl.Vars{Workspace, App, Service string; Port int}`
  - `tmpl.Validate(t Template, others []Template) error` (returns `tmpl.ValidationError []string`); `others` may contain `t` itself (matched by `Name`, skipped)
  - `(Template).Host(Vars) string` ("" when no host pattern)
  - `(Template).RenderLabels(Vars) map[string]string`
  - `(Template).Shareable() bool`
  - `tmpl.HostSuffix(pattern string) string`

- [ ] **Step 1: Write the failing test**

`internal/tmpl/tmpl_test.go`:

```go
package tmpl

import (
	"strings"
	"testing"
)

func publicWeb() Template {
	return Template{
		Name:        "public-web",
		Description: "Public HTTPS via Traefik",
		HostPattern: "{{app}}.{{workspace}}.apps.example.com",
		Networks:    []string{"apps"},
		Labels: map[string]string{
			"traefik.enable":                                        "true",
			"traefik.docker.network":                                "apps",
			"traefik.http.routers.{{id}}.rule":                      "Host(`{{host}}`)",
			"traefik.http.routers.{{id}}.entrypoints":               "websecure",
			"traefik.http.services.{{id}}.loadbalancer.server.port": "{{port}}",
		},
	}
}

func TestValidateAcceptsExamples(t *testing.T) {
	ok := []Template{
		publicWeb(),
		{Name: "per-service", HostPattern: "{{service}}-{{app}}-{{workspace}}.svc.example.com", Networks: []string{"apps"}},
		{Name: "internal", Networks: []string{"apps"}, Labels: map[string]string{"traefik.enable": "false"}},
	}
	for _, tpl := range ok {
		if err := Validate(tpl, nil); err != nil {
			t.Errorf("%s: unexpected error %v", tpl.Name, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Template)
		want   string
	}{
		{"bad name", func(t *Template) { t.Name = "Public" }, "name"},
		{"literal in subdomain", func(t *Template) { t.HostPattern = "app-{{app}}.{{workspace}}.example.com" }, "host_pattern must be"},
		{"missing workspace", func(t *Template) { t.HostPattern = "{{app}}.apps.example.com" }, "{{workspace}}"},
		{"duplicate placeholder", func(t *Template) { t.HostPattern = "{{app}}.{{app}}.{{workspace}}.example.com" }, "more than once"},
		{"single label suffix", func(t *Template) { t.HostPattern = "{{app}}.{{workspace}}.localhost" }, "at least two labels"},
		{"uppercase suffix", func(t *Template) { t.HostPattern = "{{app}}.{{workspace}}.Example.com" }, "host_pattern must be"},
		{"host network", func(t *Template) { t.Networks = []string{"host"} }, `"host"`},
		{"reserved dg- network", func(t *Template) { t.Networks = []string{"dg-alice-shop"} }, "dg-"},
		{"owner label", func(t *Template) { t.Labels["deploy-gateway.workspace"] = "x" }, "deploy-gateway."},
		{"label key starts with placeholder", func(t *Template) { t.Labels["{{app}}.x"] = "y" }, "must not start with a placeholder"},
		{"unknown placeholder", func(t *Template) { t.Labels["x"] = "{{secret}}" }, "unknown placeholder"},
		{"malformed placeholder", func(t *Template) { t.Labels["x"] = "{{app" }, "malformed"},
		{"newline in value", func(t *Template) { t.Labels["x"] = "a\nb" }, "newline"},
		{"host without pattern", func(t *Template) { t.HostPattern = "" }, "{{host}}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tpl := publicWeb()
			c.mutate(&tpl)
			err := Validate(tpl, nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestValidateSuffixOverlap(t *testing.T) {
	other := publicWeb() // suffix apps.example.com
	cases := map[string]bool{
		"{{app}}-{{workspace}}.apps.example.com":   true,  // equal
		"{{app}}-{{workspace}}.x.apps.example.com": true,  // nested below
		"{{app}}-{{workspace}}.example.com":        true,  // nested above
		"{{app}}-{{workspace}}.myapps.example.com": false, // not on a label boundary
		"{{app}}-{{workspace}}.apps.example.org":   false,
	}
	for pattern, wantErr := range cases {
		tpl := Template{Name: "second", HostPattern: pattern, Networks: []string{"apps"}}
		err := Validate(tpl, []Template{other})
		if (err != nil) != wantErr {
			t.Errorf("%s: wantErr=%v got %v", pattern, wantErr, err)
		}
	}
	// Editing the same template must not conflict with its own stored version.
	if err := Validate(publicWeb(), []Template{publicWeb()}); err != nil {
		t.Fatalf("self overlap reported: %v", err)
	}
}

func TestRender(t *testing.T) {
	tpl := publicWeb()
	v := Vars{Workspace: "alice", App: "my-shop", Service: "web", Port: 8080}
	if got := tpl.Host(v); got != "my-shop.alice.apps.example.com" {
		t.Fatalf("Host = %q", got)
	}
	labels := tpl.RenderLabels(v)
	if labels["traefik.http.routers.alice-my-shop-web.rule"] != "Host(`my-shop.alice.apps.example.com`)" {
		t.Fatalf("labels = %v", labels)
	}
	if labels["traefik.http.services.alice-my-shop-web.loadbalancer.server.port"] != "8080" {
		t.Fatalf("labels = %v", labels)
	}
	if tpl.Shareable() {
		t.Fatal("public-web has no {{service}}; must not be shareable")
	}
	if !(Template{HostPattern: "{{service}}.{{app}}.{{workspace}}.a.b"}).Shareable() || !(Template{}).Shareable() {
		t.Fatal("expected shareable")
	}
	if HostSuffix(tpl.HostPattern) != "apps.example.com" {
		t.Fatalf("HostSuffix = %q", HostSuffix(tpl.HostPattern))
	}
}

func TestHostsAreUniqueWithinTemplate(t *testing.T) {
	patterns := []string{
		"{{service}}-{{app}}-{{workspace}}.a.example.com",
		"{{workspace}}-{{app}}-{{service}}.a.example.com",
		"{{app}}-{{service}}-{{workspace}}.a.example.com",
		"{{service}}.{{app}}.{{workspace}}.a.example.com",
	}
	wss := []string{"ab", "abc", "a1"}
	apps := []string{"x1", "x1-ab", "ab-x1", "x1-x1", "ab"}
	svcs := []string{"ab", "x1", "abc"}
	for _, p := range patterns {
		tpl := Template{HostPattern: p}
		seen := map[string]string{}
		for _, w := range wss {
			for _, a := range apps {
				for _, s := range svcs {
					h := tpl.Host(Vars{Workspace: w, App: a, Service: s})
					key := w + "|" + a + "|" + s
					if prev, dup := seen[h]; dup {
						t.Fatalf("%s: %s and %s both produce %s", p, prev, key, h)
					}
					seen[h] = key
				}
			}
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tmpl/`
Expected: FAIL — `undefined: Template`.

- [ ] **Step 3: Write the implementation**

`internal/tmpl/tmpl.go`:

```go
// Package tmpl implements admin-defined exposure templates (spec §8):
// networks + labels with placeholders + an optional host pattern whose
// structure guarantees globally unique hostnames.
package tmpl

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sebmahr/deploy-gateway/internal/names"
)

type Template struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	HostPattern string            `json:"host_pattern,omitempty"`
	Networks    []string          `json:"networks"`
	Labels      map[string]string `json:"labels"`
}

type Vars struct {
	Workspace string
	App       string
	Service   string
	Port      int
}

type ValidationError []string

func (v ValidationError) Error() string { return strings.Join(v, "; ") }

var (
	placeholderRe = regexp.MustCompile(`\{\{[^{}]*\}\}`)
	hostRe        = regexp.MustCompile(`^((?:\{\{(?:workspace|app|service)\}\}[-.])*\{\{(?:workspace|app|service)\}\})\.([a-z0-9.-]+)$`)
	dnsLabelRe    = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	networkRe     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
	labelKeyRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/{}-]{0,255}$`)

	labelPlaceholders = map[string]bool{"workspace": true, "app": true, "service": true, "id": true, "host": true, "port": true}
	reservedNetworks  = map[string]bool{"host": true, "none": true, "bridge": true}
)

// Validate checks a template on save. others are the other stored
// templates; an entry with the same Name as t is ignored (edit case).
func Validate(t Template, others []Template) error {
	var errs ValidationError
	add := func(s string) { errs = append(errs, s) }
	if !names.ValidTemplate(t.Name) {
		add("name must match ^[a-z][a-z0-9-]{1,30}$")
	}
	if len(t.Description) > 500 {
		add("description longer than 500 characters")
	}
	if t.HostPattern != "" {
		validateHost(t, add)
		if suffix := HostSuffix(t.HostPattern); suffix != "" {
			for _, o := range others {
				if o.Name == t.Name || o.HostPattern == "" {
					continue
				}
				if os := HostSuffix(o.HostPattern); os != "" && suffixesOverlap(suffix, os) {
					add("host suffix " + strconv.Quote(suffix) + " overlaps suffix " + strconv.Quote(os) + " of template " + strconv.Quote(o.Name))
				}
			}
		}
	}
	for _, n := range t.Networks {
		switch {
		case !networkRe.MatchString(n):
			add("invalid network name " + strconv.Quote(n))
		case reservedNetworks[n]:
			add("network " + strconv.Quote(n) + " is not allowed")
		case strings.HasPrefix(n, "dg-"):
			add("network " + strconv.Quote(n) + ": the dg- prefix is reserved for per-app networks")
		}
	}
	for _, k := range slices.Sorted(maps.Keys(t.Labels)) {
		v := t.Labels[k]
		if strings.HasPrefix(k, "{{") {
			add("label key " + strconv.Quote(k) + " must not start with a placeholder")
		} else if !labelKeyRe.MatchString(k) {
			add("invalid label key " + strconv.Quote(k))
		}
		if strings.HasPrefix(strings.ToLower(k), names.LabelPrefix) {
			add("label key " + strconv.Quote(k) + ": the deploy-gateway. prefix is reserved")
		}
		if len(v) > 4096 {
			add("label " + strconv.Quote(k) + ": value longer than 4096 bytes")
		}
		if strings.ContainsAny(v, "\x00\r\n") {
			add("label " + strconv.Quote(k) + ": value must not contain NUL or newline")
		}
		checkPlaceholders("label "+strconv.Quote(k), k+v, add)
		if strings.Contains(k+v, "{{host}}") && t.HostPattern == "" {
			add("label " + strconv.Quote(k) + " uses {{host}} but the template has no host_pattern")
		}
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

func validateHost(t Template, add func(string)) {
	m := hostRe.FindStringSubmatch(t.HostPattern)
	if m == nil {
		add("host_pattern must be <placeholders separated by - or .>.<lowercase domain>, e.g. {{app}}.{{workspace}}.apps.example.com")
		return
	}
	sub, suffix := m[1], m[2]
	counts := map[string]int{}
	for _, ph := range placeholderRe.FindAllString(sub, -1) {
		counts[ph]++
	}
	for _, ph := range slices.Sorted(maps.Keys(counts)) {
		if counts[ph] > 1 {
			add("host_pattern uses " + ph + " more than once")
		}
	}
	if counts["{{workspace}}"] == 0 || counts["{{app}}"] == 0 {
		add("host_pattern must contain {{workspace}} and {{app}}")
	}
	if !validDomain(suffix) || !strings.Contains(suffix, ".") {
		add("host suffix " + strconv.Quote(suffix) + " must be a valid domain with at least two labels")
		return
	}
	longest := t.Host(Vars{Workspace: strings.Repeat("w", 16), App: strings.Repeat("a", 24), Service: strings.Repeat("s", 16)})
	if len(longest) > 253 || !validDomain(longest) {
		add("longest possible host " + strconv.Quote(longest) + " is not a valid DNS name")
	}
}

func checkPlaceholders(what, s string, add func(string)) {
	for _, ph := range placeholderRe.FindAllString(s, -1) {
		if !labelPlaceholders[strings.Trim(ph, "{}")] {
			add(what + ": unknown placeholder " + ph)
		}
	}
	rest := placeholderRe.ReplaceAllString(s, "")
	if strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
		add(what + ": malformed placeholder")
	}
}

func validDomain(s string) bool {
	if s == "" {
		return false
	}
	for _, l := range strings.Split(s, ".") {
		if !dnsLabelRe.MatchString(l) {
			return false
		}
	}
	return true
}

func suffixesOverlap(a, b string) bool {
	return a == b || strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}

// HostSuffix returns the literal domain part of a host pattern, or "".
func HostSuffix(pattern string) string {
	m := hostRe.FindStringSubmatch(pattern)
	if m == nil {
		return ""
	}
	return m[2]
}

// Host renders the host pattern; "" if the template has none.
func (t Template) Host(v Vars) string {
	if t.HostPattern == "" {
		return ""
	}
	return strings.NewReplacer("{{workspace}}", v.Workspace, "{{app}}", v.App, "{{service}}", v.Service).Replace(t.HostPattern)
}

// RenderLabels substitutes placeholders in label keys and values.
func (t Template) RenderLabels(v Vars) map[string]string {
	r := strings.NewReplacer(
		"{{workspace}}", v.Workspace,
		"{{app}}", v.App,
		"{{service}}", v.Service,
		"{{id}}", names.Container(v.Workspace, v.App, v.Service),
		"{{host}}", t.Host(v),
		"{{port}}", strconv.Itoa(v.Port),
	)
	out := make(map[string]string, len(t.Labels))
	for k, val := range t.Labels {
		out[r.Replace(k)] = r.Replace(val)
	}
	return out
}

// Shareable reports whether more than one service of a stack may use the
// template without producing duplicate hosts.
func (t Template) Shareable() bool {
	return t.HostPattern == "" || strings.Contains(t.HostPattern, "{{service}}")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tmpl/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tmpl
git commit -m "feat(tmpl): exposure templates with unique-host validation"
```

---
### Task 5: Hardened compose renderer

**Files:**
- Create: `internal/render/compose.go`, `internal/render/render.go`
- Test: `internal/render/render_test.go`, `internal/render/testdata/basic.golden.yml` (generated)

**Interfaces:**
- Consumes: `names.*` (Task 1); `stack.Stack`, `stack.Effective`, `stack.Defaults`, `stack.ServiceNames`, `stack.Protocol` (Tasks 2–3); `tmpl.Template`, `tmpl.Vars` (Task 4)
- Produces:
  - `render.Input{Workspace, App string; Stack *stack.Stack; Templates map[string]tmpl.Template; Defaults stack.Defaults; DataDir string; BindIP string}`
  - `render.Output{YAML []byte; Hosts map[string]string /* short service → host */; Ports []PublishedPort}`
  - `render.PublishedPort{Service string; Container int; Host int; Protocol string; HostIP string}` (JSON tags `service`, `container`, `host`, `protocol`, `host_ip`)
  - `render.Render(in Input) (Output, error)` — expects an already validated stack

- [ ] **Step 1: Add the YAML dependency**

```bash
go get go.yaml.in/yaml/v3@v3.0.5
```

- [ ] **Step 2: Write the failing test**

`internal/render/render_test.go`:

```go
package render

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/sebmahr/deploy-gateway/internal/stack"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
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
				"traefik.enable":                   "true",
				"traefik.http.routers.{{id}}.rule": "Host(`{{host}}`)",
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
	if !reflect.DeepEqual(hc, map[string]any{"test": []any{"CMD", "pg_isready"}, "interval": "10s", "retries": 5}) {
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
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/render/`
Expected: FAIL — `undefined: Input`, `undefined: Render`.

- [ ] **Step 4: Write `compose.go`**

`internal/render/compose.go`:

```go
package render

// Typed compose structures. Only the fields listed here can ever appear in a
// rendered file; anything else is unrepresentable by construction.

type composeFile struct {
	Name     string                    `yaml:"name"`
	Services map[string]composeService `yaml:"services"`
	Networks map[string]composeNetwork `yaml:"networks"`
}

type composeService struct {
	Image         string                    `yaml:"image"`
	ContainerName string                    `yaml:"container_name"`
	Hostname      string                    `yaml:"hostname"`
	Command       []string                  `yaml:"command,omitempty"`
	Entrypoint    []string                  `yaml:"entrypoint,omitempty"`
	Environment   map[string]string         `yaml:"environment,omitempty"`
	User          string                    `yaml:"user,omitempty"`
	Restart       string                    `yaml:"restart"`
	CapDrop       []string                  `yaml:"cap_drop"`
	SecurityOpt   []string                  `yaml:"security_opt"`
	ReadOnly      bool                      `yaml:"read_only"`
	Tmpfs         []string                  `yaml:"tmpfs"`
	MemLimit      int64                     `yaml:"mem_limit"`
	MemswapLimit  int64                     `yaml:"memswap_limit"`
	CPUs          float64                   `yaml:"cpus"`
	PidsLimit     int64                     `yaml:"pids_limit"`
	Logging       composeLogging            `yaml:"logging"`
	Volumes       []composeVolume           `yaml:"volumes,omitempty"`
	Ports         []composePort             `yaml:"ports,omitempty"`
	Healthcheck   *composeHealth            `yaml:"healthcheck,omitempty"`
	DependsOn     map[string]composeDepends `yaml:"depends_on,omitempty"`
	Networks      []string                  `yaml:"networks"`
	Labels        map[string]string         `yaml:"labels"`
}

type composeLogging struct {
	Driver  string            `yaml:"driver"`
	Options map[string]string `yaml:"options"`
}

type composeVolume struct {
	Type   string      `yaml:"type"`
	Source string      `yaml:"source"`
	Target string      `yaml:"target"`
	Bind   composeBind `yaml:"bind"`
}

type composeBind struct {
	CreateHostPath bool `yaml:"create_host_path"`
}

type composePort struct {
	Target    int    `yaml:"target"`
	Published string `yaml:"published"`
	HostIP    string `yaml:"host_ip"`
	Protocol  string `yaml:"protocol"`
}

type composeHealth struct {
	Test     []string `yaml:"test"`
	Interval string   `yaml:"interval"`
	Retries  int      `yaml:"retries"`
}

type composeDepends struct {
	Condition string `yaml:"condition"`
}

type composeNetwork struct {
	Name     string `yaml:"name"`
	External bool   `yaml:"external,omitempty"`
}
```

- [ ] **Step 5: Write `render.go`**

`internal/render/render.go`:

```go
// Package render turns a validated stack into a hardened compose file
// (spec §9). Hardening is hard-coded here; neither templates nor agents can
// change it.
package render

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sebmahr/deploy-gateway/internal/names"
	"github.com/sebmahr/deploy-gateway/internal/stack"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
)

type Input struct {
	Workspace string
	App       string
	Stack     *stack.Stack
	Templates map[string]tmpl.Template
	Defaults  stack.Defaults
	DataDir   string // <root>/<ws>/<app>/data
	BindIP    string
}

type PublishedPort struct {
	Service   string `json:"service"`
	Container int    `json:"container"`
	Host      int    `json:"host"`
	Protocol  string `json:"protocol"`
	HostIP    string `json:"host_ip"`
}

type Output struct {
	YAML  []byte
	Hosts map[string]string // short service name -> public host
	Ports []PublishedPort
}

func Render(in Input) (Output, error) {
	ws, app := in.Workspace, in.App
	privateNet := names.Network(ws, app)
	file := composeFile{
		Name:     names.Project(ws, app),
		Services: map[string]composeService{},
		Networks: map[string]composeNetwork{privateNet: {Name: privateNet}},
	}
	out := Output{Hosts: map[string]string{}, Ports: []PublishedPort{}}

	for _, svcName := range stack.ServiceNames(in.Stack) {
		svc := in.Stack.Services[svcName]
		id := names.Container(ws, app, svcName)
		res := stack.Effective(svc, in.Defaults)
		cs := composeService{
			Image:         svc.Image,
			ContainerName: id,
			Hostname:      id,
			Command:       escapeAll(svc.Command),
			Entrypoint:    escapeAll(svc.Entrypoint),
			Environment:   escapeMap(svc.Environment),
			User:          svc.User,
			Restart:       "unless-stopped",
			CapDrop:       []string{"ALL"},
			SecurityOpt:   []string{"no-new-privileges:true"},
			ReadOnly:      !svc.WritableRootfs,
			Tmpfs:         []string{"/tmp"},
			MemLimit:      res.MemoryBytes,
			MemswapLimit:  res.MemoryBytes,
			CPUs:          res.CPUs,
			PidsLimit:     res.Pids,
			Logging:       composeLogging{Driver: "json-file", Options: map[string]string{"max-size": "10m", "max-file": "3"}},
			Networks:      []string{privateNet},
			Labels:        map[string]string{},
		}
		if svc.Volume != nil {
			cs.Volumes = []composeVolume{{
				Type:   "bind",
				Source: filepath.Join(in.DataDir, svcName),
				Target: svc.Volume.Path,
				Bind:   composeBind{CreateHostPath: false},
			}}
		}
		for _, p := range svc.Ports {
			proto := stack.Protocol(p)
			cs.Ports = append(cs.Ports, composePort{Target: p.Container, Published: strconv.Itoa(p.Host), HostIP: in.BindIP, Protocol: proto})
			out.Ports = append(out.Ports, PublishedPort{Service: svcName, Container: p.Container, Host: p.Host, Protocol: proto, HostIP: in.BindIP})
		}
		if hc := svc.Healthcheck; hc != nil {
			interval, retries := hc.Interval, hc.Retries
			if interval == "" {
				interval = "30s"
			}
			if retries == 0 {
				retries = 3
			}
			cs.Healthcheck = &composeHealth{Test: append([]string{"CMD"}, escapeAll(hc.Command)...), Interval: interval, Retries: retries}
		}
		if len(svc.DependsOn) > 0 {
			cs.DependsOn = map[string]composeDepends{}
			for _, d := range svc.DependsOn {
				cond := "service_started"
				if in.Stack.Services[d].Healthcheck != nil {
					cond = "service_healthy"
				}
				cs.DependsOn[names.Container(ws, app, d)] = composeDepends{Condition: cond}
			}
		}
		if e := svc.Expose; e != nil {
			t, ok := in.Templates[e.Template]
			if !ok {
				return Output{}, fmt.Errorf("render: template %q not provided", e.Template)
			}
			vars := tmpl.Vars{Workspace: ws, App: app, Service: svcName, Port: e.Port}
			for k, v := range t.RenderLabels(vars) {
				cs.Labels[k] = escape(v)
			}
			for _, n := range t.Networks {
				if !slices.Contains(cs.Networks, n) {
					cs.Networks = append(cs.Networks, n)
				}
				file.Networks[n] = composeNetwork{Name: n, External: true}
			}
			if h := t.Host(vars); h != "" {
				out.Hosts[svcName] = h
			}
		}
		// Owner labels last so nothing can overwrite them.
		cs.Labels[names.LabelWorkspace] = ws
		cs.Labels[names.LabelApp] = app
		cs.Labels[names.LabelService] = svcName
		cs.Labels[names.LabelManaged] = "true"
		file.Services[id] = cs
	}

	y, err := yaml.Marshal(file)
	if err != nil {
		return Output{}, fmt.Errorf("render: marshal: %w", err)
	}
	out.YAML = y
	return out, nil
}

// escape prevents compose variable interpolation.
func escape(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func escapeAll(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = escape(s)
	}
	return out
}

func escapeMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = escape(v)
	}
	return out
}
```

- [ ] **Step 6: Generate the golden file and run the tests**

Run: `go test ./internal/render/ -run TestRenderGolden -update && go test ./internal/render/`
Expected: PASS. Open `internal/render/testdata/basic.golden.yml` and read it once: every service must show `cap_drop`, `security_opt`, `read_only`, `tmpfs`, limits and owner labels; `GREETING` must read `cost $${HOME} $$5`.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/render
git commit -m "feat(render): render validated stacks into hardened compose YAML"
```

---

### Task 6: Independent post-render verifier

**Files:**
- Create: `internal/verify/verify.go`
- Test: `internal/verify/verify_test.go`

**Interfaces:**
- Consumes: `names.*` (Task 1). Must **not** import `render`, `stack` or `tmpl`.
- Produces:
  - `verify.Policy{Workspace, App, DataDir string; Networks map[string]bool; MaxMemoryBytes int64; MaxCPUs float64; MaxPids int64; PortRangeStart, PortRangeEnd int; BindIP string}` — `Networks` = `dg-<ws>-<app>` plus all networks of templates granted to the workspace
  - `verify.Violations []string` (implements `error`)
  - `verify.Verify(doc []byte, p Policy) error` — nil or `Violations`

- [ ] **Step 1: Write the failing test**

`internal/verify/verify_test.go`:

```go
package verify

import (
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
			w["ports"].([]any)[0].(map[string]any)["host_ip"] = "192.168.0.53"
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/verify/`
Expected: FAIL — `undefined: Verify`.

- [ ] **Step 3: Write the implementation**

`internal/verify/verify.go`:

```go
// Package verify independently checks a rendered compose file against the
// security invariants (spec §10, layer 2). It deliberately shares no code
// with package render: a renderer bug must not be able to hide from it.
package verify

import (
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/sebmahr/deploy-gateway/internal/names"
)

type Policy struct {
	Workspace      string
	App            string
	DataDir        string
	Networks       map[string]bool
	MaxMemoryBytes int64
	MaxCPUs        float64
	MaxPids        int64
	PortRangeStart int
	PortRangeEnd   int
	BindIP         string
}

type Violations []string

func (v Violations) Error() string { return "compose verification failed: " + strings.Join(v, "; ") }

func (v *Violations) addf(format string, args ...any) { *v = append(*v, fmt.Sprintf(format, args...)) }

var (
	allowedServiceKeys = set("image", "container_name", "hostname", "command", "entrypoint", "environment",
		"user", "restart", "cap_drop", "security_opt", "read_only", "tmpfs", "mem_limit", "memswap_limit",
		"cpus", "pids_limit", "logging", "volumes", "ports", "healthcheck", "depends_on", "networks", "labels")
	forbiddenKeys = set("privileged", "cap_add", "devices", "device_cgroup_rules", "network_mode", "pid", "ipc",
		"uts", "userns_mode", "cgroup", "cgroup_parent", "runtime", "extra_hosts", "build", "extends", "include",
		"configs", "secrets", "sysctls", "ulimits", "volumes_from", "links", "external_links", "storage_opt",
		"isolation", "gpus")
	allowedTags = set("", "!!str", "!!int", "!!float", "!!bool", "!!map", "!!seq", "!!null")
	ownerLabels = set(names.LabelWorkspace, names.LabelApp, names.LabelService, names.LabelManaged)
	userRe      = regexp.MustCompile(`^[0-9]{1,10}:[0-9]{1,10}$`)
)

func set(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, i := range items {
		m[i] = true
	}
	return m
}

func Verify(doc []byte, p Policy) error {
	var v Violations
	var root yaml.Node
	if err := yaml.Unmarshal(doc, &root); err != nil {
		return Violations{"unparseable YAML: " + err.Error()}
	}
	checkNodes(&root, &v)
	if len(v) > 0 {
		return v
	}
	var f map[string]any
	if err := root.Decode(&f); err != nil {
		return Violations{"decode: " + err.Error()}
	}
	for _, k := range sortedKeys(f) {
		if k != "name" && k != "services" && k != "networks" {
			v.addf("top-level key %q not allowed", k)
		}
	}
	if s, _ := f["name"].(string); s != names.Project(p.Workspace, p.App) {
		v.addf("name must be %q", names.Project(p.Workspace, p.App))
	}
	checkTopNetworks(f["networks"], p, &v)
	services, ok := f["services"].(map[string]any)
	if !ok || len(services) == 0 {
		v.addf("services missing")
	}
	for _, key := range sortedKeys(services) {
		checkService(key, services[key], services, p, &v)
	}
	checkDollar(f, "", &v)
	if len(v) > 0 {
		return v
	}
	return nil
}

func checkNodes(n *yaml.Node, v *Violations) {
	if n.Kind == yaml.AliasNode {
		v.addf("YAML alias at line %d not allowed", n.Line)
		return
	}
	if n.Anchor != "" {
		v.addf("YAML anchor %q at line %d not allowed", n.Anchor, n.Line)
	}
	if !allowedTags[n.Tag] {
		v.addf("YAML tag %q at line %d not allowed", n.Tag, n.Line)
	}
	for _, c := range n.Content {
		checkNodes(c, v)
	}
}

func checkTopNetworks(raw any, p Policy, v *Violations) {
	m, ok := raw.(map[string]any)
	own := names.Network(p.Workspace, p.App)
	if !ok {
		v.addf("top-level networks missing")
		return
	}
	if _, ok := m[own]; !ok {
		v.addf("top-level network %q missing", own)
	}
	for _, k := range sortedKeys(m) {
		n, ok := m[k].(map[string]any)
		if !ok {
			v.addf("network %q must be a mapping", k)
			continue
		}
		for _, nk := range sortedKeys(n) {
			if nk != "name" && nk != "external" {
				v.addf("network %q: key %q not allowed", k, nk)
			}
		}
		if n["name"] != k {
			v.addf("network %q: name must equal its key", k)
		}
		ext, _ := n["external"].(bool)
		switch {
		case k == own:
			if ext {
				v.addf("network %q must not be external", k)
			}
		case !p.Networks[k]:
			v.addf("network %q not allowed", k)
		case !ext:
			v.addf("template network %q must be external", k)
		}
	}
}

func checkService(key string, raw any, all map[string]any, p Policy, v *Violations) {
	s, ok := raw.(map[string]any)
	if !ok {
		v.addf("service %q is not a mapping", key)
		return
	}
	ws, app, svc, ok := names.Parse(key)
	if !ok || ws != p.Workspace || app != p.App {
		v.addf("service key %q is not <workspace>-<app>-<service> for this app", key)
		return
	}
	for _, k := range sortedKeys(s) {
		if forbiddenKeys[k] {
			v.addf("%s: forbidden key %q", key, k)
		} else if !allowedServiceKeys[k] {
			v.addf("%s: key %q not allowed", key, k)
		}
	}
	if img, _ := s["image"].(string); img == "" {
		v.addf("%s: image missing", key)
	}
	if s["container_name"] != key {
		v.addf("%s: container_name must be %q", key, key)
	}
	if s["hostname"] != key {
		v.addf("%s: hostname must be %q", key, key)
	}
	if s["restart"] != "unless-stopped" {
		v.addf("%s: restart must be unless-stopped", key)
	}
	if !equalStrings(s["cap_drop"], "ALL") {
		v.addf("%s: cap_drop must be [ALL]", key)
	}
	if !equalStrings(s["security_opt"], "no-new-privileges:true") {
		v.addf("%s: security_opt must be exactly [no-new-privileges:true]", key)
	}
	if _, ok := s["read_only"].(bool); !ok {
		v.addf("%s: read_only must be set", key)
	}
	if !equalStrings(s["tmpfs"], "/tmp") {
		v.addf("%s: tmpfs must be [/tmp]", key)
	}
	mem, okMem := asInt(s["mem_limit"])
	if !okMem || mem <= 0 || mem > p.MaxMemoryBytes {
		v.addf("%s: mem_limit missing or above %d", key, p.MaxMemoryBytes)
	}
	if swap, ok := asInt(s["memswap_limit"]); !ok || swap != mem {
		v.addf("%s: memswap_limit must equal mem_limit", key)
	}
	if cpus, ok := asFloat(s["cpus"]); !ok || cpus <= 0 || cpus > p.MaxCPUs {
		v.addf("%s: cpus missing or above %g", key, p.MaxCPUs)
	}
	if pids, ok := asInt(s["pids_limit"]); !ok || pids <= 0 || pids > p.MaxPids {
		v.addf("%s: pids_limit missing or above %d", key, p.MaxPids)
	}
	if u, present := s["user"]; present {
		if us, _ := u.(string); !userRe.MatchString(us) {
			v.addf("%s: user must be numeric uid:gid", key)
		}
	}
	checkLogging(key, s["logging"], v)
	checkVolumes(key, svc, s["volumes"], p, v)
	checkPorts(key, s["ports"], p, v)
	checkServiceNetworks(key, s["networks"], p, v)
	checkLabels(key, svc, s["labels"], p, v)
	checkDependsOn(key, s["depends_on"], all, v)
	checkHealth(key, s["healthcheck"], v)
}

func checkLogging(key string, raw any, v *Violations) {
	m, ok := raw.(map[string]any)
	if !ok || m["driver"] != "json-file" {
		v.addf("%s: logging driver must be json-file", key)
		return
	}
	opts, _ := m["options"].(map[string]any)
	for _, k := range sortedKeys(opts) {
		if k != "max-size" && k != "max-file" {
			v.addf("%s: logging option %q not allowed", key, k)
		}
	}
	for _, k := range sortedKeys(m) {
		if k != "driver" && k != "options" {
			v.addf("%s: logging key %q not allowed", key, k)
		}
	}
}

func checkVolumes(key, svc string, raw any, p Policy, v *Violations) {
	if raw == nil {
		return
	}
	list, ok := raw.([]any)
	if !ok {
		v.addf("%s: volumes must be a list", key)
		return
	}
	if len(list) > 1 {
		v.addf("%s: at most one volume allowed", key)
	}
	want := filepath.Join(p.DataDir, svc)
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			v.addf("%s: volumes must use long syntax", key)
			continue
		}
		for _, k := range sortedKeys(m) {
			if k != "type" && k != "source" && k != "target" && k != "bind" {
				v.addf("%s: volume key %q not allowed", key, k)
			}
		}
		src, _ := m["source"].(string)
		tgt, _ := m["target"].(string)
		if strings.Contains(src, "docker.sock") || strings.Contains(tgt, "docker.sock") {
			v.addf("%s: docker.sock must never be mounted", key)
		}
		if m["type"] != "bind" {
			v.addf(`%s: volume type must be "bind"`, key)
			continue
		}
		if src != want {
			v.addf("%s: bind source %q must be %q", key, src, want)
			continue
		}
		if !path.IsAbs(tgt) {
			v.addf("%s: volume target must be absolute", key)
		}
		if b, present := m["bind"]; present {
			bm, ok := b.(map[string]any)
			if !ok || len(bm) != 1 || bm["create_host_path"] != false {
				v.addf("%s: bind options must be exactly {create_host_path: false}", key)
			}
		}
		checkNoSymlink(key, src, v)
	}
}

// checkNoSymlink resolves the deepest existing ancestor of src and requires
// that it resolves to itself (no symlink anywhere on the path).
func checkNoSymlink(key, src string, v *Violations) {
	existing := src
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return
		}
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil || real != existing {
		v.addf("%s: bind path %q contains a symlink", key, existing)
	}
}

func checkPorts(key string, raw any, p Policy, v *Violations) {
	if raw == nil {
		return
	}
	list, ok := raw.([]any)
	if !ok {
		v.addf("%s: ports must be a list", key)
		return
	}
	if p.PortRangeStart == 0 && len(list) > 0 {
		v.addf("%s: ports not allowed for this workspace", key)
		return
	}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			v.addf("%s: ports must use long syntax", key)
			continue
		}
		for _, k := range sortedKeys(m) {
			if k != "target" && k != "published" && k != "host_ip" && k != "protocol" {
				v.addf("%s: port key %q not allowed", key, k)
			}
		}
		if t, ok := asInt(m["target"]); !ok || t < 1 || t > 65535 {
			v.addf("%s: port target invalid", key)
		}
		pub, ok := asPort(m["published"])
		if !ok || pub < p.PortRangeStart || pub > p.PortRangeEnd {
			v.addf("%s: published port %v outside %d-%d", key, m["published"], p.PortRangeStart, p.PortRangeEnd)
		}
		if m["host_ip"] != p.BindIP {
			v.addf("%s: host_ip must be %q", key, p.BindIP)
		}
		if m["protocol"] != "tcp" && m["protocol"] != "udp" {
			v.addf("%s: protocol must be tcp or udp", key)
		}
	}
}

func checkServiceNetworks(key string, raw any, p Policy, v *Violations) {
	list, ok := raw.([]any)
	if !ok {
		v.addf("%s: networks must be a plain list (aliases not allowed)", key)
		return
	}
	own := names.Network(p.Workspace, p.App)
	hasOwn := false
	for _, item := range list {
		n, _ := item.(string)
		if n == own {
			hasOwn = true
		}
		if !p.Networks[n] {
			v.addf("%s: network %q not allowed", key, n)
		}
	}
	if !hasOwn {
		v.addf("%s: must join %q", key, own)
	}
}

func checkLabels(key, svc string, raw any, p Policy, v *Violations) {
	m, ok := raw.(map[string]any)
	if !ok {
		v.addf("%s: labels missing", key)
		return
	}
	want := map[string]string{
		names.LabelWorkspace: p.Workspace,
		names.LabelApp:       p.App,
		names.LabelService:   svc,
		names.LabelManaged:   "true",
	}
	for _, k := range sortedKeys(m) {
		if _, ok := m[k].(string); !ok {
			v.addf("%s: label %q must be a string", key, k)
		}
		if strings.HasPrefix(strings.ToLower(k), names.LabelPrefix) && !ownerLabels[k] {
			v.addf("%s: label %q not allowed", key, k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if m[k] != want[k] {
			v.addf("%s: label %s must be %q", key, k, want[k])
		}
	}
}

func checkDependsOn(key string, raw any, all map[string]any, v *Violations) {
	if raw == nil {
		return
	}
	m, ok := raw.(map[string]any)
	if !ok {
		v.addf("%s: depends_on must be a mapping", key)
		return
	}
	for _, dep := range sortedKeys(m) {
		if _, ok := all[dep]; !ok {
			v.addf("%s: depends_on unknown service %q", key, dep)
		}
		d, ok := m[dep].(map[string]any)
		if !ok || len(d) != 1 || (d["condition"] != "service_started" && d["condition"] != "service_healthy") {
			v.addf("%s: depends_on %q must only set condition", key, dep)
		}
	}
}

func checkHealth(key string, raw any, v *Violations) {
	if raw == nil {
		return
	}
	m, ok := raw.(map[string]any)
	if !ok {
		v.addf("%s: healthcheck must be a mapping", key)
		return
	}
	for _, k := range sortedKeys(m) {
		if k != "test" && k != "interval" && k != "retries" {
			v.addf("%s: healthcheck key %q not allowed", key, k)
		}
	}
	test, _ := m["test"].([]any)
	if len(test) < 2 || test[0] != "CMD" {
		v.addf("%s: healthcheck test must be exec form [CMD, ...]", key)
	}
}

// checkDollar finds any $ not written as $$ in string values.
func checkDollar(node any, at string, v *Violations) {
	switch n := node.(type) {
	case string:
		if strings.Contains(strings.ReplaceAll(n, "$$", ""), "$") {
			v.addf("%s: unescaped $ in value", at)
		}
	case map[string]any:
		for _, k := range sortedKeys(n) {
			checkDollar(n[k], at+"."+k, v)
		}
	case []any:
		for i, item := range n {
			checkDollar(item, at+"["+strconv.Itoa(i)+"]", v)
		}
	}
}

func equalStrings(raw any, want ...string) bool {
	list, ok := raw.([]any)
	if !ok || len(list) != len(want) {
		return false
	}
	for i, item := range list {
		if item != want[i] {
			return false
		}
	}
	return true
}

func asInt(raw any) (int64, bool) {
	switch n := raw.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case uint64:
		if n > 1<<62 {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}

func asFloat(raw any) (float64, bool) {
	switch n := raw.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	}
	return 0, false
}

func asPort(raw any) (int, bool) {
	switch n := raw.(type) {
	case string:
		i, err := strconv.Atoi(n)
		return i, err == nil
	case int:
		return n, true
	}
	return 0, false
}

func sortedKeys(m map[string]any) []string { return slices.Sorted(maps.Keys(m)) }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/verify/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/verify
git commit -m "feat(verify): independent post-render compose verifier"
```

---

### Task 7: Fuzz test — every valid stack renders to a verifiable file

**Files:**
- Test: `internal/render/fuzz_test.go`

**Interfaces:**
- Consumes: `stack.Decode`, `stack.Validate`, `stack.Grants`, `stack.Limits`, `stack.Defaults` (Tasks 2–3); `tmpl.Template` (Task 4); `render.Render` (Task 5); `verify.Verify`, `verify.Policy` (Task 6)
- Produces: nothing (test only)

- [ ] **Step 1: Write the fuzz test**

`internal/render/fuzz_test.go`:

```go
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
```

- [ ] **Step 2: Run the seeds as a normal test**

Run: `go test ./internal/render/ -run FuzzRenderVerify`
Expected: PASS (seeds only).

- [ ] **Step 3: Fuzz for a minute**

Run: `go test ./internal/render/ -run '^$' -fuzz FuzzRenderVerify -fuzztime 60s`
Expected: `PASS` with no new failing inputs. If a failure is found, Go writes it to `internal/render/testdata/fuzz/FuzzRenderVerify/`; fix the bug in `stack`/`render`/`verify` (whichever is wrong per the spec), keep the corpus file, and rerun.

- [ ] **Step 4: Commit**

```bash
git add internal/render
git commit -m "test(render): fuzz valid stacks through render and verify"
```

---

### Task 8: Configuration

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `config.Config{AgentListen, AdminListen, DataDir, WorkspacesRoot, AdminPasswordHash, AdminUserHeader, DockerConfig, DockerHost, ComposeBin string; AdminTrustedProxies, TrustedProxies []netip.Prefix; DeployTimeout time.Duration; DeployConcurrency int}`
  - `(Config).ProxyMode() bool`
  - `config.Load(getenv func(string) string) (Config, error)`
  - `config.ParsePrefixes(s string) ([]netip.Prefix, error)` — comma-separated IPs or CIDRs

- [ ] **Step 1: Write the failing test**

`internal/config/config_test.go`:

```go
package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadDefaultsPasswordMode(t *testing.T) {
	c, err := Load(env(map[string]string{
		"WORKSPACES_ROOT":     "/srv/dg/workspaces",
		"ADMIN_PASSWORD_HASH": "$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.AgentListen != ":8080" || c.AdminListen != ":8081" || c.DataDir != "/data" ||
		c.DockerConfig != "/docker" || c.ComposeBin != "/usr/local/bin/docker-compose" ||
		c.DockerHost != "unix:///var/run/docker.sock" ||
		c.DeployTimeout != 300*time.Second || c.DeployConcurrency != 1 || c.ProxyMode() {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoadProxyMode(t *testing.T) {
	c, err := Load(env(map[string]string{
		"WORKSPACES_ROOT":       "/srv/dg/workspaces",
		"ADMIN_TRUSTED_PROXIES": "172.19.0.149, 10.0.0.0/8",
		"ADMIN_USER_HEADER":     "Remote-User",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.ProxyMode() || len(c.AdminTrustedProxies) != 2 || c.AdminTrustedProxies[0].String() != "172.19.0.149/32" {
		t.Fatalf("unexpected: %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{"WORKSPACES_ROOT": "/srv/dg/workspaces", "ADMIN_PASSWORD_HASH": "$argon2id$x"}
	}
	cases := map[string]func(map[string]string){
		"missing root":      func(m map[string]string) { delete(m, "WORKSPACES_ROOT") },
		"relative root":     func(m map[string]string) { m["WORKSPACES_ROOT"] = "workspaces" },
		"unclean root":      func(m map[string]string) { m["WORKSPACES_ROOT"] = "/srv/dg/../x" },
		"proxy incomplete":  func(m map[string]string) { delete(m, "ADMIN_PASSWORD_HASH"); m["ADMIN_USER_HEADER"] = "Remote-User" },
		"bad hash":          func(m map[string]string) { m["ADMIN_PASSWORD_HASH"] = "plaintext" },
		"bad proxies":       func(m map[string]string) { m["TRUSTED_PROXIES"] = "not-an-ip" },
		"bad timeout":       func(m map[string]string) { m["DEPLOY_TIMEOUT"] = "1s" },
		"bad concurrency":   func(m map[string]string) { m["DEPLOY_CONCURRENCY"] = "0" },
	}
	for name, f := range cases {
		m := base()
		f(m)
		if _, err := Load(env(m)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := Load(env(map[string]string{"DEPLOY_CONCURRENCY": "x"}))
	if err == nil || !strings.Contains(err.Error(), "WORKSPACES_ROOT") || !strings.Contains(err.Error(), "DEPLOY_CONCURRENCY") {
		t.Fatalf("got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/`
Expected: FAIL — `undefined: Load`.

- [ ] **Step 3: Write the implementation**

`internal/config/config.go`:

```go
// Package config loads gateway configuration from the environment (spec §16).
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AgentListen         string
	AdminListen         string
	DataDir             string
	WorkspacesRoot      string
	AdminPasswordHash   string
	AdminTrustedProxies []netip.Prefix
	AdminUserHeader     string
	TrustedProxies      []netip.Prefix
	DockerConfig        string
	DockerHost          string
	ComposeBin          string
	DeployTimeout       time.Duration
	DeployConcurrency   int
}

// ProxyMode is true when the built-in admin login is disabled and a trusted
// forward-auth proxy authenticates admins.
func (c Config) ProxyMode() bool { return c.AdminPasswordHash == "" }

func Load(getenv func(string) string) (Config, error) {
	or := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	c := Config{
		AgentListen:       or("AGENT_LISTEN", ":8080"),
		AdminListen:       or("ADMIN_LISTEN", ":8081"),
		DataDir:           or("DATA_DIR", "/data"),
		WorkspacesRoot:    getenv("WORKSPACES_ROOT"),
		AdminPasswordHash: getenv("ADMIN_PASSWORD_HASH"),
		AdminUserHeader:   getenv("ADMIN_USER_HEADER"),
		DockerConfig:      or("DOCKER_CONFIG", "/docker"),
		DockerHost:        or("DOCKER_HOST", "unix:///var/run/docker.sock"),
		ComposeBin:        or("COMPOSE_BIN", "/usr/local/bin/docker-compose"),
		DeployTimeout:     300 * time.Second,
		DeployConcurrency: 1,
	}
	var errs []string
	if c.WorkspacesRoot == "" || !filepath.IsAbs(c.WorkspacesRoot) || filepath.Clean(c.WorkspacesRoot) != c.WorkspacesRoot {
		errs = append(errs, "WORKSPACES_ROOT must be a clean absolute path")
	}
	var err error
	if c.AdminTrustedProxies, err = ParsePrefixes(getenv("ADMIN_TRUSTED_PROXIES")); err != nil {
		errs = append(errs, "ADMIN_TRUSTED_PROXIES: "+err.Error())
	}
	if c.TrustedProxies, err = ParsePrefixes(getenv("TRUSTED_PROXIES")); err != nil {
		errs = append(errs, "TRUSTED_PROXIES: "+err.Error())
	}
	if s := getenv("DEPLOY_TIMEOUT"); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d < 10*time.Second || d > time.Hour {
			errs = append(errs, "DEPLOY_TIMEOUT must be a duration between 10s and 1h")
		} else {
			c.DeployTimeout = d
		}
	}
	if s := getenv("DEPLOY_CONCURRENCY"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 8 {
			errs = append(errs, "DEPLOY_CONCURRENCY must be 1-8")
		} else {
			c.DeployConcurrency = n
		}
	}
	if c.ProxyMode() {
		if len(c.AdminTrustedProxies) == 0 || c.AdminUserHeader == "" {
			errs = append(errs, "ADMIN_PASSWORD_HASH is empty (proxy mode): ADMIN_TRUSTED_PROXIES and ADMIN_USER_HEADER are required")
		}
	} else if !strings.HasPrefix(c.AdminPasswordHash, "$argon2id$") {
		errs = append(errs, "ADMIN_PASSWORD_HASH must be an argon2id hash (use `deploy-gateway hash-password`)")
	}
	if len(errs) > 0 {
		return Config{}, errors.New(strings.Join(errs, "; "))
	}
	return c, nil
}

// ParsePrefixes parses a comma-separated list of IPs and CIDRs.
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			p, err := netip.ParsePrefix(part)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q", part)
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("invalid IP %q", part)
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat(config): environment configuration with fail-closed admin auth"
```

---
### Task 9: Store — schema, workspaces, tokens, templates, grants

**Files:**
- Create: `internal/store/schema.sql`, `internal/store/store.go`, `internal/store/workspaces.go`, `internal/store/tokens.go`, `internal/store/templates.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Consumes: `tmpl.Template` (Task 4)
- Produces:
  - `store.Open(path string) (*Store, error)`, `(*Store).Close() error`
  - errors `store.ErrNotFound`, `store.ErrExists`, `store.ErrInUse`
  - `store.Quota{MaxApps, MaxServicesPerApp int; MaxTotalMemoryMB int64; MaxTotalCPUs float64; DefaultMemoryMB, MaxMemoryMB int64; DefaultCPUs, MaxCPUs float64; DefaultPids, MaxPids int64}`, `store.BuiltinDefaultQuota() Quota`, `(Quota).Validate() error`
  - `store.Workspace{ID int64; Name string; CreatedAt time.Time; Quota Quota; PortRangeStart, PortRangeEnd int; PortBindIP string}`
  - `CreateWorkspace(ctx, Workspace) (Workspace, error)`, `UpdateWorkspace(ctx, Workspace) error`, `GetWorkspace(ctx, id int64) (Workspace, error)`, `GetWorkspaceByName(ctx, name string) (Workspace, error)`, `ListWorkspaces(ctx) ([]Workspace, error)`, `DeleteWorkspace(ctx, id int64) error`, `OverlappingWorkspace(ctx, start, end int, excludeID int64) (string, error)`
  - `store.Token{ID, WorkspaceID int64; Label, Prefix string; CreatedAt time.Time; LastUsedAt, RevokedAt *time.Time}`
  - `CreateToken(ctx, wsID int64, label, hash, prefix string) (Token, error)`, `TokenByHash(ctx, hash string) (Token, Workspace, error)` (non-revoked only), `TouchToken(ctx, id int64) error`, `RevokeToken(ctx, id int64) error`, `GetToken(ctx, id int64) (Token, error)`, `ListTokens(ctx, wsID int64) ([]Token, error)`
  - `store.StoredTemplate{ID int64; tmpl.Template; CreatedAt, UpdatedAt time.Time}`
  - `CreateTemplate(ctx, tmpl.Template) (int64, error)`, `UpdateTemplate(ctx, id int64, t tmpl.Template) error`, `DeleteTemplate(ctx, id int64) error`, `GetTemplate(ctx, id int64) (StoredTemplate, error)`, `ListTemplates(ctx) ([]StoredTemplate, error)`, `SetGrants(ctx, wsID int64, templateIDs []int64) error`, `GrantedTemplates(ctx, wsID int64) ([]StoredTemplate, error)`

- [ ] **Step 1: Add the SQLite dependency**

```bash
go get modernc.org/sqlite@v1.60.1
```

- [ ] **Step 2: Write the failing test**

`internal/store/store_test.go`:

```go
package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sebmahr/deploy-gateway/internal/tmpl"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newWorkspace(t *testing.T, s *Store, name string, start, end int) Workspace {
	t.Helper()
	w, err := s.CreateWorkspace(context.Background(), Workspace{
		Name: name, Quota: BuiltinDefaultQuota(), PortRangeStart: start, PortRangeEnd: end, PortBindIP: "0.0.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestQuotaValidate(t *testing.T) {
	if err := BuiltinDefaultQuota().Validate(); err != nil {
		t.Fatalf("builtin default invalid: %v", err)
	}
	q := BuiltinDefaultQuota()
	q.DefaultMemoryMB = q.MaxMemoryMB + 1
	if err := q.Validate(); err == nil {
		t.Fatal("default above max must be rejected")
	}
	q = BuiltinDefaultQuota()
	q.MaxApps = 0
	if err := q.Validate(); err == nil {
		t.Fatal("max_apps 0 must be rejected")
	}
}

func TestWorkspaceCRUD(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 20000, 20099)
	if w.ID == 0 || w.CreatedAt.IsZero() {
		t.Fatalf("not populated: %+v", w)
	}
	if _, err := s.CreateWorkspace(ctx, Workspace{Name: "alice", Quota: BuiltinDefaultQuota(), PortBindIP: "0.0.0.0"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	w.Quota.MaxApps = 9
	w.PortBindIP = "127.0.0.1"
	if err := s.UpdateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetWorkspaceByName(ctx, "alice")
	if err != nil || got.Quota.MaxApps != 9 || got.PortBindIP != "127.0.0.1" || got.PortRangeEnd != 20099 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if _, err := s.GetWorkspace(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	list, _ := s.ListWorkspaces(ctx)
	if len(list) != 1 {
		t.Fatalf("list = %v", list)
	}
	if err := s.DeleteWorkspace(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetWorkspace(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}

func TestOverlappingWorkspace(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a := newWorkspace(t, s, "alice", 20000, 20099)
	newWorkspace(t, s, "nopublish", 0, 0)
	cases := []struct {
		start, end int
		exclude    int64
		want       string
	}{
		{20050, 20150, 0, "alice"},
		{19900, 20000, 0, "alice"},
		{20100, 20199, 0, ""},
		{20050, 20150, a.ID, ""},
	}
	for _, c := range cases {
		got, err := s.OverlappingWorkspace(ctx, c.start, c.end, c.exclude)
		if err != nil || got != c.want {
			t.Errorf("%d-%d excl %d: got %q, %v", c.start, c.end, c.exclude, got, err)
		}
	}
}

func TestTokens(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 0, 0)
	tok, err := s.CreateToken(ctx, w.ID, "ci", "hash1", "dgw_abcdefgh")
	if err != nil {
		t.Fatal(err)
	}
	got, ws, err := s.TokenByHash(ctx, "hash1")
	if err != nil || got.ID != tok.ID || ws.Name != "alice" {
		t.Fatalf("TokenByHash = %+v %+v %v", got, ws, err)
	}
	if err := s.TouchToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetToken(ctx, tok.ID); got.LastUsedAt == nil {
		t.Fatal("last_used_at not set")
	}
	if err := s.RevokeToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.TokenByHash(ctx, "hash1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token still valid: %v", err)
	}
	list, _ := s.ListTokens(ctx, w.ID)
	if len(list) != 1 || list[0].RevokedAt == nil {
		t.Fatalf("ListTokens = %+v", list)
	}
	if err := s.DeleteWorkspace(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetToken(ctx, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("tokens must be deleted with their workspace")
	}
}

func TestTemplatesAndGrants(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 0, 0)
	tp := tmpl.Template{Name: "public-web", Description: "d", HostPattern: "{{app}}.{{workspace}}.a.example.com",
		Networks: []string{"apps"}, Labels: map[string]string{"traefik.enable": "true"}}
	id, err := s.CreateTemplate(ctx, tp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTemplate(ctx, tp); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate template: %v", err)
	}
	internal, _ := s.CreateTemplate(ctx, tmpl.Template{Name: "internal", Networks: []string{"apps"}})
	tp.Description = "updated"
	if err := s.UpdateTemplate(ctx, id, tp); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTemplate(ctx, id)
	if err != nil || got.Description != "updated" || got.Labels["traefik.enable"] != "true" || got.Networks[0] != "apps" {
		t.Fatalf("GetTemplate = %+v, %v", got, err)
	}
	if err := s.SetGrants(ctx, w.ID, []int64{id, internal}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGrants(ctx, w.ID, []int64{id}); err != nil {
		t.Fatal(err)
	}
	granted, _ := s.GrantedTemplates(ctx, w.ID)
	if len(granted) != 1 || granted[0].Name != "public-web" {
		t.Fatalf("granted = %+v", granted)
	}
	if err := s.DeleteTemplate(ctx, id); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete granted template: %v", err)
	}
	if err := s.DeleteTemplate(ctx, internal); err != nil {
		t.Fatal(err)
	}
	all, _ := s.ListTemplates(ctx)
	if len(all) != 1 {
		t.Fatalf("templates = %+v", all)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/store/`
Expected: FAIL — `undefined: Open`.

- [ ] **Step 4: Write `schema.sql` and `store.go`**

`internal/store/schema.sql`:

```sql
CREATE TABLE IF NOT EXISTS workspaces (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  max_apps INTEGER NOT NULL,
  max_services_per_app INTEGER NOT NULL,
  max_total_memory_mb INTEGER NOT NULL,
  max_total_cpus REAL NOT NULL,
  default_memory_mb INTEGER NOT NULL,
  max_memory_mb INTEGER NOT NULL,
  default_cpus REAL NOT NULL,
  max_cpus REAL NOT NULL,
  default_pids INTEGER NOT NULL,
  max_pids INTEGER NOT NULL,
  port_range_start INTEGER NOT NULL DEFAULT 0,
  port_range_end INTEGER NOT NULL DEFAULT 0,
  port_bind_ip TEXT NOT NULL DEFAULT '0.0.0.0'
);

CREATE TABLE IF NOT EXISTS tokens (
  id INTEGER PRIMARY KEY,
  workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  label TEXT NOT NULL,
  hash TEXT NOT NULL UNIQUE,
  prefix TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER,
  revoked_at INTEGER
);

CREATE TABLE IF NOT EXISTS templates (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  description TEXT NOT NULL,
  host_pattern TEXT NOT NULL,
  networks_json TEXT NOT NULL,
  labels_json TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS workspace_templates (
  workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  template_id INTEGER NOT NULL REFERENCES templates(id) ON DELETE RESTRICT,
  PRIMARY KEY (workspace_id, template_id)
);

CREATE TABLE IF NOT EXISTS apps (
  id INTEGER PRIMARY KEY,
  workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  memory_bytes INTEGER NOT NULL DEFAULT 0,
  cpus REAL NOT NULL DEFAULT 0,
  hosts_json TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  last_deploy_at INTEGER,
  last_deploy_result TEXT NOT NULL DEFAULT '',
  UNIQUE (workspace_id, name)
);

CREATE TABLE IF NOT EXISTS port_claims (
  protocol TEXT NOT NULL,
  host_port INTEGER NOT NULL,
  container_port INTEGER NOT NULL,
  service TEXT NOT NULL,
  app_id INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
  PRIMARY KEY (protocol, host_port)
);

CREATE TABLE IF NOT EXISTS audit (
  id INTEGER PRIMARY KEY,
  ts INTEGER NOT NULL,
  actor_type TEXT NOT NULL,
  actor TEXT NOT NULL,
  workspace TEXT NOT NULL,
  token_id INTEGER,
  action TEXT NOT NULL,
  params_json TEXT NOT NULL,
  result TEXT NOT NULL,
  error TEXT NOT NULL,
  severity TEXT NOT NULL,
  duration_ms INTEGER NOT NULL,
  client_ip TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS audit_ts ON audit(ts);

CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
```

`internal/store/store.go`:

```go
// Package store persists gateway state in SQLite (spec §15).
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrInUse    = errors.New("in use")
)

type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Open opens (and migrates) the database. A single connection serialises
// all access, which is plenty for this workload and avoids SQLITE_BUSY.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}
	return &Store{db: db, now: time.Now}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// withTx runs fn in a transaction. fn must only use tx (never s.db), because
// the pool has a single connection.
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func timePtr(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.Unix(v.Int64, 0)
	return &t
}

type scanner interface{ Scan(dest ...any) error }
```

- [ ] **Step 5: Write `workspaces.go`**

`internal/store/workspaces.go`:

```go
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Quota struct {
	MaxApps           int     `json:"max_apps"`
	MaxServicesPerApp int     `json:"max_services_per_app"`
	MaxTotalMemoryMB  int64   `json:"max_total_memory_mb"`
	MaxTotalCPUs      float64 `json:"max_total_cpus"`
	DefaultMemoryMB   int64   `json:"default_memory_mb"`
	MaxMemoryMB       int64   `json:"max_memory_mb"`
	DefaultCPUs       float64 `json:"default_cpus"`
	MaxCPUs           float64 `json:"max_cpus"`
	DefaultPids       int64   `json:"default_pids"`
	MaxPids           int64   `json:"max_pids"`
}

// BuiltinDefaultQuota is deliberately small: the reference host has 4 cores
// and 15 GB RAM and already swaps.
func BuiltinDefaultQuota() Quota {
	return Quota{
		MaxApps: 5, MaxServicesPerApp: 4,
		MaxTotalMemoryMB: 2048, MaxTotalCPUs: 2,
		DefaultMemoryMB: 256, MaxMemoryMB: 1024,
		DefaultCPUs: 0.5, MaxCPUs: 1,
		DefaultPids: 256, MaxPids: 1024,
	}
}

func (q Quota) Validate() error {
	var errs []string
	check := func(ok bool, msg string) {
		if !ok {
			errs = append(errs, msg)
		}
	}
	check(q.MaxApps >= 1, "max_apps must be at least 1")
	check(q.MaxServicesPerApp >= 1 && q.MaxServicesPerApp <= 16, "max_services_per_app must be 1-16")
	check(q.MaxTotalMemoryMB >= 16, "max_total_memory_mb must be at least 16")
	check(q.MaxTotalCPUs > 0, "max_total_cpus must be positive")
	check(q.DefaultMemoryMB >= 16 && q.DefaultMemoryMB <= q.MaxMemoryMB, "default_memory_mb must be 16..max_memory_mb")
	check(q.MaxMemoryMB <= q.MaxTotalMemoryMB, "max_memory_mb must not exceed max_total_memory_mb")
	check(q.DefaultCPUs > 0 && q.DefaultCPUs <= q.MaxCPUs, "default_cpus must be >0 and <= max_cpus")
	check(q.MaxCPUs <= q.MaxTotalCPUs, "max_cpus must not exceed max_total_cpus")
	check(q.DefaultPids >= 16 && q.DefaultPids <= q.MaxPids, "default_pids must be 16..max_pids")
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

type Workspace struct {
	ID             int64     `json:"-"`
	Name           string    `json:"name"`
	CreatedAt      time.Time `json:"created_at"`
	Quota          Quota     `json:"quota"`
	PortRangeStart int       `json:"port_range_start"`
	PortRangeEnd   int       `json:"port_range_end"`
	PortBindIP     string    `json:"port_bind_ip"`
}

const wsCols = `id, name, created_at, max_apps, max_services_per_app, max_total_memory_mb, max_total_cpus,
	default_memory_mb, max_memory_mb, default_cpus, max_cpus, default_pids, max_pids,
	port_range_start, port_range_end, port_bind_ip`

func scanWorkspace(sc scanner) (Workspace, error) {
	var w Workspace
	var created int64
	q := &w.Quota
	err := sc.Scan(&w.ID, &w.Name, &created, &q.MaxApps, &q.MaxServicesPerApp, &q.MaxTotalMemoryMB, &q.MaxTotalCPUs,
		&q.DefaultMemoryMB, &q.MaxMemoryMB, &q.DefaultCPUs, &q.MaxCPUs, &q.DefaultPids, &q.MaxPids,
		&w.PortRangeStart, &w.PortRangeEnd, &w.PortBindIP)
	w.CreatedAt = time.Unix(created, 0)
	return w, notFound(err)
}

func (s *Store) CreateWorkspace(ctx context.Context, w Workspace) (Workspace, error) {
	q := w.Quota
	w.CreatedAt = time.Unix(s.now().Unix(), 0)
	res, err := s.db.ExecContext(ctx, `INSERT INTO workspaces (name, created_at, max_apps, max_services_per_app,
		max_total_memory_mb, max_total_cpus, default_memory_mb, max_memory_mb, default_cpus, max_cpus,
		default_pids, max_pids, port_range_start, port_range_end, port_bind_ip)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.Name, w.CreatedAt.Unix(), q.MaxApps, q.MaxServicesPerApp, q.MaxTotalMemoryMB, q.MaxTotalCPUs,
		q.DefaultMemoryMB, q.MaxMemoryMB, q.DefaultCPUs, q.MaxCPUs, q.DefaultPids, q.MaxPids,
		w.PortRangeStart, w.PortRangeEnd, w.PortBindIP)
	if isUnique(err) {
		return Workspace{}, ErrExists
	}
	if err != nil {
		return Workspace{}, err
	}
	w.ID, err = res.LastInsertId()
	return w, err
}

func (s *Store) UpdateWorkspace(ctx context.Context, w Workspace) error {
	q := w.Quota
	res, err := s.db.ExecContext(ctx, `UPDATE workspaces SET max_apps=?, max_services_per_app=?, max_total_memory_mb=?,
		max_total_cpus=?, default_memory_mb=?, max_memory_mb=?, default_cpus=?, max_cpus=?, default_pids=?, max_pids=?,
		port_range_start=?, port_range_end=?, port_bind_ip=? WHERE id=?`,
		q.MaxApps, q.MaxServicesPerApp, q.MaxTotalMemoryMB, q.MaxTotalCPUs, q.DefaultMemoryMB, q.MaxMemoryMB,
		q.DefaultCPUs, q.MaxCPUs, q.DefaultPids, q.MaxPids, w.PortRangeStart, w.PortRangeEnd, w.PortBindIP, w.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetWorkspace(ctx context.Context, id int64) (Workspace, error) {
	return scanWorkspace(s.db.QueryRowContext(ctx, `SELECT `+wsCols+` FROM workspaces WHERE id=?`, id))
}

func (s *Store) GetWorkspaceByName(ctx context.Context, name string) (Workspace, error) {
	return scanWorkspace(s.db.QueryRowContext(ctx, `SELECT `+wsCols+` FROM workspaces WHERE name=?`, name))
}

func (s *Store) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+wsCols+` FROM workspaces ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Workspace{}
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) DeleteWorkspace(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workspaces WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// OverlappingWorkspace returns the name of a workspace (other than
// excludeID) whose port range intersects [start, end], or "".
func (s *Store) OverlappingWorkspace(ctx context.Context, start, end int, excludeID int64) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM workspaces
		WHERE id != ? AND port_range_start > 0 AND port_range_start <= ? AND port_range_end >= ?
		ORDER BY name LIMIT 1`, excludeID, end, start).Scan(&name)
	if err != nil && !errors.Is(notFound(err), ErrNotFound) {
		return "", fmt.Errorf("store: overlap query: %w", err)
	}
	return name, nil
}
```

- [ ] **Step 6: Write `tokens.go`**

`internal/store/tokens.go`:

```go
package store

import (
	"context"
	"database/sql"
	"time"
)

type Token struct {
	ID          int64
	WorkspaceID int64
	Label       string
	Prefix      string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	RevokedAt   *time.Time
}

const tokenCols = `id, workspace_id, label, prefix, created_at, last_used_at, revoked_at`

func scanToken(sc scanner) (Token, error) {
	var t Token
	var created int64
	var used, revoked sql.NullInt64
	err := sc.Scan(&t.ID, &t.WorkspaceID, &t.Label, &t.Prefix, &created, &used, &revoked)
	t.CreatedAt = time.Unix(created, 0)
	t.LastUsedAt, t.RevokedAt = timePtr(used), timePtr(revoked)
	return t, notFound(err)
}

func (s *Store) CreateToken(ctx context.Context, wsID int64, label, hash, prefix string) (Token, error) {
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO tokens (workspace_id, label, hash, prefix, created_at) VALUES (?, ?, ?, ?, ?)`,
		wsID, label, hash, prefix, now)
	if isUnique(err) {
		return Token{}, ErrExists
	}
	if err != nil {
		return Token{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Token{}, err
	}
	return s.GetToken(ctx, id)
}

func (s *Store) GetToken(ctx context.Context, id int64) (Token, error) {
	return scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE id=?`, id))
}

// TokenByHash returns a non-revoked token and its workspace.
func (s *Store) TokenByHash(ctx context.Context, hash string) (Token, Workspace, error) {
	t, err := scanToken(s.db.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE hash=? AND revoked_at IS NULL`, hash))
	if err != nil {
		return Token{}, Workspace{}, err
	}
	w, err := s.GetWorkspace(ctx, t.WorkspaceID)
	if err != nil {
		return Token{}, Workspace{}, err
	}
	return t, w, nil
}

func (s *Store) TouchToken(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tokens SET last_used_at=? WHERE id=?`, s.now().Unix(), id)
	return err
}

func (s *Store) RevokeToken(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE tokens SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, s.now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListTokens(ctx context.Context, wsID int64) ([]Token, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+tokenCols+` FROM tokens WHERE workspace_id=? ORDER BY id`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
```

- [ ] **Step 7: Write `templates.go`**

`internal/store/templates.go`:

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/tmpl"
)

type StoredTemplate struct {
	ID int64
	tmpl.Template
	CreatedAt time.Time
	UpdatedAt time.Time
}

const tplCols = `id, name, description, host_pattern, networks_json, labels_json, created_at, updated_at`

func scanTemplate(sc scanner) (StoredTemplate, error) {
	var t StoredTemplate
	var nets, labels string
	var created, updated int64
	if err := sc.Scan(&t.ID, &t.Name, &t.Description, &t.HostPattern, &nets, &labels, &created, &updated); err != nil {
		return StoredTemplate{}, notFound(err)
	}
	t.CreatedAt, t.UpdatedAt = time.Unix(created, 0), time.Unix(updated, 0)
	if err := json.Unmarshal([]byte(nets), &t.Networks); err != nil {
		return StoredTemplate{}, err
	}
	if err := json.Unmarshal([]byte(labels), &t.Labels); err != nil {
		return StoredTemplate{}, err
	}
	return t, nil
}

func encodeTemplate(t tmpl.Template) (nets, labels string) {
	if t.Networks == nil {
		t.Networks = []string{}
	}
	if t.Labels == nil {
		t.Labels = map[string]string{}
	}
	n, _ := json.Marshal(t.Networks)
	l, _ := json.Marshal(t.Labels)
	return string(n), string(l)
}

func (s *Store) CreateTemplate(ctx context.Context, t tmpl.Template) (int64, error) {
	nets, labels := encodeTemplate(t)
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO templates (name, description, host_pattern, networks_json, labels_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, t.Name, t.Description, t.HostPattern, nets, labels, now, now)
	if isUnique(err) {
		return 0, ErrExists
	}
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateTemplate(ctx context.Context, id int64, t tmpl.Template) error {
	nets, labels := encodeTemplate(t)
	res, err := s.db.ExecContext(ctx, `UPDATE templates SET name=?, description=?, host_pattern=?, networks_json=?, labels_json=?, updated_at=?
		WHERE id=?`, t.Name, t.Description, t.HostPattern, nets, labels, s.now().Unix(), id)
	if isUnique(err) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteTemplate(ctx context.Context, id int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var grants int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_templates WHERE template_id=?`, id).Scan(&grants); err != nil {
			return err
		}
		if grants > 0 {
			return ErrInUse
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM templates WHERE id=?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *Store) GetTemplate(ctx context.Context, id int64) (StoredTemplate, error) {
	return scanTemplate(s.db.QueryRowContext(ctx, `SELECT `+tplCols+` FROM templates WHERE id=?`, id))
}

func (s *Store) ListTemplates(ctx context.Context) ([]StoredTemplate, error) {
	return s.queryTemplates(ctx, `SELECT `+tplCols+` FROM templates ORDER BY name`)
}

func (s *Store) GrantedTemplates(ctx context.Context, wsID int64) ([]StoredTemplate, error) {
	return s.queryTemplates(ctx, `SELECT t.id, t.name, t.description, t.host_pattern, t.networks_json, t.labels_json, t.created_at, t.updated_at
		FROM templates t JOIN workspace_templates wt ON wt.template_id = t.id WHERE wt.workspace_id=? ORDER BY t.name`, wsID)
}

func (s *Store) queryTemplates(ctx context.Context, q string, args ...any) ([]StoredTemplate, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StoredTemplate{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) SetGrants(ctx context.Context, wsID int64, templateIDs []int64) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_templates WHERE workspace_id=?`, wsID); err != nil {
			return err
		}
		for _, id := range templateIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO workspace_templates (workspace_id, template_id) VALUES (?, ?)`, wsID, id); err != nil {
				return err
			}
		}
		return nil
	})
}
```

- [ ] **Step 8: Run test to verify it passes**

Run: `go test ./internal/store/`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add go.mod go.sum internal/store
git commit -m "feat(store): sqlite schema, workspaces, tokens, templates, grants"
```

---

### Task 10: Store — apps, port claims, usage, audit, settings

**Files:**
- Create: `internal/store/apps.go`, `internal/store/audit.go`
- Test: `internal/store/apps_test.go`

**Interfaces:**
- Consumes: Task 9 (`Store`, `Quota`, helpers)
- Produces:
  - `store.App{ID, WorkspaceID int64; Name string; MemoryBytes int64; CPUs float64; Hosts map[string]string; CreatedAt, UpdatedAt time.Time; LastDeployAt *time.Time; LastDeployResult string}`
  - `store.PortClaim{Protocol string; HostPort, ContainerPort int; Service, App string}` (JSON `protocol`, `host_port`, `container_port`, `service`, `app`)
  - `*store.PortTakenError{Claim PortClaim}`
  - `store.Usage{Apps int; MemoryBytes int64; CPUs float64}`
  - `GetApp(ctx, wsID int64, name string) (App, error)`, `ListApps(ctx, wsID int64) ([]App, error)`, `AppKeys(ctx) (map[string]bool, error)` (keys `"<ws>/<app>"`)
  - `SaveDeployment(ctx, wsID int64, name string, mem int64, cpus float64, hosts map[string]string, claims []PortClaim) (App, error)` — atomic upsert + claim replacement, returns `*PortTakenError` on conflict
  - `PortConflicts(ctx, wsID int64, app string, claims []PortClaim) ([]PortClaim, error)`, `ListPortClaims(ctx, wsID int64) ([]PortClaim, error)`
  - `RecordDeploy(ctx, appID int64, result string) error`, `DeleteApp(ctx, appID int64) error`, `WorkspaceUsage(ctx, wsID int64, excludeApp string) (Usage, error)`
  - `store.AuditEntry{ID int64; Time time.Time; ActorType, Actor, Workspace string; TokenID int64; Action, Params, Result, Error, Severity string; DurationMS int64; ClientIP string}`
  - `store.AuditFilter{Workspace, Action, Result, Severity string; BeforeID int64; Limit int}`
  - `InsertAudit(ctx, AuditEntry) error`, `ListAudit(ctx, AuditFilter) ([]AuditEntry, error)`, `PruneAudit(ctx, before time.Time) (int64, error)`
  - `DefaultQuota(ctx) (Quota, error)`, `SetDefaultQuota(ctx, Quota) error`, `AuditRetentionDays(ctx) (int, error)`, `SetAuditRetentionDays(ctx, int) error`

- [ ] **Step 1: Write the failing test**

`internal/store/apps_test.go`:

```go
package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSaveDeploymentAndUsage(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 20000, 20099)
	a, err := s.SaveDeployment(ctx, w.ID, "shop", 256<<20, 0.5, map[string]string{"web": "shop.alice.example.com"},
		[]PortClaim{{Protocol: "tcp", HostPort: 20001, ContainerPort: 8080, Service: "web"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 || a.Hosts["web"] != "shop.alice.example.com" || a.MemoryBytes != 256<<20 {
		t.Fatalf("app = %+v", a)
	}
	if _, err := s.SaveDeployment(ctx, w.ID, "blog", 128<<20, 0.25, nil, nil); err != nil {
		t.Fatal(err)
	}
	u, err := s.WorkspaceUsage(ctx, w.ID, "")
	if err != nil || u.Apps != 2 || u.MemoryBytes != 384<<20 || u.CPUs != 0.75 {
		t.Fatalf("usage = %+v, %v", u, err)
	}
	u, _ = s.WorkspaceUsage(ctx, w.ID, "shop")
	if u.Apps != 1 || u.MemoryBytes != 128<<20 {
		t.Fatalf("usage excluding shop = %+v", u)
	}
	claims, _ := s.ListPortClaims(ctx, w.ID)
	if len(claims) != 1 || claims[0].App != "shop" || claims[0].HostPort != 20001 {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestPortClaimConflicts(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 20000, 20099)
	claim := PortClaim{Protocol: "tcp", HostPort: 20001, ContainerPort: 80, Service: "web"}
	if _, err := s.SaveDeployment(ctx, w.ID, "shop", 1, 0.1, nil, []PortClaim{claim}); err != nil {
		t.Fatal(err)
	}
	// Same app may re-claim its own port (redeploy).
	if _, err := s.SaveDeployment(ctx, w.ID, "shop", 2, 0.1, nil, []PortClaim{claim}); err != nil {
		t.Fatalf("redeploy own port: %v", err)
	}
	conflicts, err := s.PortConflicts(ctx, w.ID, "blog", []PortClaim{claim, {Protocol: "udp", HostPort: 20001}})
	if err != nil || len(conflicts) != 1 || conflicts[0].App != "shop" {
		t.Fatalf("conflicts = %+v, %v", conflicts, err)
	}
	_, err = s.SaveDeployment(ctx, w.ID, "blog", 1, 0.1, nil, []PortClaim{claim})
	var taken *PortTakenError
	if !errors.As(err, &taken) || taken.Claim.App != "shop" {
		t.Fatalf("expected PortTakenError, got %v", err)
	}
	if _, err := s.GetApp(ctx, w.ID, "blog"); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed SaveDeployment must not leave an app row behind")
	}
	// Redeploy shop without the port releases it.
	if _, err := s.SaveDeployment(ctx, w.ID, "shop", 1, 0.1, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveDeployment(ctx, w.ID, "blog", 1, 0.1, nil, []PortClaim{claim}); err != nil {
		t.Fatalf("port should be free now: %v", err)
	}
}

func TestRecordDeployDeleteAndKeys(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	w := newWorkspace(t, s, "alice", 20000, 20099)
	a, _ := s.SaveDeployment(ctx, w.ID, "shop", 1, 0.1, nil, []PortClaim{{Protocol: "tcp", HostPort: 20001, ContainerPort: 1, Service: "web"}})
	if err := s.RecordDeploy(ctx, a.ID, "ok"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetApp(ctx, w.ID, "shop")
	if got.LastDeployResult != "ok" || got.LastDeployAt == nil {
		t.Fatalf("app = %+v", got)
	}
	keys, _ := s.AppKeys(ctx)
	if !keys["alice/shop"] {
		t.Fatalf("keys = %v", keys)
	}
	if err := s.DeleteApp(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if claims, _ := s.ListPortClaims(ctx, w.ID); len(claims) != 0 {
		t.Fatal("claims must be deleted with the app")
	}
	if apps, _ := s.ListApps(ctx, w.ID); len(apps) != 0 {
		t.Fatalf("apps = %+v", apps)
	}
}

func TestAudit(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	old := time.Now().Add(-100 * 24 * time.Hour)
	entries := []AuditEntry{
		{Time: old, ActorType: "agent", Actor: "token:1", Workspace: "alice", TokenID: 1, Action: "deploy", Params: "{}", Result: "ok", Severity: "info"},
		{Time: time.Now(), ActorType: "agent", Actor: "token:1", Workspace: "alice", TokenID: 1, Action: "deploy", Params: "{}", Result: "security_violation", Severity: "critical"},
		{Time: time.Now(), ActorType: "admin", Actor: "admin", Workspace: "bob", Action: "create_workspace", Params: "{}", Result: "ok", Severity: "info"},
	}
	for _, e := range entries {
		if err := s.InsertAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	crit, _ := s.ListAudit(ctx, AuditFilter{Severity: "critical"})
	if len(crit) != 1 || crit[0].Result != "security_violation" {
		t.Fatalf("critical = %+v", crit)
	}
	alice, _ := s.ListAudit(ctx, AuditFilter{Workspace: "alice", Limit: 1})
	if len(alice) != 1 || alice[0].Severity != "critical" {
		t.Fatalf("newest-first limit failed: %+v", alice)
	}
	page2, _ := s.ListAudit(ctx, AuditFilter{Workspace: "alice", BeforeID: alice[0].ID})
	if len(page2) != 1 || page2[0].Result != "ok" {
		t.Fatalf("paging failed: %+v", page2)
	}
	n, err := s.PruneAudit(ctx, time.Now().Add(-90*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	q, err := s.DefaultQuota(ctx)
	if err != nil || q != BuiltinDefaultQuota() {
		t.Fatalf("default quota = %+v, %v", q, err)
	}
	q.MaxApps = 7
	if err := s.SetDefaultQuota(ctx, q); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.DefaultQuota(ctx); got.MaxApps != 7 {
		t.Fatalf("got %+v", got)
	}
	if d, _ := s.AuditRetentionDays(ctx); d != 90 {
		t.Fatalf("retention default = %d", d)
	}
	if err := s.SetAuditRetentionDays(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.AuditRetentionDays(ctx); d != 30 {
		t.Fatalf("retention = %d", d)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/`
Expected: FAIL — `undefined: PortClaim`, `undefined: AuditEntry`, ...

- [ ] **Step 3: Write `apps.go`**

`internal/store/apps.go`:

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type App struct {
	ID               int64
	WorkspaceID      int64
	Name             string
	MemoryBytes      int64
	CPUs             float64
	Hosts            map[string]string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	LastDeployAt     *time.Time
	LastDeployResult string
}

type PortClaim struct {
	Protocol      string `json:"protocol"`
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Service       string `json:"service"`
	App           string `json:"app"`
}

type PortTakenError struct{ Claim PortClaim }

func (e *PortTakenError) Error() string {
	return fmt.Sprintf("host port %s/%d is already used by app %q", e.Claim.Protocol, e.Claim.HostPort, e.Claim.App)
}

type Usage struct {
	Apps        int
	MemoryBytes int64
	CPUs        float64
}

const appCols = `id, workspace_id, name, memory_bytes, cpus, hosts_json, created_at, updated_at, last_deploy_at, last_deploy_result`

func scanApp(sc scanner) (App, error) {
	var a App
	var hosts string
	var created, updated int64
	var deployed sql.NullInt64
	if err := sc.Scan(&a.ID, &a.WorkspaceID, &a.Name, &a.MemoryBytes, &a.CPUs, &hosts, &created, &updated, &deployed, &a.LastDeployResult); err != nil {
		return App{}, notFound(err)
	}
	a.CreatedAt, a.UpdatedAt, a.LastDeployAt = time.Unix(created, 0), time.Unix(updated, 0), timePtr(deployed)
	if err := json.Unmarshal([]byte(hosts), &a.Hosts); err != nil {
		return App{}, err
	}
	return a, nil
}

func (s *Store) GetApp(ctx context.Context, wsID int64, name string) (App, error) {
	return scanApp(s.db.QueryRowContext(ctx, `SELECT `+appCols+` FROM apps WHERE workspace_id=? AND name=?`, wsID, name))
}

func (s *Store) ListApps(ctx context.Context, wsID int64) ([]App, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+appCols+` FROM apps WHERE workspace_id=? ORDER BY name`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []App{}
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AppKeys returns "<workspace>/<app>" for every app (used to find orphans).
func (s *Store) AppKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT w.name, a.name FROM apps a JOIN workspaces w ON w.id = a.workspace_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var ws, app string
		if err := rows.Scan(&ws, &app); err != nil {
			return nil, err
		}
		out[ws+"/"+app] = true
	}
	return out, rows.Err()
}

// SaveDeployment upserts the app record and replaces its port claims in one
// transaction. On a claim conflict nothing is changed.
func (s *Store) SaveDeployment(ctx context.Context, wsID int64, name string, mem int64, cpus float64, hosts map[string]string, claims []PortClaim) (App, error) {
	if hosts == nil {
		hosts = map[string]string{}
	}
	hostsJSON, _ := json.Marshal(hosts)
	now := s.now().Unix()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO apps (workspace_id, name, memory_bytes, cpus, hosts_json, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (workspace_id, name) DO UPDATE SET memory_bytes=excluded.memory_bytes, cpus=excluded.cpus,
			hosts_json=excluded.hosts_json, updated_at=excluded.updated_at`,
			wsID, name, mem, cpus, string(hostsJSON), now, now); err != nil {
			return err
		}
		var appID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM apps WHERE workspace_id=? AND name=?`, wsID, name).Scan(&appID); err != nil {
			return err
		}
		for _, c := range claims {
			var other PortClaim
			err := tx.QueryRowContext(ctx, `SELECT pc.protocol, pc.host_port, pc.container_port, pc.service, a.name
				FROM port_claims pc JOIN apps a ON a.id = pc.app_id
				WHERE pc.protocol=? AND pc.host_port=? AND pc.app_id != ?`, c.Protocol, c.HostPort, appID).
				Scan(&other.Protocol, &other.HostPort, &other.ContainerPort, &other.Service, &other.App)
			if err == nil {
				return &PortTakenError{Claim: other}
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM port_claims WHERE app_id=?`, appID); err != nil {
			return err
		}
		for _, c := range claims {
			if _, err := tx.ExecContext(ctx, `INSERT INTO port_claims (protocol, host_port, container_port, service, app_id) VALUES (?, ?, ?, ?, ?)`,
				c.Protocol, c.HostPort, c.ContainerPort, c.Service, appID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return App{}, err
	}
	return s.GetApp(ctx, wsID, name)
}

// PortConflicts returns existing claims on the given ports held by any app
// other than (wsID, app).
func (s *Store) PortConflicts(ctx context.Context, wsID int64, app string, claims []PortClaim) ([]PortClaim, error) {
	out := []PortClaim{}
	for _, c := range claims {
		var other PortClaim
		err := s.db.QueryRowContext(ctx, `SELECT pc.protocol, pc.host_port, pc.container_port, pc.service, a.name
			FROM port_claims pc JOIN apps a ON a.id = pc.app_id
			WHERE pc.protocol=? AND pc.host_port=? AND NOT (a.workspace_id=? AND a.name=?)`, c.Protocol, c.HostPort, wsID, app).
			Scan(&other.Protocol, &other.HostPort, &other.ContainerPort, &other.Service, &other.App)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, other)
	}
	return out, nil
}

func (s *Store) ListPortClaims(ctx context.Context, wsID int64) ([]PortClaim, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT pc.protocol, pc.host_port, pc.container_port, pc.service, a.name
		FROM port_claims pc JOIN apps a ON a.id = pc.app_id WHERE a.workspace_id=? ORDER BY pc.host_port, pc.protocol`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PortClaim{}
	for rows.Next() {
		var c PortClaim
		if err := rows.Scan(&c.Protocol, &c.HostPort, &c.ContainerPort, &c.Service, &c.App); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) RecordDeploy(ctx context.Context, appID int64, result string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE apps SET last_deploy_at=?, last_deploy_result=? WHERE id=?`, s.now().Unix(), result, appID)
	return err
}

func (s *Store) DeleteApp(ctx context.Context, appID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM apps WHERE id=?`, appID)
	return err
}

// WorkspaceUsage sums all apps of a workspace except excludeApp (running or
// stopped alike).
func (s *Store) WorkspaceUsage(ctx context.Context, wsID int64, excludeApp string) (Usage, error) {
	var u Usage
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(memory_bytes), 0), COALESCE(SUM(cpus), 0)
		FROM apps WHERE workspace_id=? AND name != ?`, wsID, excludeApp).Scan(&u.Apps, &u.MemoryBytes, &u.CPUs)
	return u, err
}
```

- [ ] **Step 4: Write `audit.go`**

`internal/store/audit.go`:

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

type AuditEntry struct {
	ID         int64
	Time       time.Time
	ActorType  string // agent | admin
	Actor      string
	Workspace  string
	TokenID    int64 // 0 = none
	Action     string
	Params     string // JSON, already redacted
	Result     string // ok | error code
	Error      string
	Severity   string // info | warn | critical
	DurationMS int64
	ClientIP   string
}

type AuditFilter struct {
	Workspace string
	Action    string
	Result    string
	Severity  string
	BeforeID  int64
	Limit     int
}

func (s *Store) InsertAudit(ctx context.Context, e AuditEntry) error {
	var tokenID any
	if e.TokenID != 0 {
		tokenID = e.TokenID
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit (ts, actor_type, actor, workspace, token_id, action, params_json,
		result, error, severity, duration_ms, client_ip) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Time.Unix(), e.ActorType, e.Actor, e.Workspace, tokenID, e.Action, e.Params, e.Result, e.Error,
		e.Severity, e.DurationMS, e.ClientIP)
	return err
}

func (s *Store) ListAudit(ctx context.Context, f AuditFilter) ([]AuditEntry, error) {
	var where []string
	var args []any
	for _, c := range []struct{ col, val string }{
		{"workspace", f.Workspace}, {"action", f.Action}, {"result", f.Result}, {"severity", f.Severity},
	} {
		if c.val != "" {
			where = append(where, c.col+" = ?")
			args = append(args, c.val)
		}
	}
	if f.BeforeID > 0 {
		where = append(where, "id < ?")
		args = append(args, f.BeforeID)
	}
	q := `SELECT id, ts, actor_type, actor, workspace, token_id, action, params_json, result, error, severity, duration_ms, client_ip FROM audit`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q += " ORDER BY id DESC LIMIT " + strconv.Itoa(limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var ts int64
		var tokenID sql.NullInt64
		if err := rows.Scan(&e.ID, &ts, &e.ActorType, &e.Actor, &e.Workspace, &tokenID, &e.Action, &e.Params,
			&e.Result, &e.Error, &e.Severity, &e.DurationMS, &e.ClientIP); err != nil {
			return nil, err
		}
		e.Time, e.TokenID = time.Unix(ts, 0), tokenID.Int64
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) PruneAudit(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM audit WHERE ts < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) getSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) setSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

func (s *Store) DefaultQuota(ctx context.Context) (Quota, error) {
	v, ok, err := s.getSetting(ctx, "default_quota")
	if err != nil || !ok {
		return BuiltinDefaultQuota(), err
	}
	var q Quota
	if err := json.Unmarshal([]byte(v), &q); err != nil {
		return BuiltinDefaultQuota(), err
	}
	return q, nil
}

func (s *Store) SetDefaultQuota(ctx context.Context, q Quota) error {
	if err := q.Validate(); err != nil {
		return err
	}
	b, _ := json.Marshal(q)
	return s.setSetting(ctx, "default_quota", string(b))
}

func (s *Store) AuditRetentionDays(ctx context.Context) (int, error) {
	v, ok, err := s.getSetting(ctx, "audit_retention_days")
	if err != nil || !ok {
		return 90, err
	}
	return strconv.Atoi(v)
}

func (s *Store) SetAuditRetentionDays(ctx context.Context, days int) error {
	if days < 1 || days > 3650 {
		return errors.New("audit retention must be 1-3650 days")
	}
	return s.setSetting(ctx, "audit_retention_days", strconv.Itoa(days))
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/store/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/store
git commit -m "feat(store): apps, atomic port claims, usage, audit, settings"
```

---

### Task 11: Auth primitives

**Files:**
- Create: `internal/auth/token.go`, `internal/auth/password.go`, `internal/auth/limiter.go`, `internal/auth/clientip.go`, `internal/auth/sessions.go`
- Test: `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `auth.GenerateToken() (token, hash, prefix string, err error)` — `dgw_` + 43 base64url chars; prefix = first 12 chars
  - `auth.HashToken(token string) string` — hex SHA-256
  - `auth.HashPassword(pw string) (string, error)`, `auth.CheckPassword(encoded, pw string) bool`
  - `auth.NewLimiter(max int, window, block time.Duration) *Limiter`; `(*Limiter).Blocked(key string) bool`, `Fail(key string)`, `Reset(key string)`; field `Now func() time.Time`
  - `auth.ClientIP(r *http.Request, trusted []netip.Prefix) string`, `auth.PeerTrusted(r *http.Request, trusted []netip.Prefix) bool`
  - `auth.NewSessions(idle time.Duration) *Sessions`; `Create(actor string) (string, error)`, `Lookup(id string) (string, bool)`, `Delete(id string)`; field `Now func() time.Time`

- [ ] **Step 1: Add the crypto dependency**

```bash
go get golang.org/x/crypto@v0.57.0
```

- [ ] **Step 2: Write the failing test**

`internal/auth/auth_test.go`:

```go
package auth

import (
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestGenerateToken(t *testing.T) {
	tok, hash, prefix, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, "dgw_") || len(tok) != 47 {
		t.Fatalf("token %q has wrong shape", tok)
	}
	if hash != HashToken(tok) || len(hash) != 64 || prefix != tok[:12] {
		t.Fatalf("hash/prefix mismatch: %q %q", hash, prefix)
	}
	tok2, _, _, _ := GenerateToken()
	if tok == tok2 {
		t.Fatal("tokens must be random")
	}
}

func TestPassword(t *testing.T) {
	h, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("unexpected encoding %q", h)
	}
	if !CheckPassword(h, "correct horse") || CheckPassword(h, "wrong") {
		t.Fatal("password check broken")
	}
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=999999999,t=3,p=2$c2FsdA$aGFzaA", "$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA"} {
		if CheckPassword(bad, "x") {
			t.Errorf("malformed hash %q accepted", bad)
		}
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(3, time.Minute, 15*time.Minute)
	l.Now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		l.Fail("1.2.3.4")
	}
	if l.Blocked("1.2.3.4") {
		t.Fatal("blocked too early")
	}
	now = now.Add(2 * time.Minute) // old failures expire
	l.Fail("1.2.3.4")
	if l.Blocked("1.2.3.4") {
		t.Fatal("expired failures counted")
	}
	l.Fail("1.2.3.4")
	l.Fail("1.2.3.4")
	if !l.Blocked("1.2.3.4") || l.Blocked("5.6.7.8") {
		t.Fatal("block state wrong")
	}
	now = now.Add(16 * time.Minute)
	if l.Blocked("1.2.3.4") {
		t.Fatal("block did not expire")
	}
}

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.19.0.149/32")}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.19.0.149:5555"
	r.Header.Set("X-Forwarded-For", "9.9.9.9, 203.0.113.7")
	if got := ClientIP(r, trusted); got != "203.0.113.7" {
		t.Fatalf("trusted proxy: got %q", got)
	}
	r.RemoteAddr = "10.0.0.5:5555"
	if got := ClientIP(r, trusted); got != "10.0.0.5" {
		t.Fatalf("untrusted peer must ignore XFF: got %q", got)
	}
	if PeerTrusted(r, trusted) {
		t.Fatal("peer should not be trusted")
	}
	r.RemoteAddr = "172.19.0.149:1"
	if !PeerTrusted(r, trusted) {
		t.Fatal("peer should be trusted")
	}
}

func TestSessions(t *testing.T) {
	now := time.Unix(0, 0)
	s := NewSessions(time.Hour)
	s.Now = func() time.Time { return now }
	id, err := s.Create("admin")
	if err != nil {
		t.Fatal(err)
	}
	if actor, ok := s.Lookup(id); !ok || actor != "admin" {
		t.Fatal("lookup failed")
	}
	now = now.Add(59 * time.Minute)
	if _, ok := s.Lookup(id); !ok {
		t.Fatal("lookup must refresh idle timer")
	}
	now = now.Add(61 * time.Minute)
	if _, ok := s.Lookup(id); ok {
		t.Fatal("session should have expired")
	}
	id, _ = s.Create("admin")
	s.Delete(id)
	if _, ok := s.Lookup(id); ok {
		t.Fatal("deleted session still valid")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/auth/`
Expected: FAIL — `undefined: GenerateToken`, ...

- [ ] **Step 4: Write the implementation**

`internal/auth/token.go`:

```go
// Package auth holds authentication primitives shared by the agent API and
// the admin UI.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// GenerateToken returns a new 256-bit agent token, its storage hash and a
// short display prefix.
func GenerateToken() (token, hash, prefix string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", "", err
	}
	token = "dgw_" + base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), token[:12], nil
}

// HashToken is a plain SHA-256: tokens carry 256 bits of entropy, so a slow
// hash adds nothing.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
```

`internal/auth/password.go`:

```go
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// CheckPassword verifies pw against a PHC-encoded argon2id hash. Parameters
// are bounded so a bad hash in config cannot make login exhaust memory.
func CheckPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	if m < 8*1024 || m > 1<<20 || t < 1 || t > 10 || p < 1 || p > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(key)))
	return subtle.ConstantTimeCompare(got, key) == 1
}
```

`internal/auth/limiter.go`:

```go
package auth

import (
	"sync"
	"time"
)

// Limiter blocks a key (client IP) after max failures within window.
type Limiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	block   time.Duration
	fails   map[string][]time.Time
	blocked map[string]time.Time
	Now     func() time.Time
}

func NewLimiter(max int, window, block time.Duration) *Limiter {
	return &Limiter{max: max, window: window, block: block,
		fails: map[string][]time.Time{}, blocked: map[string]time.Time{}, Now: time.Now}
}

func (l *Limiter) Blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	until, ok := l.blocked[key]
	if !ok {
		return false
	}
	if l.Now().Before(until) {
		return true
	}
	delete(l.blocked, key)
	return false
}

func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	if len(l.fails) > 10000 {
		l.fails = map[string][]time.Time{}
	}
	kept := l.fails[key][:0]
	for _, t := range l.fails[key] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	if len(kept) >= l.max {
		l.blocked[key] = now.Add(l.block)
		delete(l.fails, key)
		return
	}
	l.fails[key] = kept
}

func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}
```

`internal/auth/clientip.go`:

```go
package auth

import (
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP returns the client address, honouring X-Forwarded-For only when
// the direct peer is a trusted proxy. It walks XFF from the right and
// returns the first address that is not itself a trusted proxy.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	peer := peerAddr(r)
	if !peer.IsValid() {
		return r.RemoteAddr
	}
	if inPrefixes(peer, trusted) {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(parts) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
			if err != nil {
				break
			}
			a = a.Unmap()
			if !inPrefixes(a, trusted) {
				return a.String()
			}
		}
	}
	return peer.String()
}

func PeerTrusted(r *http.Request, trusted []netip.Prefix) bool {
	p := peerAddr(r)
	return p.IsValid() && inPrefixes(p, trusted)
}

func peerAddr(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

func inPrefixes(a netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
```

`internal/auth/sessions.go`:

```go
package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// Sessions is an in-memory admin session store with an idle timeout. A
// gateway restart logs the admin out, which is acceptable for one admin.
type Sessions struct {
	mu   sync.Mutex
	idle time.Duration
	m    map[string]session
	Now  func() time.Time
}

type session struct {
	actor    string
	lastSeen time.Time
}

func NewSessions(idle time.Duration) *Sessions {
	return &Sessions{idle: idle, m: map[string]session{}, Now: time.Now}
}

func (s *Sessions) Create(actor string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = session{actor: actor, lastSeen: s.Now()}
	return id, nil
}

func (s *Sessions) Lookup(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[id]
	now := s.Now()
	if !ok || now.Sub(sess.lastSeen) > s.idle {
		delete(s.m, id)
		return "", false
	}
	sess.lastSeen = now
	s.m[id] = sess
	return sess.actor, true
}

func (s *Sessions) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/auth/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/auth
git commit -m "feat(auth): tokens, argon2id passwords, limiter, client IP, sessions"
```

---

### Task 12: Compose runner

**Files:**
- Create: `internal/runner/runner.go`
- Test: `internal/runner/runner_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `runner.Options{Bin, DockerConfig, DockerHost string; Timeout time.Duration; Concurrency int}`
  - `runner.New(o Options) *Runner`
  - `(*Runner).Up(ctx, file, project string) (string, error)`
  - `(*Runner).Down(ctx, file, project string) (string, error)`
  - `(*Runner).Restart(ctx, file, project, service string) (string, error)` — service = full compose key or "" for all
  - `(*Runner).Stop(ctx, file, project, service string) (string, error)`
  - `(*Runner).TryLock(key string) (unlock func(), ok bool)`

- [ ] **Step 1: Write the failing test**

`internal/runner/runner_test.go`:

```go
package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCompose writes a script that records its argv and environment into
// dir, then behaves according to body.
func fakeCompose(t *testing.T, body string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "docker-compose")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + dir + "/args\nenv > " + dir + "/env\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpArgvAndScrubbedEnv(t *testing.T) {
	t.Setenv("COMPOSE_PROJECT_NAME", "evil")
	t.Setenv("COMPOSE_FILE", "/etc/evil.yml")
	bin, dir := fakeCompose(t, "echo started")
	r := New(Options{Bin: bin, DockerConfig: "/docker", DockerHost: "unix:///var/run/docker.sock", Timeout: 120 * time.Second, Concurrency: 1})
	out, err := r.Up(context.Background(), "/ws/alice/shop/docker-compose.yml", "dg-alice-shop")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "started") {
		t.Fatalf("output = %q", out)
	}
	wantArgs := "-f\n/ws/alice/shop/docker-compose.yml\n-p\ndg-alice-shop\nup\n-d\n--pull\nalways\n--remove-orphans\n--wait\n--wait-timeout\n120\n"
	if got := read(t, dir+"/args"); got != wantArgs {
		t.Fatalf("args =\n%s\nwant\n%s", got, wantArgs)
	}
	env := read(t, dir+"/env")
	if strings.Contains(env, "COMPOSE_") {
		t.Fatalf("COMPOSE_* leaked into env:\n%s", env)
	}
	if !strings.Contains(env, "DOCKER_CONFIG=/docker") || !strings.Contains(env, "DOCKER_HOST=unix:///var/run/docker.sock") {
		t.Fatalf("env missing docker settings:\n%s", env)
	}
}

func TestDownRestartStopArgv(t *testing.T) {
	bin, dir := fakeCompose(t, "")
	r := New(Options{Bin: bin, Timeout: time.Minute, Concurrency: 1})
	ctx := context.Background()
	cases := []struct {
		run  func() (string, error)
		want string
	}{
		{func() (string, error) { return r.Down(ctx, "/f.yml", "dg-a-b") }, "down\n-v\n--remove-orphans\n"},
		{func() (string, error) { return r.Restart(ctx, "/f.yml", "dg-a-b", "") }, "restart\n"},
		{func() (string, error) { return r.Restart(ctx, "/f.yml", "dg-a-b", "a-b-web") }, "restart\na-b-web\n"},
		{func() (string, error) { return r.Stop(ctx, "/f.yml", "dg-a-b", "a-b-web") }, "stop\na-b-web\n"},
	}
	for _, c := range cases {
		if _, err := c.run(); err != nil {
			t.Fatal(err)
		}
		if got := read(t, dir+"/args"); got != "-f\n/f.yml\n-p\ndg-a-b\n"+c.want {
			t.Errorf("args = %q", got)
		}
	}
}

func TestFailureReturnsOutput(t *testing.T) {
	bin, _ := fakeCompose(t, "echo 'pull access denied' >&2; exit 1")
	r := New(Options{Bin: bin, Timeout: time.Minute, Concurrency: 1})
	out, err := r.Up(context.Background(), "/f.yml", "dg-a-b")
	if err == nil || !strings.Contains(out, "pull access denied") {
		t.Fatalf("err=%v out=%q", err, out)
	}
}

func TestTimeoutKillsProcess(t *testing.T) {
	bin, _ := fakeCompose(t, "sleep 30")
	r := New(Options{Bin: bin, Timeout: time.Minute, Concurrency: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := r.Up(ctx, "/f.yml", "dg-a-b")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("process was not killed promptly")
	}
}

func TestTryLock(t *testing.T) {
	r := New(Options{Concurrency: 1})
	unlock, ok := r.TryLock("alice/shop")
	if !ok {
		t.Fatal("first lock failed")
	}
	if _, ok := r.TryLock("alice/shop"); ok {
		t.Fatal("second lock must fail")
	}
	if u2, ok := r.TryLock("alice/blog"); !ok {
		t.Fatal("other key must lock")
	} else {
		u2()
	}
	unlock()
	unlock() // idempotent
	if u3, ok := r.TryLock("alice/shop"); !ok {
		t.Fatal("lock not released")
	} else {
		u3()
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/runner/`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Write the implementation**

`internal/runner/runner.go`:

```go
// Package runner executes the standalone docker-compose binary with a fixed
// argv and a scrubbed environment (spec §11). No shell is ever involved.
package runner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type Options struct {
	Bin          string
	DockerConfig string
	DockerHost   string
	Timeout      time.Duration
	Concurrency  int
}

type Runner struct {
	opts  Options
	sem   chan struct{}
	mu    sync.Mutex
	locks map[string]bool
}

func New(o Options) *Runner {
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	if o.DockerHost == "" {
		o.DockerHost = "unix:///var/run/docker.sock"
	}
	return &Runner{opts: o, sem: make(chan struct{}, o.Concurrency), locks: map[string]bool{}}
}

// TryLock takes a non-blocking per-app lock. The returned unlock is idempotent.
func (r *Runner) TryLock(key string) (func(), bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.locks[key] {
		return nil, false
	}
	r.locks[key] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			delete(r.locks, key)
			r.mu.Unlock()
		})
	}, true
}

func (r *Runner) Up(ctx context.Context, file, project string) (string, error) {
	return r.run(ctx, file, project, "up", "-d", "--pull", "always", "--remove-orphans",
		"--wait", "--wait-timeout", strconv.Itoa(int(r.opts.Timeout.Seconds())))
}

func (r *Runner) Down(ctx context.Context, file, project string) (string, error) {
	return r.run(ctx, file, project, "down", "-v", "--remove-orphans")
}

func (r *Runner) Restart(ctx context.Context, file, project, service string) (string, error) {
	return r.run(ctx, file, project, withService("restart", service)...)
}

func (r *Runner) Stop(ctx context.Context, file, project, service string) (string, error) {
	return r.run(ctx, file, project, withService("stop", service)...)
}

func withService(cmd, service string) []string {
	if service == "" {
		return []string{cmd}
	}
	return []string{cmd, service}
}

func (r *Runner) run(ctx context.Context, file, project string, args ...string) (string, error) {
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return "", fmt.Errorf("compose %s: %w", args[0], ctx.Err())
	}
	defer func() { <-r.sem }()

	ctx, cancel := context.WithTimeout(ctx, r.opts.Timeout+30*time.Second)
	defer cancel()
	argv := append([]string{"-f", file, "-p", project}, args...)
	cmd := exec.CommandContext(ctx, r.opts.Bin, argv...)
	cmd.Dir = filepath.Dir(file)
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=/tmp",
		"DOCKER_CONFIG=" + r.opts.DockerConfig,
		"DOCKER_HOST=" + r.opts.DockerHost,
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	buf := &tailBuffer{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = buf, buf
	err := cmd.Run()
	if ctx.Err() != nil {
		return buf.String(), fmt.Errorf("compose %s timed out", args[0])
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return buf.String(), fmt.Errorf("compose %s failed with exit code %d", args[0], exitErr.ExitCode())
		}
		return buf.String(), fmt.Errorf("compose %s: %w", args[0], err)
	}
	return buf.String(), nil
}

// tailBuffer keeps the last max bytes of output. exec guarantees a single
// writer when Stdout and Stderr are the same pointer.
type tailBuffer struct {
	max int
	b   []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = t.b[over:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.b) }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/runner/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/runner
git commit -m "feat(runner): docker-compose execution with fixed argv and scrubbed env"
```

---

### Task 13: Docker client wrapper and runtime checks

**Files:**
- Create: `internal/dockerx/client.go`, `internal/dockerx/check.go`
- Test: `internal/dockerx/check_test.go`

**Interfaces:**
- Consumes: `names.Label*` (Task 1)
- Produces:
  - `dockerx.ContainerInfo{ID, Name string; Labels map[string]string; State, Status, Health string}`
  - `dockerx.Facts{Name string; Labels map[string]string; Privileged bool; CapAdd, CapDrop, SecurityOpt []string; NetworkMode, PidMode, IpcMode, UTSMode, UsernsMode, Runtime string; Devices int; Memory, PidsLimit int64; Mounts []MountFact; PortBindings []PortFact; State, Health string}`
  - `dockerx.MountFact{Type, Name, Source, Destination string}`, `dockerx.PortFact{ContainerPort int; Protocol, HostIP string; HostPort int}`
  - `dockerx.RuntimePolicy{Workspace, App, DataDir string; Ports []PortFact}`
  - `dockerx.CheckRuntime(f Facts, p RuntimePolicy) []string`
  - `dockerx.New() (*Client, error)` with methods `Ping(ctx) error`, `ProjectContainers(ctx, ws, app string) ([]ContainerInfo, error)`, `ManagedContainers(ctx) ([]ContainerInfo, error)`, `Inspect(ctx, id string) (Facts, error)`, `Logs(ctx, id string, tail int, since string, maxBytes int) (string, error)`, `NetworkExists(ctx, name string) (bool, error)`, `RemoveContainer(ctx, id string) error`, `MountSource(ctx, containerID, destination string) (source string, inContainer bool, err error)`

- [ ] **Step 1: Add the Docker client dependency**

```bash
go get github.com/moby/moby/client@v0.6.0
```

- [ ] **Step 2: Write the failing test**

`internal/dockerx/check_test.go`:

```go
package dockerx

import (
	"strings"
	"testing"
)

func goodFacts() Facts {
	return Facts{
		Name:        "alice-shop-web",
		Labels:      map[string]string{"deploy-gateway.workspace": "alice", "deploy-gateway.app": "shop"},
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges:true"},
		NetworkMode: "dg-alice-shop",
		IpcMode:     "private",
		Memory:      256 << 20,
		PidsLimit:   256,
		Mounts: []MountFact{
			{Type: "bind", Source: "/srv/ws/alice/shop/data/web", Destination: "/data"},
			{Type: "volume", Name: strings.Repeat("a", 64), Destination: "/var/cache"},
		},
		PortBindings: []PortFact{{ContainerPort: 8080, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 20001}},
	}
}

func policy() RuntimePolicy {
	return RuntimePolicy{Workspace: "alice", App: "shop", DataDir: "/srv/ws/alice/shop/data",
		Ports: []PortFact{{ContainerPort: 8080, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 20001}}}
}

func TestCheckRuntimeAcceptsGood(t *testing.T) {
	if v := CheckRuntime(goodFacts(), policy()); len(v) != 0 {
		t.Fatalf("unexpected violations: %v", v)
	}
}

func TestCheckRuntimeRejects(t *testing.T) {
	cases := []struct {
		name string
		f    func(*Facts)
		want string
	}{
		{"privileged", func(f *Facts) { f.Privileged = true }, "privileged"},
		{"cap_add", func(f *Facts) { f.CapAdd = []string{"NET_ADMIN"} }, "cap_add"},
		{"no cap_drop", func(f *Facts) { f.CapDrop = nil }, "cap_drop"},
		{"no nnp", func(f *Facts) { f.SecurityOpt = nil }, "no-new-privileges"},
		{"host network", func(f *Facts) { f.NetworkMode = "host" }, "network mode"},
		{"container network", func(f *Facts) { f.NetworkMode = "container:abc" }, "network mode"},
		{"host pid", func(f *Facts) { f.PidMode = "host" }, "pid mode"},
		{"host userns", func(f *Facts) { f.UsernsMode = "host" }, "userns mode"},
		{"runtime", func(f *Facts) { f.Runtime = "nvidia" }, "runtime"},
		{"devices", func(f *Facts) { f.Devices = 1 }, "devices"},
		{"no memory", func(f *Facts) { f.Memory = 0 }, "memory"},
		{"no pids", func(f *Facts) { f.PidsLimit = 0 }, "pids"},
		{"bind outside", func(f *Facts) { f.Mounts[0].Source = "/etc" }, "bind mount"},
		{"bind prefix trick", func(f *Facts) { f.Mounts[0].Source = "/srv/ws/alice/shop/data-evil" }, "bind mount"},
		{"docker.sock", func(f *Facts) { f.Mounts[0].Source = "/var/run/docker.sock" }, "bind mount"},
		{"named volume", func(f *Facts) { f.Mounts[1].Name = "shared-data" }, "named volume"},
		{"other mount type", func(f *Facts) { f.Mounts[1].Type = "npipe" }, "mount type"},
		{"owner mismatch", func(f *Facts) { f.Labels["deploy-gateway.workspace"] = "bob" }, "owner labels"},
		{"extra port", func(f *Facts) {
			f.PortBindings = append(f.PortBindings, PortFact{ContainerPort: 22, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 22})
		}, "port bindings"},
		{"wrong host ip", func(f *Facts) { f.PortBindings[0].HostIP = "" }, "port bindings"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := goodFacts()
			c.f(&f)
			v := CheckRuntime(f, policy())
			if len(v) == 0 || !strings.Contains(strings.Join(v, "; "), c.want) {
				t.Fatalf("want violation containing %q, got %v", c.want, v)
			}
		})
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/dockerx/`
Expected: FAIL — `undefined: Facts`.

- [ ] **Step 4: Write `check.go`**

`internal/dockerx/check.go`:

```go
package dockerx

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/sebmahr/deploy-gateway/internal/names"
)

type MountFact struct {
	Type        string
	Name        string
	Source      string
	Destination string
}

type PortFact struct {
	ContainerPort int
	Protocol      string
	HostIP        string
	HostPort      int
}

// Facts is the security-relevant subset of a container inspect result.
type Facts struct {
	Name         string
	Labels       map[string]string
	Privileged   bool
	CapAdd       []string
	CapDrop      []string
	SecurityOpt  []string
	NetworkMode  string
	PidMode      string
	IpcMode      string
	UTSMode      string
	UsernsMode   string
	Runtime      string
	Devices      int
	Memory       int64
	PidsLimit    int64
	Mounts       []MountFact
	PortBindings []PortFact
	State        string
	Health       string
}

// RuntimePolicy describes what one container of an app may look like.
// Ports are the expected bindings for this container's service.
type RuntimePolicy struct {
	Workspace string
	App       string
	DataDir   string
	Ports     []PortFact
}

// Anonymous volumes (from an image's VOLUME instruction) have random 64-hex
// names and live in Docker's own storage; they grant no host access.
var anonymousVolumeRe = regexp.MustCompile(`^[a-f0-9]{64}$`)

// CheckRuntime verifies a running container's real configuration (spec §10
// layer 3) and returns human-readable violations.
func CheckRuntime(f Facts, p RuntimePolicy) []string {
	var v []string
	add := func(format string, args ...any) { v = append(v, f.Name+": "+fmt.Sprintf(format, args...)) }
	if f.Privileged {
		add("privileged")
	}
	if len(f.CapAdd) > 0 {
		add("cap_add %v", f.CapAdd)
	}
	if !slices.ContainsFunc(f.CapDrop, func(c string) bool { return strings.EqualFold(c, "ALL") }) {
		add("cap_drop does not contain ALL")
	}
	if !slices.Contains(f.SecurityOpt, "no-new-privileges:true") && !slices.Contains(f.SecurityOpt, "no-new-privileges") {
		add("no-new-privileges not set")
	}
	for _, m := range []struct{ name, mode string }{
		{"network", f.NetworkMode}, {"pid", f.PidMode}, {"ipc", f.IpcMode}, {"uts", f.UTSMode}, {"userns", f.UsernsMode},
	} {
		if m.mode == "host" || strings.HasPrefix(m.mode, "container:") {
			add("%s mode %q", m.name, m.mode)
		}
	}
	if f.Runtime != "" && f.Runtime != "runc" {
		add("runtime %q", f.Runtime)
	}
	if f.Devices > 0 {
		add("devices mapped")
	}
	if f.Memory <= 0 {
		add("no memory limit")
	}
	if f.PidsLimit <= 0 {
		add("no pids limit")
	}
	prefix := filepath.Clean(p.DataDir) + string(filepath.Separator)
	for _, m := range f.Mounts {
		switch m.Type {
		case "bind":
			if !strings.HasPrefix(filepath.Clean(m.Source)+string(filepath.Separator), prefix) || filepath.Clean(m.Source) == filepath.Clean(p.DataDir) {
				add("bind mount %q outside %s", m.Source, prefix)
			}
		case "volume":
			if !anonymousVolumeRe.MatchString(m.Name) {
				add("named volume %q", m.Name)
			}
		case "tmpfs":
		default:
			add("mount type %q", m.Type)
		}
	}
	if f.Labels[names.LabelWorkspace] != p.Workspace || f.Labels[names.LabelApp] != p.App {
		add("owner labels do not match %s/%s", p.Workspace, p.App)
	}
	if !samePorts(f.PortBindings, p.Ports) {
		add("port bindings %v do not match expected %v", f.PortBindings, p.Ports)
	}
	return v
}

func samePorts(a, b []PortFact) bool {
	key := func(p PortFact) string { return fmt.Sprintf("%d/%s@%s:%d", p.ContainerPort, p.Protocol, p.HostIP, p.HostPort) }
	ka, kb := make([]string, len(a)), make([]string, len(b))
	for i, p := range a {
		ka[i] = key(p)
	}
	for i, p := range b {
		kb[i] = key(p)
	}
	slices.Sort(ka)
	slices.Sort(kb)
	return slices.Equal(ka, kb)
}
```

- [ ] **Step 5: Write `client.go`**

`internal/dockerx/client.go`:

```go
// Package dockerx wraps the Docker Engine API for the few read and cleanup
// operations the gateway needs. All container creation goes through compose.
package dockerx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/sebmahr/deploy-gateway/internal/names"
)

type ContainerInfo struct {
	ID     string
	Name   string
	Labels map[string]string
	State  string
	Status string
	Health string
}

type Client struct{ api *client.Client }

func New() (*Client, error) {
	c, err := client.New(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &Client{api: c}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	_, err := c.api.Ping(ctx, client.PingOptions{})
	return err
}

func (c *Client) ProjectContainers(ctx context.Context, ws, app string) ([]ContainerInfo, error) {
	f := make(client.Filters).Add("label", names.LabelWorkspace+"="+ws).Add("label", names.LabelApp+"="+app)
	return c.list(ctx, f)
}

func (c *Client) ManagedContainers(ctx context.Context) ([]ContainerInfo, error) {
	return c.list(ctx, make(client.Filters).Add("label", names.LabelManaged+"=true"))
}

func (c *Client) list(ctx context.Context, f client.Filters) ([]ContainerInfo, error) {
	res, err := c.api.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	out := make([]ContainerInfo, 0, len(res.Items))
	for _, s := range res.Items {
		info := ContainerInfo{ID: s.ID, Labels: s.Labels, State: string(s.State), Status: s.Status}
		if len(s.Names) > 0 {
			info.Name = strings.TrimPrefix(s.Names[0], "/")
		}
		if s.Health != nil {
			info.Health = string(s.Health.Status)
		}
		out = append(out, info)
	}
	return out, nil
}

func (c *Client) Inspect(ctx context.Context, id string) (Facts, error) {
	res, err := c.api.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return Facts{}, err
	}
	ir := res.Container
	f := Facts{Name: strings.TrimPrefix(ir.Name, "/")}
	if ir.Config != nil {
		f.Labels = ir.Config.Labels
	}
	if hc := ir.HostConfig; hc != nil {
		f.Privileged = hc.Privileged
		f.CapAdd, f.CapDrop, f.SecurityOpt = hc.CapAdd, hc.CapDrop, hc.SecurityOpt
		f.NetworkMode, f.PidMode, f.IpcMode = string(hc.NetworkMode), string(hc.PidMode), string(hc.IpcMode)
		f.UTSMode, f.UsernsMode, f.Runtime = string(hc.UTSMode), string(hc.UsernsMode), hc.Runtime
		f.Devices, f.Memory = len(hc.Devices), hc.Memory
		if hc.PidsLimit != nil {
			f.PidsLimit = *hc.PidsLimit
		}
		for port, binds := range hc.PortBindings {
			for _, b := range binds {
				hp, _ := strconv.Atoi(b.HostPort)
				ip := ""
				if b.HostIP.IsValid() {
					ip = b.HostIP.String()
				}
				f.PortBindings = append(f.PortBindings, PortFact{ContainerPort: int(port.Num()), Protocol: string(port.Proto()), HostIP: ip, HostPort: hp})
			}
		}
	}
	for _, m := range ir.Mounts {
		f.Mounts = append(f.Mounts, MountFact{Type: string(m.Type), Name: m.Name, Source: m.Source, Destination: m.Destination})
	}
	if ir.State != nil {
		f.State = string(ir.State.Status)
		if ir.State.Health != nil {
			f.Health = string(ir.State.Health.Status)
		}
	}
	return f, nil
}

var errCapped = errors.New("log output capped")

type capWriter struct {
	buf *bytes.Buffer
	max int
}

func (w *capWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); len(p) > room {
		w.buf.Write(p[:max(room, 0)])
		return 0, errCapped
	}
	return w.buf.Write(p)
}

func (c *Client) Logs(ctx context.Context, id string, tail int, since string, maxBytes int) (string, error) {
	rc, err := c.api.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true, ShowStderr: true, Tail: strconv.Itoa(tail), Since: since,
	})
	if err != nil {
		return "", err
	}
	defer rc.Close()
	var buf bytes.Buffer
	w := &capWriter{buf: &buf, max: maxBytes}
	if _, err := stdcopy.StdCopy(w, w, rc); err != nil && !errors.Is(err, errCapped) && !errors.Is(err, io.EOF) {
		return "", err
	}
	return buf.String(), nil
}

func (c *Client) NetworkExists(ctx context.Context, name string) (bool, error) {
	_, err := c.api.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err == nil {
		return true, nil
	}
	if cerrdefs.IsNotFound(err) {
		return false, nil
	}
	return false, err
}

func (c *Client) RemoveContainer(ctx context.Context, id string) error {
	_, err := c.api.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	return err
}

// MountSource reports where destination inside containerID comes from on
// the host. inContainer is false when containerID does not exist (the
// gateway is running directly on the host).
func (c *Client) MountSource(ctx context.Context, containerID, destination string) (string, bool, error) {
	res, err := c.api.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	for _, m := range res.Container.Mounts {
		if m.Destination == destination {
			return m.Source, true, nil
		}
	}
	return "", true, nil
}
```

- [ ] **Step 6: Tidy, vet and run the tests**

Run: `go mod tidy && go vet ./internal/dockerx/ && go test ./internal/dockerx/`
Expected: PASS. If `go vet` reports that `cerrdefs` is unresolved, run `go get github.com/containerd/errdefs` (the moby client already depends on it) and repeat.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/dockerx
git commit -m "feat(dockerx): docker client wrapper and runtime security checks"
```

---
### Task 14: Service core — types, errors, audit tracking, test fakes, Whoami/ListTemplates/Validate

**Files:**
- Create: `internal/service/service.go`, `internal/service/types.go`, `internal/service/errors.go`, `internal/service/track.go`, `internal/service/plan.go`, `internal/service/read.go` (Whoami, ListTemplates only in this task), `internal/service/deploy.go` (Validate only in this task), `internal/service/disk.go`, `internal/service/admin.go` (CreateWorkspace, SaveTemplate, SetGrants only in this task), `internal/service/servicetest/fakes.go`
- Test: `internal/service/harness_test.go`, `internal/service/validate_test.go`

**Interfaces:**
- Consumes: `names` (1), `stack` (2–3), `tmpl` (4), `render` (5), `verify` (6), `store` (9–10), `auth.GenerateToken` (11), `dockerx.ContainerInfo`, `dockerx.Facts`, `dockerx.PortFact`, `dockerx.RuntimePolicy`, `dockerx.CheckRuntime` (13)
- Produces:
  - `service.Compose` interface: `Up(ctx, file, project string) (string, error)`, `Down(ctx, file, project string) (string, error)`, `Restart(ctx, file, project, service string) (string, error)`, `Stop(ctx, file, project, service string) (string, error)`, `TryLock(key string) (func(), bool)` — satisfied by `*runner.Runner`
  - `service.Docker` interface: `ProjectContainers`, `ManagedContainers`, `Inspect`, `Logs`, `NetworkExists`, `RemoveContainer` (signatures as in Task 13) — satisfied by `*dockerx.Client`
  - `service.New(st *store.Store, c Compose, d Docker, root string) *Service`; fields `Chown func(path string, uid, gid int) error`, `Logger *slog.Logger`
  - `service.Principal{Workspace store.Workspace; TokenID int64; ClientIP string}`
  - `service.Error{Code, Message string; Fields stack.Errors}` (+ `Detail() string`), codes `CodeUnauthorized`, `CodeValidation`, `CodeQuota`, `CodePortTaken`, `CodeNotFound`, `CodeConflict`, `CodeDeployFailed`, `CodeSecurity`, `CodeInternal`
  - Inputs: `NoInput{}`, `DeployInput{App string; Stack stack.Stack}`, `ServiceInput{App, Service string}`, `LogsInput{App, Service string; Tail int; Since string}`, `DeleteInput{App, Confirm string}`
  - Outputs: `WhoamiResult`, `TemplatesResult{Templates []TemplateView}`, `ValidateResult{App string; Services []ServicePlan; TotalMemoryMB int64; TotalCPUs float64}`, `AppStatus{App string; Services []ServiceStatus}`, `ListResult{Apps []AppView}`, `LogsResult{App, Service, Logs string}`, `DeleteResult{App string; Deleted bool}`
  - `(*Service).Whoami(ctx, Principal) (WhoamiResult, error)`, `ListTemplates(ctx, Principal) (TemplatesResult, error)`, `Validate(ctx, Principal, DeployInput) (ValidateResult, error)`
  - Admin (this task): `service.PortSettings{Start, End int; BindIP string}`, `CreateWorkspace(ctx, actor, name string, q store.Quota, ps PortSettings) (store.Workspace, error)`, `SaveTemplate(ctx, actor string, id int64, t tmpl.Template) (int64, error)`, `SetGrants(ctx, actor string, w store.Workspace, templateIDs []int64) error`
  - `servicetest.FakeCompose`, `servicetest.NewFakeDocker() *FakeDocker`, `servicetest.Wire(c *FakeCompose, d *FakeDocker)`

- [ ] **Step 1: Write the test fakes**

`internal/service/servicetest/fakes.go`:

```go
// Package servicetest provides in-memory Compose and Docker fakes. Wire
// connects them so that a fake `compose up` creates containers whose facts
// mirror the rendered compose file, which lets service tests exercise the
// full deploy pipeline including the runtime check.
package servicetest

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/sebmahr/deploy-gateway/internal/dockerx"
	"github.com/sebmahr/deploy-gateway/internal/names"
)

type FakeCompose struct {
	mu        sync.Mutex
	calls     []string
	locks     map[string]bool
	UpErr     error
	UpOut     string
	OnUp      func(file, project string) error
	OnDown    func(project string)
	OnStop    func(project, service string)
	OnRestart func(project, service string)
	AfterUp   func()
}

func (f *FakeCompose) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.TrimSpace(s))
}

func (f *FakeCompose) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *FakeCompose) Up(_ context.Context, file, project string) (string, error) {
	f.record("up " + project + " " + file)
	if f.UpErr != nil {
		return f.UpOut, f.UpErr
	}
	if f.OnUp != nil {
		if err := f.OnUp(file, project); err != nil {
			return "", err
		}
	}
	if f.AfterUp != nil {
		f.AfterUp()
	}
	return f.UpOut, nil
}

func (f *FakeCompose) Down(_ context.Context, _, project string) (string, error) {
	f.record("down " + project)
	if f.OnDown != nil {
		f.OnDown(project)
	}
	return "", nil
}

func (f *FakeCompose) Restart(_ context.Context, _, project, service string) (string, error) {
	f.record("restart " + project + " " + service)
	if f.OnRestart != nil {
		f.OnRestart(project, service)
	}
	return "", nil
}

func (f *FakeCompose) Stop(_ context.Context, _, project, service string) (string, error) {
	f.record("stop " + project + " " + service)
	if f.OnStop != nil {
		f.OnStop(project, service)
	}
	return "", nil
}

func (f *FakeCompose) TryLock(key string) (func(), bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.locks == nil {
		f.locks = map[string]bool{}
	}
	if f.locks[key] {
		return nil, false
	}
	f.locks[key] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			delete(f.locks, key)
			f.mu.Unlock()
		})
	}, true
}

type FakeDocker struct {
	mu         sync.Mutex
	Containers map[string]dockerx.ContainerInfo
	Facts      map[string]dockerx.Facts
	Networks   map[string]bool
	LogsOut    string
	touched    []string
}

func NewFakeDocker() *FakeDocker {
	return &FakeDocker{Containers: map[string]dockerx.ContainerInfo{}, Facts: map[string]dockerx.Facts{}, Networks: map[string]bool{}}
}

// Touched lists container IDs passed to Inspect, Logs or RemoveContainer.
func (d *FakeDocker) Touched() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.touched)
}

func (d *FakeDocker) Add(info dockerx.ContainerInfo, facts dockerx.Facts) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Containers[info.ID] = info
	d.Facts[info.ID] = facts
}

func (d *FakeDocker) MutateFacts(id string, f func(*dockerx.Facts)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	facts := d.Facts[id]
	f(&facts)
	d.Facts[id] = facts
}

func (d *FakeDocker) filter(match func(map[string]string) bool) []dockerx.ContainerInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := []dockerx.ContainerInfo{}
	for _, c := range d.Containers {
		if match(c.Labels) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b dockerx.ContainerInfo) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (d *FakeDocker) ProjectContainers(_ context.Context, ws, app string) ([]dockerx.ContainerInfo, error) {
	return d.filter(func(l map[string]string) bool { return l[names.LabelWorkspace] == ws && l[names.LabelApp] == app }), nil
}

func (d *FakeDocker) ManagedContainers(_ context.Context) ([]dockerx.ContainerInfo, error) {
	return d.filter(func(l map[string]string) bool { return l[names.LabelManaged] == "true" }), nil
}

func (d *FakeDocker) Inspect(_ context.Context, id string) (dockerx.Facts, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.touched = append(d.touched, id)
	f, ok := d.Facts[id]
	if !ok {
		return dockerx.Facts{}, errors.New("no such container: " + id)
	}
	return f, nil
}

func (d *FakeDocker) Logs(_ context.Context, id string, _ int, _ string, _ int) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.touched = append(d.touched, id)
	return d.LogsOut, nil
}

func (d *FakeDocker) NetworkExists(_ context.Context, name string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.Networks[name], nil
}

func (d *FakeDocker) RemoveContainer(_ context.Context, id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.touched = append(d.touched, id)
	delete(d.Containers, id)
	delete(d.Facts, id)
	return nil
}

type renderedFile struct {
	Services map[string]struct {
		Labels      map[string]string `yaml:"labels"`
		MemLimit    int64             `yaml:"mem_limit"`
		PidsLimit   int64             `yaml:"pids_limit"`
		Healthcheck map[string]any    `yaml:"healthcheck"`
		Networks    []string          `yaml:"networks"`
		Volumes     []struct {
			Type   string `yaml:"type"`
			Source string `yaml:"source"`
			Target string `yaml:"target"`
		} `yaml:"volumes"`
		Ports []struct {
			Target    int    `yaml:"target"`
			Published string `yaml:"published"`
			HostIP    string `yaml:"host_ip"`
			Protocol  string `yaml:"protocol"`
		} `yaml:"ports"`
	} `yaml:"services"`
}

// projectParts splits "dg-<ws>-<app>" (workspace names contain no hyphen).
func projectParts(project string) (ws, app string) {
	rest := strings.TrimPrefix(project, "dg-")
	i := strings.IndexByte(rest, '-')
	return rest[:i], rest[i+1:]
}

// Wire makes compose operations act on the fake Docker state.
func Wire(c *FakeCompose, d *FakeDocker) {
	c.OnUp = func(file, project string) error {
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var doc renderedFile
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return err
		}
		ws, app := projectParts(project)
		d.mu.Lock()
		defer d.mu.Unlock()
		for id, ci := range d.Containers { // --remove-orphans
			if ci.Labels[names.LabelWorkspace] == ws && ci.Labels[names.LabelApp] == app {
				if _, keep := doc.Services[id]; !keep {
					delete(d.Containers, id)
					delete(d.Facts, id)
				}
			}
		}
		for key, s := range doc.Services {
			health := ""
			if s.Healthcheck != nil {
				health = "healthy"
			}
			f := dockerx.Facts{
				Name: key, Labels: s.Labels, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"},
				NetworkMode: s.Networks[0], IpcMode: "private", Memory: s.MemLimit, PidsLimit: s.PidsLimit,
				State: "running", Health: health,
			}
			for _, v := range s.Volumes {
				f.Mounts = append(f.Mounts, dockerx.MountFact{Type: v.Type, Source: v.Source, Destination: v.Target})
			}
			for _, p := range s.Ports {
				hp, _ := strconv.Atoi(p.Published)
				f.PortBindings = append(f.PortBindings, dockerx.PortFact{ContainerPort: p.Target, Protocol: p.Protocol, HostIP: p.HostIP, HostPort: hp})
			}
			d.Containers[key] = dockerx.ContainerInfo{ID: key, Name: key, Labels: s.Labels, State: "running", Status: "Up", Health: health}
			d.Facts[key] = f
		}
		return nil
	}
	setState := func(project, service, state string) {
		ws, app := projectParts(project)
		d.mu.Lock()
		defer d.mu.Unlock()
		for id, ci := range d.Containers {
			if ci.Labels[names.LabelWorkspace] == ws && ci.Labels[names.LabelApp] == app && (service == "" || id == service) {
				ci.State = state
				d.Containers[id] = ci
			}
		}
	}
	c.OnStop = func(project, service string) { setState(project, service, "exited") }
	c.OnRestart = func(project, service string) { setState(project, service, "running") }
	c.OnDown = func(project string) {
		ws, app := projectParts(project)
		d.mu.Lock()
		defer d.mu.Unlock()
		for id, ci := range d.Containers {
			if ci.Labels[names.LabelWorkspace] == ws && ci.Labels[names.LabelApp] == app {
				delete(d.Containers, id)
				delete(d.Facts, id)
			}
		}
	}
}
```

- [ ] **Step 2: Write the test harness and failing tests**

`internal/service/harness_test.go`:

```go
package service_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/service/servicetest"
	"github.com/sebmahr/deploy-gateway/internal/stack"
	"github.com/sebmahr/deploy-gateway/internal/store"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
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
```

`internal/service/validate_test.go`:

```go
package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

func TestWhoami(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	res, err := h.svc.Whoami(h.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if res.Workspace != "alice" || res.PortRange != "20000-20099" || len(res.Guidance) == 0 || res.PortsInUse == nil {
		t.Fatalf("whoami = %+v", res)
	}
	entries, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Action: "whoami"})
	if len(entries) != 1 || entries[0].Workspace != "alice" || entries[0].ClientIP != "203.0.113.1" {
		t.Fatalf("audit = %+v", entries)
	}
}

func TestListTemplates(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 0, 0)
	res, err := h.svc.ListTemplates(h.ctx, p)
	if err != nil || len(res.Templates) != 1 || res.Templates[0].Name != "public-web" || res.Templates[0].Shareable {
		t.Fatalf("templates = %+v, %v", res, err)
	}
}

func TestValidateReturnsPlanWithoutSideEffects(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	res, err := h.svc.Validate(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Services) != 2 || res.TotalMemoryMB != 512 || res.TotalCPUs != 1 {
		t.Fatalf("plan = %+v", res)
	}
	web := res.Services[1]
	if web.Service != "web" || web.URL != "https://shop.alice.apps.example.com" || web.InternalHost != "alice-shop-web" ||
		len(web.Ports) != 1 || web.Ports[0].HostPort != 20001 {
		t.Fatalf("web plan = %+v", web)
	}
	if calls := h.compose.Calls(); len(calls) != 0 {
		t.Fatalf("validate must not run compose: %v", calls)
	}
	if _, err := os.Stat(filepath.Join(h.root, "alice", "shop")); !os.IsNotExist(err) {
		t.Fatal("validate must not create the app folder")
	}
}

func TestValidateErrors(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	_, err := h.svc.Validate(h.ctx, p, service.DeployInput{App: "Bad_Name", Stack: mustStack(t, exampleStack)})
	if code(err) != service.CodeValidation || !strings.Contains(err.Error(), `"field":"app"`) {
		t.Fatalf("bad app name: %v", err)
	}
	_, err = h.svc.Validate(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, `{"services": {"web": {"image": "nginx"}}}`)})
	if code(err) != service.CodeValidation || !strings.Contains(err.Error(), "services.web.image") {
		t.Fatalf("invalid stack: %v", err)
	}
	q := store.BuiltinDefaultQuota()
	q.MaxTotalMemoryMB, q.MaxMemoryMB, q.DefaultMemoryMB = 300, 256, 256
	small := h.workspaceWithQuota("small", 0, 0, q)
	_, err = h.svc.Validate(h.ctx, small, service.DeployInput{App: "shop", Stack: mustStack(t,
		`{"services": {"a": {"image": "a:1"}, "b": {"image": "b:1"}}}`)})
	if code(err) != service.CodeQuota || !strings.Contains(err.Error(), "total memory") {
		t.Fatalf("quota: %v", err)
	}
}

func TestAuditRedactsEnvironmentValues(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	if _, err := h.svc.Validate(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)}); err != nil {
		t.Fatal(err)
	}
	entries, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Action: "validate"})
	if len(entries) != 1 {
		t.Fatalf("audit = %+v", entries)
	}
	if strings.Contains(entries[0].Params, "s3cr3t-value") || !strings.Contains(entries[0].Params, `"SECRET_KEY":"***"`) {
		t.Fatalf("env not redacted: %s", entries[0].Params)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/service/...`
Expected: FAIL — `undefined: service.New` (and others).

- [ ] **Step 4: Write `service.go`, `types.go`, `errors.go`**

`internal/service/service.go`:

```go
// Package service orchestrates every agent and admin operation. It is the
// only package that combines validation, rendering, verification, compose
// execution, runtime checks, persistence and auditing.
package service

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/dockerx"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

type Compose interface {
	Up(ctx context.Context, file, project string) (string, error)
	Down(ctx context.Context, file, project string) (string, error)
	Restart(ctx context.Context, file, project, service string) (string, error)
	Stop(ctx context.Context, file, project, service string) (string, error)
	TryLock(key string) (func(), bool)
}

type Docker interface {
	ProjectContainers(ctx context.Context, ws, app string) ([]dockerx.ContainerInfo, error)
	ManagedContainers(ctx context.Context) ([]dockerx.ContainerInfo, error)
	Inspect(ctx context.Context, id string) (dockerx.Facts, error)
	Logs(ctx context.Context, id string, tail int, since string, maxBytes int) (string, error)
	NetworkExists(ctx context.Context, name string) (bool, error)
	RemoveContainer(ctx context.Context, id string) error
}

// Principal is the authenticated caller. Workspace comes from the token
// lookup only — never from agent input.
type Principal struct {
	Workspace store.Workspace
	TokenID   int64
	ClientIP  string
}

type Service struct {
	Store   *store.Store
	Compose Compose
	Docker  Docker
	Root    string
	Chown   func(path string, uid, gid int) error
	Logger  *slog.Logger

	wsLocks sync.Map // workspace name -> *sync.Mutex
	disk    *diskCache
}

func New(st *store.Store, c Compose, d Docker, root string) *Service {
	return &Service{Store: st, Compose: c, Docker: d, Root: root, Chown: os.Chown,
		Logger: slog.Default(), disk: newDiskCache(10 * time.Minute)}
}

func (s *Service) workspaceDir(ws string) string    { return filepath.Join(s.Root, ws) }
func (s *Service) appDir(ws, app string) string      { return filepath.Join(s.Root, ws, app) }
func (s *Service) dataDir(ws, app string) string     { return filepath.Join(s.Root, ws, app, "data") }
func (s *Service) composeFile(ws, app string) string { return filepath.Join(s.appDir(ws, app), "docker-compose.yml") }
func (s *Service) prevFile(ws, app string) string    { return filepath.Join(s.appDir(ws, app), "docker-compose.prev.yml") }
func lockKey(ws, app string) string                  { return ws + "/" + app }

// wsMutex serialises quota checks and state changes within a workspace.
func (s *Service) wsMutex(ws string) *sync.Mutex {
	m, _ := s.wsLocks.LoadOrStore(ws, &sync.Mutex{})
	return m.(*sync.Mutex)
}
```

`internal/service/types.go`:

```go
package service

import (
	"github.com/sebmahr/deploy-gateway/internal/stack"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

type NoInput struct{}

type DeployInput struct {
	App   string      `json:"app" jsonschema:"app name, ^[a-z][a-z0-9-]{0,22}[a-z0-9]$"`
	Stack stack.Stack `json:"stack" jsonschema:"strict compose subset describing the app's services"`
}

type ServiceInput struct {
	App     string `json:"app" jsonschema:"app name"`
	Service string `json:"service,omitempty" jsonschema:"short service name; empty means all services"`
}

type LogsInput struct {
	App     string `json:"app" jsonschema:"app name"`
	Service string `json:"service" jsonschema:"short service name"`
	Tail    int    `json:"tail,omitempty" jsonschema:"number of lines, default 200, max 1000"`
	Since   string `json:"since,omitempty" jsonschema:"Go duration like 10m or an RFC3339 timestamp"`
}

type DeleteInput struct {
	App     string `json:"app" jsonschema:"app name"`
	Confirm string `json:"confirm" jsonschema:"must equal the app name"`
}

type Usage struct {
	Apps     int     `json:"apps"`
	MemoryMB int64   `json:"memory_mb"`
	CPUs     float64 `json:"cpus"`
}

type WhoamiResult struct {
	Workspace  string            `json:"workspace"`
	Quota      store.Quota       `json:"quota"`
	Usage      Usage             `json:"usage"`
	PortRange  string            `json:"port_range" jsonschema:"host port range for published ports, empty if publishing is disabled"`
	PortBindIP string            `json:"port_bind_ip"`
	PortsInUse []store.PortClaim `json:"ports_in_use"`
	Naming     string            `json:"naming"`
	Guidance   []string          `json:"guidance"`
}

type TemplateView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	HostPattern string `json:"host_pattern"`
	Shareable   bool   `json:"shareable" jsonschema:"true if several services of one app may use this template"`
}

type TemplatesResult struct {
	Templates []TemplateView `json:"templates"`
}

type ServicePlan struct {
	Service      string            `json:"service"`
	InternalHost string            `json:"internal_host"`
	URL          string            `json:"url,omitempty"`
	Ports        []store.PortClaim `json:"ports"`
	MemoryMB     int64             `json:"memory_mb"`
	CPUs         float64           `json:"cpus"`
}

type ValidateResult struct {
	App           string        `json:"app"`
	Services      []ServicePlan `json:"services"`
	TotalMemoryMB int64         `json:"total_memory_mb"`
	TotalCPUs     float64       `json:"total_cpus"`
}

type ServiceStatus struct {
	Service      string            `json:"service"`
	InternalHost string            `json:"internal_host"`
	URL          string            `json:"url,omitempty"`
	State        string            `json:"state"`
	Health       string            `json:"health,omitempty"`
	Ports        []store.PortClaim `json:"ports"`
}

type AppStatus struct {
	App      string          `json:"app"`
	Services []ServiceStatus `json:"services"`
}

type AppView struct {
	App              string          `json:"app"`
	Services         []ServiceStatus `json:"services"`
	DiskBytes        int64           `json:"disk_bytes"`
	LastDeployAt     string          `json:"last_deploy_at,omitempty"`
	LastDeployResult string          `json:"last_deploy_result"`
}

type ListResult struct {
	Apps []AppView `json:"apps"`
}

type LogsResult struct {
	App     string `json:"app"`
	Service string `json:"service"`
	Logs    string `json:"logs"`
}

type DeleteResult struct {
	App     string `json:"app"`
	Deleted bool   `json:"deleted"`
}
```

`internal/service/errors.go`:

```go
package service

import (
	"encoding/json"

	"github.com/sebmahr/deploy-gateway/internal/stack"
)

const (
	CodeUnauthorized = "unauthorized"
	CodeValidation   = "validation_failed"
	CodeQuota        = "quota_exceeded"
	CodePortTaken    = "port_taken"
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
	CodeDeployFailed = "deploy_failed"
	CodeSecurity     = "security_violation"
	CodeInternal     = "internal"
)

// Error is the structured error returned to agents. detail is recorded in
// the audit log and shown to admins, never to agents.
type Error struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Fields  stack.Errors `json:"errors,omitempty"`

	detail   string
	critical bool
}

func (e *Error) Error() string {
	b, _ := json.Marshal(e)
	return string(b)
}

func (e *Error) Detail() string { return e.detail }

const appRule = "must match ^[a-z][a-z0-9-]{0,22}[a-z0-9]$"

func validationError(field, msg string) *Error {
	return &Error{Code: CodeValidation, Message: msg, Fields: stack.Errors{{Field: field, Message: msg}}}
}

func notFoundError(what string) *Error {
	return &Error{Code: CodeNotFound, Message: what + " not found"}
}
```

- [ ] **Step 5: Write `track.go` and `disk.go`**

`internal/service/track.go`:

```go
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/store"
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
```

`internal/service/disk.go`:

```go
package service

import (
	"io/fs"
	"path/filepath"
	"sync"
	"time"
)

type diskEntry struct {
	bytes int64
	at    time.Time
}

// diskCache measures directory sizes at most once per ttl.
type diskCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]diskEntry
}

func newDiskCache(ttl time.Duration) *diskCache {
	return &diskCache{ttl: ttl, m: map[string]diskEntry{}}
}

func (d *diskCache) usage(dir string) int64 {
	d.mu.Lock()
	e, ok := d.m[dir]
	d.mu.Unlock()
	if ok && time.Since(e.at) < d.ttl {
		return e.bytes
	}
	var total int64
	filepath.WalkDir(dir, func(_ string, de fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if de.Type().IsRegular() {
			if info, err := de.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	d.mu.Lock()
	d.m[dir] = diskEntry{bytes: total, at: time.Now()}
	d.mu.Unlock()
	return total
}
```

- [ ] **Step 6: Write `plan.go`**

`internal/service/plan.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sebmahr/deploy-gateway/internal/names"
	"github.com/sebmahr/deploy-gateway/internal/render"
	"github.com/sebmahr/deploy-gateway/internal/stack"
	"github.com/sebmahr/deploy-gateway/internal/store"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
	"github.com/sebmahr/deploy-gateway/internal/verify"
)

type planned struct {
	app      string
	stack    *stack.Stack
	out      render.Output
	policy   verify.Policy
	defaults stack.Defaults
	memBytes int64
	cpus     float64
	claims   []store.PortClaim
}

// plan runs validation, quota checks, rendering and verification. It has no
// side effects.
func (s *Service) plan(ctx context.Context, ws store.Workspace, app string, st *stack.Stack) (*planned, error) {
	if !names.ValidApp(app) {
		return nil, validationError("app", appRule)
	}
	granted, err := s.Store.GrantedTemplates(ctx, ws.ID)
	if err != nil {
		return nil, err
	}
	templates := map[string]tmpl.Template{}
	grants := stack.Grants{}
	for _, t := range granted {
		templates[t.Name] = t.Template
		grants[t.Name] = t.Template.Shareable()
	}
	q := ws.Quota
	lim := stack.Limits{
		MaxServices: q.MaxServicesPerApp, MaxMemoryBytes: q.MaxMemoryMB << 20, MaxCPUs: q.MaxCPUs, MaxPids: q.MaxPids,
		PortRangeStart: ws.PortRangeStart, PortRangeEnd: ws.PortRangeEnd,
	}
	if errs := stack.Validate(st, lim, grants); errs != nil {
		return nil, &Error{Code: CodeValidation, Message: "stack validation failed", Fields: errs}
	}
	defaults := stack.Defaults{MemoryBytes: q.DefaultMemoryMB << 20, CPUs: q.DefaultCPUs, Pids: q.DefaultPids}
	mem, cpus := stack.Totals(st, defaults)
	usage, err := s.Store.WorkspaceUsage(ctx, ws.ID, app)
	if err != nil {
		return nil, err
	}
	_, err = s.Store.GetApp(ctx, ws.ID, app)
	isNew := errors.Is(err, store.ErrNotFound)
	if err != nil && !isNew {
		return nil, err
	}
	var qerrs []string
	if isNew && usage.Apps+1 > q.MaxApps {
		qerrs = append(qerrs, fmt.Sprintf("workspace app limit of %d reached", q.MaxApps))
	}
	if usage.MemoryBytes+mem > q.MaxTotalMemoryMB<<20 {
		qerrs = append(qerrs, fmt.Sprintf("total memory would be %dm, limit is %dm", (usage.MemoryBytes+mem)>>20, q.MaxTotalMemoryMB))
	}
	if usage.CPUs+cpus > q.MaxTotalCPUs+1e-9 {
		qerrs = append(qerrs, fmt.Sprintf("total cpus would be %.2f, limit is %.2f", usage.CPUs+cpus, q.MaxTotalCPUs))
	}
	if len(qerrs) > 0 {
		return nil, &Error{Code: CodeQuota, Message: strings.Join(qerrs, "; ")}
	}
	dataDir := s.dataDir(ws.Name, app)
	out, err := render.Render(render.Input{
		Workspace: ws.Name, App: app, Stack: st, Templates: templates,
		Defaults: defaults, DataDir: dataDir, BindIP: ws.PortBindIP,
	})
	if err != nil {
		return nil, err
	}
	nets := map[string]bool{names.Network(ws.Name, app): true}
	for _, t := range templates {
		for _, n := range t.Networks {
			nets[n] = true
		}
	}
	pol := verify.Policy{
		Workspace: ws.Name, App: app, DataDir: dataDir, Networks: nets,
		MaxMemoryBytes: lim.MaxMemoryBytes, MaxCPUs: q.MaxCPUs, MaxPids: q.MaxPids,
		PortRangeStart: ws.PortRangeStart, PortRangeEnd: ws.PortRangeEnd, BindIP: ws.PortBindIP,
	}
	if err := verify.Verify(out.YAML, pol); err != nil {
		return nil, &Error{Code: CodeSecurity, Message: "rendered configuration failed verification", detail: err.Error(), critical: true}
	}
	return &planned{app: app, stack: st, out: out, policy: pol, defaults: defaults, memBytes: mem, cpus: cpus,
		claims: claimsFrom(app, out.Ports)}, nil
}

func claimsFrom(app string, ports []render.PublishedPort) []store.PortClaim {
	out := []store.PortClaim{}
	for _, p := range ports {
		out = append(out, store.PortClaim{Protocol: p.Protocol, HostPort: p.Host, ContainerPort: p.Container, Service: p.Service, App: app})
	}
	return out
}

func portsFor(claims []store.PortClaim, svc string) []store.PortClaim {
	out := []store.PortClaim{}
	for _, c := range claims {
		if c.Service == svc {
			out = append(out, c)
		}
	}
	return out
}

func hostURL(h string) string {
	if h == "" {
		return ""
	}
	return "https://" + h
}

func (pl *planned) servicePlans(ws string) []ServicePlan {
	out := []ServicePlan{}
	for _, svc := range stack.ServiceNames(pl.stack) {
		r := stack.Effective(pl.stack.Services[svc], pl.defaults)
		out = append(out, ServicePlan{
			Service: svc, InternalHost: names.Container(ws, pl.app, svc), URL: hostURL(pl.out.Hosts[svc]),
			Ports: portsFor(pl.claims, svc), MemoryMB: r.MemoryBytes >> 20, CPUs: r.CPUs,
		})
	}
	return out
}

func (s *Service) checkPortConflicts(ctx context.Context, ws store.Workspace, app string, claims []store.PortClaim) error {
	conflicts, err := s.Store.PortConflicts(ctx, ws.ID, app, claims)
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		return &Error{Code: CodePortTaken, Message: (&store.PortTakenError{Claim: conflicts[0]}).Error()}
	}
	return nil
}
```

- [ ] **Step 7: Write `read.go` (Whoami, ListTemplates) and `deploy.go` (Validate)**

`internal/service/read.go`:

```go
package service

import (
	"context"
	"fmt"
	"time"
)

const namingDoc = "Containers, compose services and DNS names are <workspace>-<app>-<service>. " +
	"Services of one app reach each other only by that full name on the app's private network " +
	"(e.g. postgres://alice-shop-db:5432). There are no short aliases."

var guidance = []string{
	"Images must be pinned (explicit tag or digest); :latest is rejected. Build and push images in your own CI; the server pulls them on every deploy.",
	`Containers run with all capabilities dropped and no-new-privileges. Images whose entrypoint starts as root and switches user (official postgres, redis, ...) need "user": "<uid>:<gid>" (e.g. "999:999"); the volume directory is then owned by that uid.`,
	"The root filesystem is read-only and /tmp is a tmpfs. Set writable_rootfs: true if the image must write elsewhere, or use a volume.",
	"Use validate before deploy to see hostnames, URLs, published ports and resource totals without starting anything.",
	"Environment values are passed literally; $ is never interpolated.",
}

func (s *Service) Whoami(ctx context.Context, p Principal) (res WhoamiResult, err error) {
	defer s.track(ctx, p, "whoami", nil, &err, time.Now())
	ws := p.Workspace
	u, err := s.Store.WorkspaceUsage(ctx, ws.ID, "")
	if err != nil {
		return WhoamiResult{}, err
	}
	claims, err := s.Store.ListPortClaims(ctx, ws.ID)
	if err != nil {
		return WhoamiResult{}, err
	}
	pr := ""
	if ws.PortRangeStart > 0 {
		pr = fmt.Sprintf("%d-%d", ws.PortRangeStart, ws.PortRangeEnd)
	}
	return WhoamiResult{
		Workspace: ws.Name, Quota: ws.Quota,
		Usage:     Usage{Apps: u.Apps, MemoryMB: u.MemoryBytes >> 20, CPUs: u.CPUs},
		PortRange: pr, PortBindIP: ws.PortBindIP, PortsInUse: claims,
		Naming: namingDoc, Guidance: guidance,
	}, nil
}

func (s *Service) ListTemplates(ctx context.Context, p Principal) (res TemplatesResult, err error) {
	defer s.track(ctx, p, "list_templates", nil, &err, time.Now())
	granted, err := s.Store.GrantedTemplates(ctx, p.Workspace.ID)
	if err != nil {
		return TemplatesResult{}, err
	}
	res.Templates = []TemplateView{}
	for _, t := range granted {
		res.Templates = append(res.Templates, TemplateView{Name: t.Name, Description: t.Description, HostPattern: t.HostPattern, Shareable: t.Template.Shareable()})
	}
	return res, nil
}
```

`internal/service/deploy.go`:

```go
package service

import (
	"context"
	"time"
)

func (s *Service) Validate(ctx context.Context, p Principal, in DeployInput) (res ValidateResult, err error) {
	defer s.track(ctx, p, "validate", in, &err, time.Now())
	pl, err := s.plan(ctx, p.Workspace, in.App, &in.Stack)
	if err != nil {
		return ValidateResult{}, err
	}
	if err := s.checkPortConflicts(ctx, p.Workspace, in.App, pl.claims); err != nil {
		return ValidateResult{}, err
	}
	return ValidateResult{App: in.App, Services: pl.servicePlans(p.Workspace.Name), TotalMemoryMB: pl.memBytes >> 20, TotalCPUs: pl.cpus}, nil
}
```

- [ ] **Step 8: Write `admin.go` (workspace creation, templates, grants)**

`internal/service/admin.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/names"
	"github.com/sebmahr/deploy-gateway/internal/store"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
)

type PortSettings struct {
	Start  int    `json:"start"`
	End    int    `json:"end"`
	BindIP string `json:"bind_ip"`
}

func (s *Service) validatePortSettings(ctx context.Context, ps PortSettings, excludeID int64) error {
	if !(ps.Start == 0 && ps.End == 0) && (ps.Start < 1024 || ps.End > 65535 || ps.Start > ps.End) {
		return validationError("ports", "port range must be within 1024-65535 with start <= end (or 0-0 to disable publishing)")
	}
	if _, err := netip.ParseAddr(ps.BindIP); err != nil {
		return validationError("bind_ip", "bind IP must be an IP address")
	}
	if ps.Start > 0 {
		name, err := s.Store.OverlappingWorkspace(ctx, ps.Start, ps.End, excludeID)
		if err != nil {
			return err
		}
		if name != "" {
			return validationError("ports", fmt.Sprintf("port range overlaps workspace %q", name))
		}
	}
	return nil
}

func (s *Service) CreateWorkspace(ctx context.Context, actor, name string, q store.Quota, ps PortSettings) (w store.Workspace, err error) {
	defer s.adminTrack(ctx, actor, name, "create_workspace", map[string]any{"quota": q, "ports": ps}, &err, time.Now())
	if !names.ValidWorkspace(name) {
		return w, validationError("name", "workspace name must match ^[a-z][a-z0-9]{1,15}$")
	}
	if err = q.Validate(); err != nil {
		return w, validationError("quota", err.Error())
	}
	if err = s.validatePortSettings(ctx, ps, 0); err != nil {
		return w, err
	}
	if err = os.MkdirAll(s.workspaceDir(name), 0o750); err != nil {
		return w, err
	}
	w, err = s.Store.CreateWorkspace(ctx, store.Workspace{Name: name, Quota: q, PortRangeStart: ps.Start, PortRangeEnd: ps.End, PortBindIP: ps.BindIP})
	if errors.Is(err, store.ErrExists) {
		return w, validationError("name", "workspace already exists")
	}
	return w, err
}

// SaveTemplate creates (id == 0) or updates a template after structural
// validation, hostname-uniqueness validation and a Docker network check.
func (s *Service) SaveTemplate(ctx context.Context, actor string, id int64, t tmpl.Template) (newID int64, err error) {
	defer s.adminTrack(ctx, actor, "", "save_template", t, &err, time.Now())
	all, err := s.Store.ListTemplates(ctx)
	if err != nil {
		return 0, err
	}
	var others []tmpl.Template
	for _, o := range all {
		if o.ID != id {
			others = append(others, o.Template)
		}
	}
	if err = tmpl.Validate(t, others); err != nil {
		return 0, &Error{Code: CodeValidation, Message: err.Error()}
	}
	for _, n := range t.Networks {
		ok, err := s.Docker.NetworkExists(ctx, n)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, validationError("networks", fmt.Sprintf("network %q does not exist in Docker", n))
		}
	}
	if id == 0 {
		newID, err = s.Store.CreateTemplate(ctx, t)
	} else {
		newID, err = id, s.Store.UpdateTemplate(ctx, id, t)
	}
	if errors.Is(err, store.ErrExists) {
		return 0, validationError("name", "a template with this name already exists")
	}
	return newID, err
}

func (s *Service) SetGrants(ctx context.Context, actor string, w store.Workspace, templateIDs []int64) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "set_grants", templateIDs, &err, time.Now())
	return s.Store.SetGrants(ctx, w.ID, templateIDs)
}
```

- [ ] **Step 9: Run tests to verify they pass**

Run: `go test ./internal/service/...`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/service
git commit -m "feat(service): core types, audit tracking, plan pipeline, validate"
```

---

### Task 15: Service — Deploy

**Files:**
- Modify: `internal/service/deploy.go` (add Deploy and helpers)
- Test: `internal/service/deploy_test.go`

**Interfaces:**
- Consumes: Task 14 (`plan`, `planned`, `track`, `sanitize`, `checkPortConflicts`, `portsFor`, `hostURL`, path helpers), `store.SaveDeployment`, `store.RecordDeploy`, `store.PortTakenError` (Task 10), `dockerx.CheckRuntime` (Task 13)
- Produces:
  - `(*Service).Deploy(ctx, Principal, DeployInput) (AppStatus, error)`
  - internal helpers used by Task 16: `(*Service).status(ctx, ws store.Workspace, app string, hosts map[string]string, claims []store.PortClaim, services []string) (AppStatus, error)`, `(*Service).failureLogs(ctx, ws, app string) string`

- [ ] **Step 1: Write the failing tests**

`internal/service/deploy_test.go`:

```go
package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sebmahr/deploy-gateway/internal/dockerx"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

func TestDeployHappyPath(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	res, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(h.root, "alice", "shop", "docker-compose.yml")
	if calls := h.compose.Calls(); len(calls) != 1 || calls[0] != "up dg-alice-shop "+file {
		t.Fatalf("compose calls = %v", calls)
	}
	data, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(data), "cap_drop") {
		t.Fatalf("compose file: %v\n%s", err, data)
	}
	if _, err := os.Stat(filepath.Join(h.root, "alice", "shop", "data", "db")); err != nil {
		t.Fatalf("volume dir missing: %v", err)
	}
	if !slices.Equal(h.chowns, []string{"db 999:999"}) {
		t.Fatalf("chowns = %v", h.chowns)
	}
	if len(res.Services) != 2 || res.Services[0].Service != "db" || res.Services[0].State != "running" ||
		res.Services[1].URL != "https://shop.alice.apps.example.com" || len(res.Services[1].Ports) != 1 {
		t.Fatalf("result = %+v", res)
	}
	app, _ := h.st.GetApp(h.ctx, p.Workspace.ID, "shop")
	if app.LastDeployResult != "ok" {
		t.Fatalf("app = %+v", app)
	}
}

func TestDeployRedeployReleasesRemovedServicePorts(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	two := `{"services": {"web": {"image": "a:1", "ports": [{"container": 80, "host": 20001}]}, "worker": {"image": "b:1", "ports": [{"container": 81, "host": 20002}]}}}`
	one := `{"services": {"web": {"image": "a:1", "ports": [{"container": 80, "host": 20001}]}}}`
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "app", Stack: mustStack(t, two)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "app", Stack: mustStack(t, one)}); err != nil {
		t.Fatal(err)
	}
	claims, _ := h.st.ListPortClaims(h.ctx, p.Workspace.ID)
	if len(claims) != 1 || claims[0].HostPort != 20001 {
		t.Fatalf("claims = %+v", claims)
	}
	u, _ := h.st.WorkspaceUsage(h.ctx, p.Workspace.ID, "")
	if u.CPUs != 0.5 {
		t.Fatalf("usage still counts removed service: %+v", u)
	}
	if _, ok := h.docker.Containers["alice-app-worker"]; ok {
		t.Fatal("removed service container still present")
	}
	other := `{"services": {"w": {"image": "a:1", "ports": [{"container": 80, "host": 20002}]}}}`
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "other", Stack: mustStack(t, other)}); err != nil {
		t.Fatalf("released port should be claimable: %v", err)
	}
}

func TestDeployFailureReleasesLockAndKeepsLastGoodPrev(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	version := func(v string) service.DeployInput {
		return service.DeployInput{App: "shop", Stack: mustStack(t, strings.Replace(exampleStack, "shop:1.4.2", "shop:"+v, 1))}
	}
	if _, err := h.svc.Deploy(h.ctx, p, version("1.0.0")); err != nil {
		t.Fatal(err)
	}
	h.compose.UpErr = errors.New("compose up failed with exit code 1")
	h.compose.UpOut = "pull access denied for " + h.root + "/secret"
	_, err := h.svc.Deploy(h.ctx, p, version("2.0.0"))
	if code(err) != service.CodeDeployFailed || !strings.Contains(err.Error(), "docker-compose.prev.yml") {
		t.Fatalf("expected deploy_failed mentioning prev: %v", err)
	}
	if strings.Contains(err.Error(), h.root) {
		t.Fatalf("host path leaked to agent: %v", err)
	}
	h.compose.UpErr, h.compose.UpOut = nil, ""
	if _, err := h.svc.Deploy(h.ctx, p, version("3.0.0")); err != nil {
		t.Fatalf("retry after failure must succeed (lock released): %v", err)
	}
	dir := filepath.Join(h.root, "alice", "shop")
	prev, _ := os.ReadFile(filepath.Join(dir, "docker-compose.prev.yml"))
	cur, _ := os.ReadFile(filepath.Join(dir, "docker-compose.yml"))
	if !strings.Contains(string(prev), "shop:1.0.0") {
		t.Fatalf("prev must hold the last successful render (1.0.0):\n%s", prev)
	}
	if !strings.Contains(string(cur), "shop:3.0.0") {
		t.Fatalf("current must be 3.0.0:\n%s", cur)
	}
}

func TestDeployRuntimeViolationTearsDown(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	h.compose.AfterUp = func() {
		h.docker.MutateFacts("alice-shop-web", func(f *dockerx.Facts) { f.Privileged = true })
	}
	_, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if code(err) != service.CodeSecurity {
		t.Fatalf("expected security_violation, got %v", err)
	}
	if strings.Contains(err.Error(), "privileged") {
		t.Fatal("violation details must not be sent to the agent")
	}
	calls := h.compose.Calls()
	if calls[len(calls)-1] != "down dg-alice-shop" {
		t.Fatalf("containers not torn down: %v", calls)
	}
	crit, _ := h.st.ListAudit(h.ctx, store.AuditFilter{Severity: "critical"})
	if len(crit) != 1 || !strings.Contains(crit[0].Error, "privileged") {
		t.Fatalf("critical audit = %+v", crit)
	}
}

func TestDeployConflictWhenLocked(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	unlock, _ := h.compose.TryLock("alice/shop")
	defer unlock()
	_, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if code(err) != service.CodeConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestDeployPortTaken(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)}); err != nil {
		t.Fatal(err)
	}
	blog := service.DeployInput{App: "blog", Stack: mustStack(t, `{"services": {"web": {"image": "a:1", "ports": [{"container": 80, "host": 20001}]}}}`)}
	if _, err := h.svc.Validate(h.ctx, p, blog); code(err) != service.CodePortTaken {
		t.Fatalf("validate: expected port_taken, got %v", err)
	}
	if _, err := h.svc.Deploy(h.ctx, p, blog); code(err) != service.CodePortTaken {
		t.Fatalf("deploy: expected port_taken, got %v", err)
	}
	if _, err := h.st.GetApp(h.ctx, p.Workspace.ID, "blog"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("failed deploy left an app record")
	}
}

func TestDeployQuotaCountsOtherApps(t *testing.T) {
	h := newHarness(t)
	q := store.BuiltinDefaultQuota()
	q.MaxApps = 1
	p := h.workspaceWithQuota("alice", 0, 0, q)
	simple := `{"services": {"web": {"image": "a:1"}}}`
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, simple)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "blog", Stack: mustStack(t, simple)}); code(err) != service.CodeQuota {
		t.Fatalf("expected quota_exceeded, got %v", err)
	}
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, simple)}); err != nil {
		t.Fatalf("redeploy of existing app must not count twice: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/service/`
Expected: FAIL — `h.svc.Deploy undefined`.

- [ ] **Step 3: Replace `deploy.go` with Validate + Deploy**

`internal/service/deploy.go`:

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/dockerx"
	"github.com/sebmahr/deploy-gateway/internal/names"
	"github.com/sebmahr/deploy-gateway/internal/stack"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

func (s *Service) Validate(ctx context.Context, p Principal, in DeployInput) (res ValidateResult, err error) {
	defer s.track(ctx, p, "validate", in, &err, time.Now())
	pl, err := s.plan(ctx, p.Workspace, in.App, &in.Stack)
	if err != nil {
		return ValidateResult{}, err
	}
	if err := s.checkPortConflicts(ctx, p.Workspace, in.App, pl.claims); err != nil {
		return ValidateResult{}, err
	}
	return ValidateResult{App: in.App, Services: pl.servicePlans(p.Workspace.Name), TotalMemoryMB: pl.memBytes >> 20, TotalCPUs: pl.cpus}, nil
}

func (s *Service) Deploy(ctx context.Context, p Principal, in DeployInput) (res AppStatus, err error) {
	defer s.track(ctx, p, "deploy", in, &err, time.Now())
	ws, app := p.Workspace, in.App
	if !names.ValidApp(app) {
		return AppStatus{}, validationError("app", appRule)
	}
	unlock, ok := s.Compose.TryLock(lockKey(ws.Name, app))
	if !ok {
		return AppStatus{}, &Error{Code: CodeConflict, Message: "another operation on this app is in progress"}
	}
	defer unlock()

	appRec, pl, err := s.prepare(ctx, ws, app, &in.Stack)
	if err != nil {
		return AppStatus{}, err
	}
	file, project := s.composeFile(ws.Name, app), names.Project(ws.Name, app)
	if out, upErr := s.Compose.Up(ctx, file, project); upErr != nil {
		_ = s.Store.RecordDeploy(ctx, appRec.ID, "failed")
		msg := "deployment failed: " + upErr.Error() + "\n" + s.sanitize(ws.Name, out) + s.failureLogs(ctx, ws.Name, app)
		if _, statErr := os.Stat(s.prevFile(ws.Name, app)); statErr == nil {
			msg += "\nThe previous successful configuration is kept as docker-compose.prev.yml; redeploy the previous stack to roll back."
		}
		return AppStatus{}, &Error{Code: CodeDeployFailed, Message: msg, detail: out}
	}
	if violations := s.runtimeCheck(ctx, ws.Name, app, pl); len(violations) > 0 {
		downOut, _ := s.Compose.Down(ctx, file, project)
		_ = s.Store.RecordDeploy(ctx, appRec.ID, CodeSecurity)
		return AppStatus{}, &Error{Code: CodeSecurity, Message: "deployment rejected by runtime security check; containers were removed",
			detail: strings.Join(violations, "; ") + " | " + downOut, critical: true}
	}
	if err := s.Store.RecordDeploy(ctx, appRec.ID, "ok"); err != nil {
		return AppStatus{}, err
	}
	return s.status(ctx, ws, app, appRec.Hosts, pl.claims, stack.ServiceNames(pl.stack))
}

// prepare runs under the workspace mutex so concurrent deploys of different
// apps cannot both pass the quota check. It writes files and DB state.
func (s *Service) prepare(ctx context.Context, ws store.Workspace, app string, st *stack.Stack) (store.App, *planned, error) {
	mu := s.wsMutex(ws.Name)
	mu.Lock()
	defer mu.Unlock()
	pl, err := s.plan(ctx, ws, app, st)
	if err != nil {
		return store.App{}, nil, err
	}
	prev, err := s.Store.GetApp(ctx, ws.ID, app)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.App{}, nil, err
	}
	lastOK := err == nil && prev.LastDeployResult == "ok"
	if err := s.prepareDirs(ws.Name, app, st); err != nil {
		return store.App{}, nil, err
	}
	next := filepath.Join(s.appDir(ws.Name, app), "docker-compose.next.yml")
	if err := os.WriteFile(next, pl.out.YAML, 0o640); err != nil {
		return store.App{}, nil, err
	}
	appRec, err := s.Store.SaveDeployment(ctx, ws.ID, app, pl.memBytes, pl.cpus, pl.out.Hosts, pl.claims)
	if err != nil {
		os.Remove(next)
		var taken *store.PortTakenError
		if errors.As(err, &taken) {
			return store.App{}, nil, &Error{Code: CodePortTaken, Message: taken.Error()}
		}
		return store.App{}, nil, err
	}
	cur := s.composeFile(ws.Name, app)
	if lastOK {
		if _, err := os.Stat(cur); err == nil {
			if err := os.Rename(cur, s.prevFile(ws.Name, app)); err != nil {
				return store.App{}, nil, err
			}
		}
	}
	if err := os.Rename(next, cur); err != nil {
		return store.App{}, nil, err
	}
	return appRec, pl, nil
}

func (s *Service) prepareDirs(ws, app string, st *stack.Stack) error {
	data := s.dataDir(ws, app)
	if err := os.MkdirAll(data, 0o750); err != nil {
		return err
	}
	for _, name := range stack.ServiceNames(st) {
		svc := st.Services[name]
		if svc.Volume == nil {
			continue
		}
		dir := filepath.Join(data, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
		if svc.User != "" {
			uidStr, gidStr, _ := strings.Cut(svc.User, ":")
			uid, _ := strconv.Atoi(uidStr)
			gid, _ := strconv.Atoi(gidStr)
			if err := s.Chown(dir, uid, gid); err != nil {
				return fmt.Errorf("chown volume of %s: %w", name, err)
			}
		}
	}
	return nil
}

// runtimeCheck inspects every container of the app (spec §10 layer 3).
func (s *Service) runtimeCheck(ctx context.Context, ws, app string, pl *planned) []string {
	containers, err := s.Docker.ProjectContainers(ctx, ws, app)
	if err != nil {
		return []string{"cannot list containers: " + err.Error()}
	}
	var v []string
	for _, c := range containers {
		svc := c.Labels[names.LabelService]
		facts, err := s.Docker.Inspect(ctx, c.ID)
		if err != nil {
			v = append(v, c.Name+": inspect failed: "+err.Error())
			continue
		}
		var ports []dockerx.PortFact
		for _, cl := range portsFor(pl.claims, svc) {
			ports = append(ports, dockerx.PortFact{ContainerPort: cl.ContainerPort, Protocol: cl.Protocol, HostIP: pl.policy.BindIP, HostPort: cl.HostPort})
		}
		v = append(v, dockerx.CheckRuntime(facts, dockerx.RuntimePolicy{Workspace: ws, App: app, DataDir: pl.policy.DataDir, Ports: ports})...)
	}
	return v
}

// status merges container state with recorded hosts and port claims.
func (s *Service) status(ctx context.Context, ws store.Workspace, app string, hosts map[string]string, claims []store.PortClaim, services []string) (AppStatus, error) {
	containers, err := s.Docker.ProjectContainers(ctx, ws.Name, app)
	if err != nil {
		return AppStatus{}, err
	}
	byService := map[string]dockerx.ContainerInfo{}
	set := map[string]bool{}
	for _, c := range containers {
		svc := c.Labels[names.LabelService]
		byService[svc] = c
		set[svc] = true
	}
	for _, svc := range services {
		set[svc] = true
	}
	for svc := range hosts {
		set[svc] = true
	}
	for _, c := range claims {
		set[c.Service] = true
	}
	res := AppStatus{App: app, Services: []ServiceStatus{}}
	for _, svc := range slices.Sorted(maps.Keys(set)) {
		st := ServiceStatus{Service: svc, InternalHost: names.Container(ws.Name, app, svc), URL: hostURL(hosts[svc]),
			State: "missing", Ports: portsFor(claims, svc)}
		if c, ok := byService[svc]; ok {
			st.State, st.Health = c.State, c.Health
		}
		res.Services = append(res.Services, st)
	}
	return res, nil
}

// failureLogs returns the last log lines of containers that are not healthy.
func (s *Service) failureLogs(ctx context.Context, ws, app string) string {
	containers, err := s.Docker.ProjectContainers(ctx, ws, app)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, c := range containers {
		if c.State == "running" && c.Health != "unhealthy" {
			continue
		}
		logs, err := s.Docker.Logs(ctx, c.ID, 50, "", 8<<10)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "\n--- %s (%s) last log lines ---\n%s", c.Labels[names.LabelService], c.State, s.sanitize(ws, logs))
	}
	return b.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/service/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/service
git commit -m "feat(service): deploy with file rotation, port claims and runtime check"
```

---

### Task 16: Service — List, Logs, Restart, Stop, Delete and admin operations

**Files:**
- Modify: `internal/service/read.go` (add List, Logs, appViews, ownedApp, findContainer), `internal/service/admin.go` (add remaining admin operations)
- Create: `internal/service/lifecycle.go`
- Test: `internal/service/lifecycle_test.go`, `internal/service/admin_test.go`

**Interfaces:**
- Consumes: Tasks 14–15
- Produces:
  - `(*Service).List(ctx, Principal) (ListResult, error)`, `Logs(ctx, Principal, LogsInput) (LogsResult, error)`, `Restart(ctx, Principal, ServiceInput) (AppStatus, error)`, `Stop(ctx, Principal, ServiceInput) (AppStatus, error)`, `Delete(ctx, Principal, DeleteInput) (DeleteResult, error)`
  - Admin: `UpdateWorkspace(ctx, actor string, w store.Workspace) error`, `DeleteWorkspace(ctx, actor string, w store.Workspace) error`, `CreateToken(ctx, actor string, w store.Workspace, label string) (string, error)`, `RevokeToken(ctx, actor string, w store.Workspace, id int64) error`, `DeleteTemplate(ctx, actor string, id int64) error`, `AdminApps(ctx, w store.Workspace) ([]AppView, error)`, `AdminStopApp(ctx, actor string, w store.Workspace, app string) error`, `AdminDeleteApp(ctx, actor string, w store.Workspace, app string) error`, `Orphans(ctx) ([]dockerx.ContainerInfo, error)`, `RemoveOrphan(ctx, actor, id string) error`, `UpdateSettings(ctx, actor string, q store.Quota, retentionDays int) error`

- [ ] **Step 1: Write the failing tests**

`internal/service/lifecycle_test.go`:

```go
package service_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sebmahr/deploy-gateway/internal/service"
)

func deployExample(t *testing.T, h *harness, p service.Principal) {
	t.Helper()
	if _, err := h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)}); err != nil {
		t.Fatal(err)
	}
}

func TestListSurvivesTemplateRevocation(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	if err := h.svc.SetGrants(h.ctx, "admin", p.Workspace, nil); err != nil {
		t.Fatal(err)
	}
	res, err := h.svc.List(h.ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Apps) != 1 || res.Apps[0].Services[1].URL != "https://shop.alice.apps.example.com" || res.Apps[0].LastDeployResult != "ok" {
		t.Fatalf("list = %+v", res)
	}
	_, err = h.svc.Deploy(h.ctx, p, service.DeployInput{App: "shop", Stack: mustStack(t, exampleStack)})
	if code(err) != service.CodeValidation || !strings.Contains(err.Error(), "public-web") {
		t.Fatalf("redeploy with revoked template: %v", err)
	}
}

func TestCrossWorkspaceAccessIsNotFound(t *testing.T) {
	h := newHarness(t)
	alice := h.workspace("alice", 20000, 20099)
	bob := h.workspace("bob", 20100, 20199)
	bobStack := strings.Replace(exampleStack, `"host": 20001`, `"host": 20101`, 1)
	if _, err := h.svc.Deploy(h.ctx, bob, service.DeployInput{App: "shop", Stack: mustStack(t, bobStack)}); err != nil {
		t.Fatal(err)
	}
	before := len(h.docker.Touched())
	if _, err := h.svc.Logs(h.ctx, alice, service.LogsInput{App: "shop", Service: "web"}); code(err) != service.CodeNotFound {
		t.Fatalf("logs: %v", err)
	}
	if _, err := h.svc.Restart(h.ctx, alice, service.ServiceInput{App: "shop"}); code(err) != service.CodeNotFound {
		t.Fatalf("restart: %v", err)
	}
	if _, err := h.svc.Delete(h.ctx, alice, service.DeleteInput{App: "shop", Confirm: "shop"}); code(err) != service.CodeNotFound {
		t.Fatalf("delete: %v", err)
	}
	deployExample(t, h, alice)
	if _, err := h.svc.Logs(h.ctx, alice, service.LogsInput{App: "shop", Service: "admin"}); code(err) != service.CodeNotFound {
		t.Fatalf("unknown service: %v", err)
	}
	for _, id := range h.docker.Touched()[before:] {
		if strings.HasPrefix(id, "bob-") {
			t.Fatalf("alice's calls touched bob's container %s", id)
		}
	}
	if _, ok := h.docker.Containers["bob-shop-web"]; !ok {
		t.Fatal("bob's container was removed")
	}
}

func TestLogs(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	h.docker.LogsOut = "hello from web\n"
	res, err := h.svc.Logs(h.ctx, p, service.LogsInput{App: "shop", Service: "web", Tail: 5000, Since: "10m"})
	if err != nil || res.Logs != "hello from web\n" {
		t.Fatalf("logs = %+v, %v", res, err)
	}
	if _, err := h.svc.Logs(h.ctx, p, service.LogsInput{App: "shop", Service: "web", Since: "yesterday"}); code(err) != service.CodeValidation {
		t.Fatalf("bad since: %v", err)
	}
}

func TestRestartAndStop(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	res, err := h.svc.Stop(h.ctx, p, service.ServiceInput{App: "shop", Service: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Services[1].State != "exited" || res.Services[0].State != "running" {
		t.Fatalf("after stop = %+v", res)
	}
	if !slices.Contains(h.compose.Calls(), "stop dg-alice-shop alice-shop-web") {
		t.Fatalf("calls = %v", h.compose.Calls())
	}
	res, err = h.svc.Restart(h.ctx, p, service.ServiceInput{App: "shop"})
	if err != nil || res.Services[1].State != "running" {
		t.Fatalf("after restart = %+v, %v", res, err)
	}
}

func TestDelete(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	if _, err := h.svc.Delete(h.ctx, p, service.DeleteInput{App: "shop", Confirm: "shp"}); code(err) != service.CodeValidation {
		t.Fatalf("confirm mismatch: %v", err)
	}
	res, err := h.svc.Delete(h.ctx, p, service.DeleteInput{App: "shop", Confirm: "shop"})
	if err != nil || !res.Deleted {
		t.Fatalf("delete = %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "alice", "shop")); !os.IsNotExist(err) {
		t.Fatal("app folder not removed")
	}
	if claims, _ := h.st.ListPortClaims(h.ctx, p.Workspace.ID); len(claims) != 0 {
		t.Fatal("port claims not released")
	}
	if len(h.docker.Containers) != 0 {
		t.Fatalf("containers left: %v", h.docker.Containers)
	}
}
```

`internal/service/admin_test.go`:

```go
package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sebmahr/deploy-gateway/internal/auth"
	"github.com/sebmahr/deploy-gateway/internal/dockerx"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/store"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
)

func TestWorkspacePortRules(t *testing.T) {
	h := newHarness(t)
	h.workspace("alice", 20000, 20099)
	q := store.BuiltinDefaultQuota()
	if _, err := h.svc.CreateWorkspace(h.ctx, "admin", "bob", q, service.PortSettings{Start: 20050, End: 20150, BindIP: "0.0.0.0"}); code(err) != service.CodeValidation {
		t.Fatalf("overlap: %v", err)
	}
	if _, err := h.svc.CreateWorkspace(h.ctx, "admin", "bob", q, service.PortSettings{Start: 80, End: 90, BindIP: "0.0.0.0"}); code(err) != service.CodeValidation {
		t.Fatalf("privileged ports: %v", err)
	}
	if _, err := h.svc.CreateWorkspace(h.ctx, "admin", "bob", q, service.PortSettings{BindIP: "not-an-ip"}); code(err) != service.CodeValidation {
		t.Fatalf("bad bind ip: %v", err)
	}
	if _, err := h.svc.CreateWorkspace(h.ctx, "admin", "Bad-Name", q, service.PortSettings{BindIP: "0.0.0.0"}); code(err) != service.CodeValidation {
		t.Fatalf("bad name: %v", err)
	}
}

func TestUpdateWorkspaceRejectsRangeExcludingUsedPorts(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	w := p.Workspace
	w.PortRangeStart, w.PortRangeEnd = 20050, 20099
	if err := h.svc.UpdateWorkspace(h.ctx, "admin", w); code(err) != service.CodeValidation {
		t.Fatalf("expected validation error, got %v", err)
	}
	w.PortRangeStart, w.PortRangeEnd = 20000, 20010
	if err := h.svc.UpdateWorkspace(h.ctx, "admin", w); err != nil {
		t.Fatalf("range still covering 20001 must be accepted: %v", err)
	}
}

func TestDeleteWorkspace(t *testing.T) {
	h := newHarness(t)
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	if err := h.svc.DeleteWorkspace(h.ctx, "admin", p.Workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(h.root, "alice")); !os.IsNotExist(err) {
		t.Fatal("workspace folder not removed")
	}
	if len(h.docker.Containers) != 0 {
		t.Fatal("containers left behind")
	}
	if _, err := h.st.GetWorkspaceByName(h.ctx, "alice"); err == nil {
		t.Fatal("workspace row still present")
	}
}

func TestTokens(t *testing.T) {
	h := newHarness(t)
	alice := h.workspace("alice", 0, 0)
	bob := h.workspace("bob", 0, 0)
	tok, err := h.svc.CreateToken(h.ctx, "admin", alice.Workspace, "ci")
	if err != nil {
		t.Fatal(err)
	}
	got, ws, err := h.st.TokenByHash(h.ctx, auth.HashToken(tok))
	if err != nil || ws.Name != "alice" {
		t.Fatalf("token lookup: %v %v", ws, err)
	}
	if err := h.svc.RevokeToken(h.ctx, "admin", bob.Workspace, got.ID); code(err) != service.CodeNotFound {
		t.Fatalf("revoking another workspace's token: %v", err)
	}
	if err := h.svc.RevokeToken(h.ctx, "admin", alice.Workspace, got.ID); err != nil {
		t.Fatal(err)
	}
}

func TestTemplateAdmin(t *testing.T) {
	h := newHarness(t)
	_, err := h.svc.SaveTemplate(h.ctx, "admin", 0, tmpl.Template{Name: "lan", Networks: []string{"missing-net"}})
	if code(err) != service.CodeValidation {
		t.Fatalf("missing network: %v", err)
	}
	_, err = h.svc.SaveTemplate(h.ctx, "admin", 0, tmpl.Template{Name: "dup", HostPattern: "{{app}}.{{workspace}}.apps.example.com", Networks: []string{"apps"}})
	if code(err) != service.CodeValidation {
		t.Fatalf("overlapping suffix: %v", err)
	}
	h.workspace("alice", 0, 0) // grants public-web
	if err := h.svc.DeleteTemplate(h.ctx, "admin", h.tplID); code(err) != service.CodeValidation {
		t.Fatalf("delete granted template: %v", err)
	}
}

func TestOrphans(t *testing.T) {
	h := newHarness(t)
	labels := map[string]string{"deploy-gateway.workspace": "ghost", "deploy-gateway.app": "x", "deploy-gateway.managed": "true"}
	h.docker.Add(dockerx.ContainerInfo{ID: "ghost-x-web", Name: "ghost-x-web", Labels: labels}, dockerx.Facts{})
	p := h.workspace("alice", 20000, 20099)
	deployExample(t, h, p)
	orphans, err := h.svc.Orphans(h.ctx)
	if err != nil || len(orphans) != 1 || orphans[0].ID != "ghost-x-web" {
		t.Fatalf("orphans = %+v, %v", orphans, err)
	}
	if err := h.svc.RemoveOrphan(h.ctx, "admin", "alice-shop-web"); code(err) != service.CodeNotFound {
		t.Fatalf("removing a non-orphan must fail: %v", err)
	}
	if err := h.svc.RemoveOrphan(h.ctx, "admin", "ghost-x-web"); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.docker.Containers["ghost-x-web"]; ok {
		t.Fatal("orphan not removed")
	}
}

func TestUpdateSettings(t *testing.T) {
	h := newHarness(t)
	q := store.BuiltinDefaultQuota()
	q.MaxApps = 2
	if err := h.svc.UpdateSettings(h.ctx, "admin", q, 30); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.st.DefaultQuota(h.ctx); got.MaxApps != 2 {
		t.Fatalf("default quota = %+v", got)
	}
	q.MaxApps = 0
	if err := h.svc.UpdateSettings(h.ctx, "admin", q, 30); code(err) != service.CodeValidation {
		t.Fatalf("invalid quota: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/service/`
Expected: FAIL — `h.svc.List undefined`, ...

- [ ] **Step 3: Append List and Logs to `read.go`**

Add these imports to `internal/service/read.go` (`errors`, `time` already partly present — final import block shown):

```go
import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/dockerx"
	"github.com/sebmahr/deploy-gateway/internal/names"
	"github.com/sebmahr/deploy-gateway/internal/store"
)
```

Append to `internal/service/read.go`:

```go
func (s *Service) List(ctx context.Context, p Principal) (res ListResult, err error) {
	defer s.track(ctx, p, "list", nil, &err, time.Now())
	apps, err := s.appViews(ctx, p.Workspace)
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{Apps: apps}, nil
}

// appViews builds app status from DB records (hosts, claims) plus live
// container state; it never consults templates.
func (s *Service) appViews(ctx context.Context, ws store.Workspace) ([]AppView, error) {
	apps, err := s.Store.ListApps(ctx, ws.ID)
	if err != nil {
		return nil, err
	}
	claims, err := s.Store.ListPortClaims(ctx, ws.ID)
	if err != nil {
		return nil, err
	}
	out := []AppView{}
	for _, a := range apps {
		var appClaims []store.PortClaim
		for _, c := range claims {
			if c.App == a.Name {
				appClaims = append(appClaims, c)
			}
		}
		st, err := s.status(ctx, ws, a.Name, a.Hosts, appClaims, nil)
		if err != nil {
			return nil, err
		}
		v := AppView{App: a.Name, Services: st.Services, DiskBytes: s.disk.usage(s.appDir(ws.Name, a.Name)), LastDeployResult: a.LastDeployResult}
		if a.LastDeployAt != nil {
			v.LastDeployAt = a.LastDeployAt.UTC().Format(time.RFC3339)
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) Logs(ctx context.Context, p Principal, in LogsInput) (res LogsResult, err error) {
	defer s.track(ctx, p, "logs", in, &err, time.Now())
	ws := p.Workspace
	if _, err = s.ownedApp(ctx, ws, in.App); err != nil {
		return LogsResult{}, err
	}
	if !names.ValidService(in.Service) {
		return LogsResult{}, notFoundError("service")
	}
	tail := in.Tail
	if tail <= 0 {
		tail = 200
	}
	tail = min(tail, 1000)
	if in.Since != "" && !validSince(in.Since) {
		return LogsResult{}, validationError("since", "since must be a duration like 10m or an RFC3339 timestamp")
	}
	c, err := s.findContainer(ctx, ws.Name, in.App, in.Service)
	if err != nil {
		return LogsResult{}, err
	}
	logs, err := s.Docker.Logs(ctx, c.ID, tail, in.Since, 256<<10)
	if err != nil {
		return LogsResult{}, err
	}
	return LogsResult{App: in.App, Service: in.Service, Logs: logs}, nil
}

func validSince(v string) bool {
	if _, err := time.ParseDuration(v); err == nil {
		return true
	}
	_, err := time.Parse(time.RFC3339, v)
	return err == nil
}

// ownedApp resolves an app strictly within the caller's workspace.
func (s *Service) ownedApp(ctx context.Context, ws store.Workspace, app string) (store.App, error) {
	if !names.ValidApp(app) {
		return store.App{}, notFoundError("app")
	}
	a, err := s.Store.GetApp(ctx, ws.ID, app)
	if errors.Is(err, store.ErrNotFound) {
		return store.App{}, notFoundError("app")
	}
	return a, err
}

func (s *Service) findContainer(ctx context.Context, ws, app, svc string) (dockerx.ContainerInfo, error) {
	containers, err := s.Docker.ProjectContainers(ctx, ws, app)
	if err != nil {
		return dockerx.ContainerInfo{}, err
	}
	for _, c := range containers {
		if c.Labels[names.LabelWorkspace] == ws && c.Labels[names.LabelApp] == app && c.Labels[names.LabelService] == svc {
			return c, nil
		}
	}
	return dockerx.ContainerInfo{}, notFoundError("service")
}
```

- [ ] **Step 4: Write `lifecycle.go`**

`internal/service/lifecycle.go`:

```go
package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/names"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

type composeOp func(ctx context.Context, file, project, service string) (string, error)

func (s *Service) Restart(ctx context.Context, p Principal, in ServiceInput) (res AppStatus, err error) {
	defer s.track(ctx, p, "restart", in, &err, time.Now())
	return s.lifecycle(ctx, p.Workspace, in.App, in.Service, s.Compose.Restart)
}

func (s *Service) Stop(ctx context.Context, p Principal, in ServiceInput) (res AppStatus, err error) {
	defer s.track(ctx, p, "stop", in, &err, time.Now())
	return s.lifecycle(ctx, p.Workspace, in.App, in.Service, s.Compose.Stop)
}

func (s *Service) lifecycle(ctx context.Context, ws store.Workspace, app, svc string, op composeOp) (AppStatus, error) {
	a, err := s.ownedApp(ctx, ws, app)
	if err != nil {
		return AppStatus{}, err
	}
	target := ""
	if svc != "" {
		if !names.ValidService(svc) {
			return AppStatus{}, notFoundError("service")
		}
		if _, err := s.findContainer(ctx, ws.Name, app, svc); err != nil {
			return AppStatus{}, err
		}
		target = names.Container(ws.Name, app, svc)
	}
	unlock, ok := s.Compose.TryLock(lockKey(ws.Name, app))
	if !ok {
		return AppStatus{}, &Error{Code: CodeConflict, Message: "another operation on this app is in progress"}
	}
	defer unlock()
	if out, err := op(ctx, s.composeFile(ws.Name, app), names.Project(ws.Name, app), target); err != nil {
		return AppStatus{}, &Error{Code: CodeDeployFailed, Message: err.Error() + "\n" + s.sanitize(ws.Name, out), detail: out}
	}
	claims, err := s.Store.ListPortClaims(ctx, ws.ID)
	if err != nil {
		return AppStatus{}, err
	}
	var appClaims []store.PortClaim
	for _, c := range claims {
		if c.App == app {
			appClaims = append(appClaims, c)
		}
	}
	return s.status(ctx, ws, app, a.Hosts, appClaims, nil)
}

func (s *Service) Delete(ctx context.Context, p Principal, in DeleteInput) (res DeleteResult, err error) {
	defer s.track(ctx, p, "delete", in, &err, time.Now())
	if in.Confirm != in.App {
		return DeleteResult{}, validationError("confirm", "confirm must equal the app name")
	}
	if err := s.deleteApp(ctx, p.Workspace, in.App); err != nil {
		return DeleteResult{}, err
	}
	return DeleteResult{App: in.App, Deleted: true}, nil
}

// deleteApp stops and removes containers, the private network, anonymous
// volumes, the app folder (including data) and the DB record.
func (s *Service) deleteApp(ctx context.Context, ws store.Workspace, app string) error {
	a, err := s.ownedApp(ctx, ws, app)
	if err != nil {
		return err
	}
	unlock, ok := s.Compose.TryLock(lockKey(ws.Name, app))
	if !ok {
		return &Error{Code: CodeConflict, Message: "another operation on this app is in progress"}
	}
	defer unlock()
	file := s.composeFile(ws.Name, app)
	if _, statErr := os.Stat(file); statErr == nil {
		if out, err := s.Compose.Down(ctx, file, names.Project(ws.Name, app)); err != nil {
			return &Error{Code: CodeDeployFailed, Message: "compose down failed: " + s.sanitize(ws.Name, out), detail: out}
		}
	}
	leftovers, err := s.Docker.ProjectContainers(ctx, ws.Name, app)
	if err != nil {
		return err
	}
	for _, c := range leftovers {
		if err := s.Docker.RemoveContainer(ctx, c.ID); err != nil {
			return err
		}
	}
	dir := s.appDir(ws.Name, app)
	if filepath.Dir(dir) != s.workspaceDir(ws.Name) {
		return errors.New("refusing to remove unexpected path " + dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return s.Store.DeleteApp(ctx, a.ID)
}
```

- [ ] **Step 5: Append the remaining admin operations to `admin.go`**

Final import block for `internal/service/admin.go`:

```go
import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/auth"
	"github.com/sebmahr/deploy-gateway/internal/dockerx"
	"github.com/sebmahr/deploy-gateway/internal/names"
	"github.com/sebmahr/deploy-gateway/internal/store"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
)
```

Append to `internal/service/admin.go`:

```go
func (s *Service) UpdateWorkspace(ctx context.Context, actor string, w store.Workspace) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "update_workspace", w, &err, time.Now())
	if err = w.Quota.Validate(); err != nil {
		return validationError("quota", err.Error())
	}
	if err = s.validatePortSettings(ctx, PortSettings{Start: w.PortRangeStart, End: w.PortRangeEnd, BindIP: w.PortBindIP}, w.ID); err != nil {
		return err
	}
	claims, err := s.Store.ListPortClaims(ctx, w.ID)
	if err != nil {
		return err
	}
	for _, c := range claims {
		if w.PortRangeStart == 0 || c.HostPort < w.PortRangeStart || c.HostPort > w.PortRangeEnd {
			return validationError("ports", fmt.Sprintf("app %q publishes port %d outside the new range; redeploy or delete it first", c.App, c.HostPort))
		}
	}
	return s.Store.UpdateWorkspace(ctx, w)
}

func (s *Service) DeleteWorkspace(ctx context.Context, actor string, w store.Workspace) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "delete_workspace", nil, &err, time.Now())
	apps, err := s.Store.ListApps(ctx, w.ID)
	if err != nil {
		return err
	}
	for _, a := range apps {
		if err = s.deleteApp(ctx, w, a.Name); err != nil {
			return err
		}
	}
	dir := s.workspaceDir(w.Name)
	if !names.ValidWorkspace(w.Name) || filepath.Dir(dir) != s.Root {
		return errors.New("refusing to remove unexpected path " + dir)
	}
	if err = os.RemoveAll(dir); err != nil {
		return err
	}
	return s.Store.DeleteWorkspace(ctx, w.ID)
}

func (s *Service) CreateToken(ctx context.Context, actor string, w store.Workspace, label string) (token string, err error) {
	defer s.adminTrack(ctx, actor, w.Name, "create_token", map[string]string{"label": label}, &err, time.Now())
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 100 {
		return "", validationError("label", "label must be 1-100 characters")
	}
	var hash, prefix string
	if token, hash, prefix, err = auth.GenerateToken(); err != nil {
		return "", err
	}
	if _, err = s.Store.CreateToken(ctx, w.ID, label, hash, prefix); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) RevokeToken(ctx context.Context, actor string, w store.Workspace, id int64) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "revoke_token", map[string]int64{"id": id}, &err, time.Now())
	t, err := s.Store.GetToken(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && t.WorkspaceID != w.ID) {
		return notFoundError("token")
	}
	if err != nil {
		return err
	}
	return s.Store.RevokeToken(ctx, id)
}

func (s *Service) DeleteTemplate(ctx context.Context, actor string, id int64) (err error) {
	defer s.adminTrack(ctx, actor, "", "delete_template", map[string]int64{"id": id}, &err, time.Now())
	err = s.Store.DeleteTemplate(ctx, id)
	if errors.Is(err, store.ErrInUse) {
		return validationError("template", "template is granted to at least one workspace; revoke the grants first")
	}
	if errors.Is(err, store.ErrNotFound) {
		return notFoundError("template")
	}
	return err
}

func (s *Service) AdminApps(ctx context.Context, w store.Workspace) ([]AppView, error) {
	return s.appViews(ctx, w)
}

func (s *Service) AdminStopApp(ctx context.Context, actor string, w store.Workspace, app string) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "stop_app", map[string]string{"app": app}, &err, time.Now())
	_, err = s.lifecycle(ctx, w, app, "", s.Compose.Stop)
	return err
}

func (s *Service) AdminDeleteApp(ctx context.Context, actor string, w store.Workspace, app string) (err error) {
	defer s.adminTrack(ctx, actor, w.Name, "delete_app", map[string]string{"app": app}, &err, time.Now())
	return s.deleteApp(ctx, w, app)
}

// Orphans are gateway-labelled containers whose workspace/app is unknown.
func (s *Service) Orphans(ctx context.Context) ([]dockerx.ContainerInfo, error) {
	keys, err := s.Store.AppKeys(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.Docker.ManagedContainers(ctx)
	if err != nil {
		return nil, err
	}
	out := []dockerx.ContainerInfo{}
	for _, c := range all {
		if !keys[c.Labels[names.LabelWorkspace]+"/"+c.Labels[names.LabelApp]] {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Service) RemoveOrphan(ctx context.Context, actor, id string) (err error) {
	defer s.adminTrack(ctx, actor, "", "remove_orphan", map[string]string{"id": id}, &err, time.Now())
	orphans, err := s.Orphans(ctx)
	if err != nil {
		return err
	}
	for _, o := range orphans {
		if o.ID == id {
			return s.Docker.RemoveContainer(ctx, id)
		}
	}
	return notFoundError("orphan container")
}

func (s *Service) UpdateSettings(ctx context.Context, actor string, q store.Quota, retentionDays int) (err error) {
	defer s.adminTrack(ctx, actor, "", "update_settings", map[string]any{"default_quota": q, "audit_retention_days": retentionDays}, &err, time.Now())
	if err = q.Validate(); err != nil {
		return validationError("default_quota", err.Error())
	}
	if retentionDays < 1 || retentionDays > 3650 {
		return validationError("audit_retention_days", "audit retention must be 1-3650 days")
	}
	if err = s.Store.SetDefaultQuota(ctx, q); err != nil {
		return err
	}
	return s.Store.SetAuditRetentionDays(ctx, retentionDays)
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/service/...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/service
git commit -m "feat(service): list, logs, lifecycle, delete and admin operations"
```

---
### Task 17: Agent authentication and JSON API

**Files:**
- Create: `internal/agentauth/agentauth.go`, `internal/api/api.go`
- Test: `internal/agentauth/agentauth_test.go`, `internal/api/api_test.go`

**Interfaces:**
- Consumes: `auth.HashToken`, `auth.ClientIP`, `auth.Limiter` (11); `store.TokenByHash`, `store.TouchToken` (9); `service.*` (14–16); `servicetest` (14)
- Produces:
  - `agentauth.Authenticator{Store *store.Store; Limiter *auth.Limiter; TrustedProxies []netip.Prefix}`
  - `(*Authenticator).Verify(ctx, token, ip string) (service.Principal, error)`; `agentauth.ErrUnauthorized`
  - `(*Authenticator).Middleware(next http.Handler) http.Handler`; `agentauth.FromContext(ctx) (service.Principal, bool)`
  - `(*Authenticator).MCPVerifier() sdkauth.TokenVerifier` (puts the Principal in `TokenInfo.Extra["principal"]`)
  - `api.StatusFor(code string) int`
  - `api.Handler{Svc *service.Service; Auth *agentauth.Authenticator}`; `(*Handler).Routes() http.Handler` serving `POST /api/v1/{whoami,list_templates,validate,deploy,list,logs,restart,stop,delete}`

- [ ] **Step 1: Add the MCP SDK dependency** (used by `MCPVerifier`)

```bash
go get github.com/modelcontextprotocol/go-sdk@v1.8.0
```

- [ ] **Step 2: Write the failing tests**

`internal/agentauth/agentauth_test.go`:

```go
package agentauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/sebmahr/deploy-gateway/internal/auth"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

func setup(t *testing.T) (*Authenticator, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	w, err := st.CreateWorkspace(context.Background(), store.Workspace{Name: "alice", Quota: store.BuiltinDefaultQuota(), PortBindIP: "0.0.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	tok, hash, prefix, _ := auth.GenerateToken()
	if _, err := st.CreateToken(context.Background(), w.ID, "ci", hash, prefix); err != nil {
		t.Fatal(err)
	}
	return &Authenticator{Store: st, Limiter: auth.NewLimiter(3, time.Minute, time.Hour)}, tok
}

func TestVerify(t *testing.T) {
	a, tok := setup(t)
	p, err := a.Verify(context.Background(), tok, "1.2.3.4")
	if err != nil || p.Workspace.Name != "alice" || p.ClientIP != "1.2.3.4" || p.TokenID == 0 {
		t.Fatalf("Verify = %+v, %v", p, err)
	}
	for _, bad := range []string{"", "dgw_short", "Bearer x", tok + "x"} {
		if _, err := a.Verify(context.Background(), bad, "5.5.5.5"); !errors.Is(err, ErrUnauthorized) {
			t.Errorf("token %q: %v", bad, err)
		}
	}
	if _, err := a.Verify(context.Background(), tok, "5.5.5.5"); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("IP must be blocked after repeated failures, even with a valid token")
	}
}

func TestMiddleware(t *testing.T) {
	a, tok := setup(t)
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := FromContext(r.Context())
		if !ok {
			t.Error("principal missing")
		}
		w.Write([]byte(p.Workspace.Name))
	}))
	r := httptest.NewRequest("POST", "/api/v1/whoami", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "alice" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("POST", "/api/v1/whoami", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 || w.Body.String() != `{"code":"unauthorized","message":"unauthorized"}`+"\n" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}

func TestMCPVerifier(t *testing.T) {
	a, tok := setup(t)
	v := a.MCPVerifier()
	r := httptest.NewRequest("POST", "/mcp", nil)
	info, err := v(context.Background(), tok, r)
	if err != nil || info.UserID == "" {
		t.Fatalf("info = %+v, %v", info, err)
	}
	if _, err := v(context.Background(), "dgw_nope", r); !errors.Is(err, sdkauth.ErrInvalidToken) {
		t.Fatalf("bad token: %v", err)
	}
}
```

`internal/api/api_test.go`:

```go
package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/agentauth"
	"github.com/sebmahr/deploy-gateway/internal/auth"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/service/servicetest"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

func setup(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	compose, docker := &servicetest.FakeCompose{}, servicetest.NewFakeDocker()
	servicetest.Wire(compose, docker)
	svc := service.New(st, compose, docker, t.TempDir())
	svc.Chown = func(string, int, int) error { return nil }
	w, err := svc.CreateWorkspace(ctx, "admin", "alice", store.BuiltinDefaultQuota(), service.PortSettings{Start: 20000, End: 20099, BindIP: "0.0.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svc.CreateToken(ctx, "admin", w, "ci")
	if err != nil {
		t.Fatal(err)
	}
	authn := &agentauth.Authenticator{Store: st, Limiter: auth.NewLimiter(10, time.Minute, time.Hour)}
	srv := httptest.NewServer((&Handler{Svc: svc, Auth: authn}).Routes())
	t.Cleanup(srv.Close)
	return srv, tok
}

func post(t *testing.T, srv *httptest.Server, tok, tool, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/"+tool, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("non-JSON response %d: %s", resp.StatusCode, raw)
	}
	return resp.StatusCode, m
}

func TestWhoamiAndAuth(t *testing.T) {
	srv, tok := setup(t)
	code, body := post(t, srv, tok, "whoami", "")
	if code != 200 || body["workspace"] != "alice" {
		t.Fatalf("%d %v", code, body)
	}
	if code, body := post(t, srv, "", "whoami", "{}"); code != 401 || body["code"] != "unauthorized" {
		t.Fatalf("%d %v", code, body)
	}
}

func TestDeployViaAPI(t *testing.T) {
	srv, tok := setup(t)
	ok := `{"app": "shop", "stack": {"services": {"web": {"image": "nginx:1.27.0", "ports": [{"container": 80, "host": 20001}]}}}}`
	code, body := post(t, srv, tok, "deploy", ok)
	if code != 200 || body["app"] != "shop" {
		t.Fatalf("%d %v", code, body)
	}
	code, body = post(t, srv, tok, "deploy", `{"app": "shop", "stack": {"services": {"web": {"image": "nginx:1.27.0", "privileged": true}}}}`)
	if code != 400 || body["code"] != "validation_failed" || !strings.Contains(body["message"].(string), "privileged") {
		t.Fatalf("unknown field: %d %v", code, body)
	}
	code, body = post(t, srv, tok, "deploy", `{"app": "shop", "stack": {"services": {"web": {"image": "nginx"}}}}`)
	if code != 422 || body["errors"] == nil {
		t.Fatalf("validation: %d %v", code, body)
	}
	code, body = post(t, srv, tok, "logs", `{"app": "nope", "service": "web"}`)
	if code != 404 || body["code"] != "not_found" {
		t.Fatalf("not found: %d %v", code, body)
	}
	code, body = post(t, srv, tok, "list", "{}")
	if code != 200 || len(body["apps"].([]any)) != 1 {
		t.Fatalf("list: %d %v", code, body)
	}
	code, body = post(t, srv, tok, "delete", `{"app": "shop", "confirm": "shop"}`)
	if code != 200 || body["deleted"] != true {
		t.Fatalf("delete: %d %v", code, body)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/agentauth/ ./internal/api/`
Expected: FAIL — `undefined: Authenticator`, `undefined: Handler`.

- [ ] **Step 4: Write `agentauth.go`**

`internal/agentauth/agentauth.go`:

```go
// Package agentauth authenticates agent bearer tokens for the JSON API and
// the MCP endpoint. The workspace is resolved from the token only.
package agentauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/sebmahr/deploy-gateway/internal/auth"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

var ErrUnauthorized = errors.New("unauthorized")

type Authenticator struct {
	Store          *store.Store
	Limiter        *auth.Limiter
	TrustedProxies []netip.Prefix
}

type ctxKey struct{}

func (a *Authenticator) Verify(ctx context.Context, token, ip string) (service.Principal, error) {
	if a.Limiter.Blocked(ip) {
		return service.Principal{}, ErrUnauthorized
	}
	if !strings.HasPrefix(token, "dgw_") || len(token) != 47 {
		a.Limiter.Fail(ip)
		return service.Principal{}, ErrUnauthorized
	}
	t, ws, err := a.Store.TokenByHash(ctx, auth.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		a.Limiter.Fail(ip)
		return service.Principal{}, ErrUnauthorized
	}
	if err != nil {
		return service.Principal{}, err
	}
	if err := a.Store.TouchToken(ctx, t.ID); err != nil {
		return service.Principal{}, err
	}
	return service.Principal{Workspace: ws, TokenID: t.ID, ClientIP: ip}, nil
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := auth.ClientIP(r, a.TrustedProxies)
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		p, err := a.Verify(r.Context(), token, ip)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			if errors.Is(err, ErrUnauthorized) {
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprintln(w, `{"code":"unauthorized","message":"unauthorized"}`)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintln(w, `{"code":"internal","message":"internal error"}`)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	})
}

func FromContext(ctx context.Context) (service.Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(service.Principal)
	return p, ok
}

// MCPVerifier adapts Verify to the MCP SDK's bearer-token middleware. UserID
// binds MCP sessions to the token that created them.
func (a *Authenticator) MCPVerifier() sdkauth.TokenVerifier {
	return func(ctx context.Context, token string, r *http.Request) (*sdkauth.TokenInfo, error) {
		p, err := a.Verify(ctx, token, auth.ClientIP(r, a.TrustedProxies))
		if errors.Is(err, ErrUnauthorized) {
			return nil, sdkauth.ErrInvalidToken
		}
		if err != nil {
			return nil, err
		}
		return &sdkauth.TokenInfo{UserID: fmt.Sprintf("token:%d", p.TokenID), Extra: map[string]any{"principal": p}}, nil
	}
}
```

- [ ] **Step 5: Write `api.go`**

`internal/api/api.go`:

```go
// Package api exposes the agent tools as a small JSON HTTP API, mirroring
// the MCP tools one to one.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/sebmahr/deploy-gateway/internal/agentauth"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/stack"
)

type Handler struct {
	Svc  *service.Service
	Auth *agentauth.Authenticator
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	s := h.Svc
	handle(mux, "whoami", func(ctx context.Context, p service.Principal, _ service.NoInput) (any, error) { return s.Whoami(ctx, p) })
	handle(mux, "list_templates", func(ctx context.Context, p service.Principal, _ service.NoInput) (any, error) {
		return s.ListTemplates(ctx, p)
	})
	handle(mux, "validate", func(ctx context.Context, p service.Principal, in service.DeployInput) (any, error) { return s.Validate(ctx, p, in) })
	handle(mux, "deploy", func(ctx context.Context, p service.Principal, in service.DeployInput) (any, error) { return s.Deploy(ctx, p, in) })
	handle(mux, "list", func(ctx context.Context, p service.Principal, _ service.NoInput) (any, error) { return s.List(ctx, p) })
	handle(mux, "logs", func(ctx context.Context, p service.Principal, in service.LogsInput) (any, error) { return s.Logs(ctx, p, in) })
	handle(mux, "restart", func(ctx context.Context, p service.Principal, in service.ServiceInput) (any, error) { return s.Restart(ctx, p, in) })
	handle(mux, "stop", func(ctx context.Context, p service.Principal, in service.ServiceInput) (any, error) { return s.Stop(ctx, p, in) })
	handle(mux, "delete", func(ctx context.Context, p service.Principal, in service.DeleteInput) (any, error) { return s.Delete(ctx, p, in) })
	return h.Auth.Middleware(mux)
}

func handle[In any](mux *http.ServeMux, name string, fn func(context.Context, service.Principal, In) (any, error)) {
	mux.HandleFunc("POST /api/v1/"+name, func(w http.ResponseWriter, r *http.Request) {
		p, ok := agentauth.FromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, &service.Error{Code: service.CodeUnauthorized, Message: "unauthorized"})
			return
		}
		var in In
		if err := decodeStrict(w, r, &in); err != nil {
			writeJSON(w, http.StatusBadRequest, &service.Error{Code: service.CodeValidation, Message: "invalid request body: " + err.Error()})
			return
		}
		res, err := fn(r.Context(), p, in)
		if err != nil {
			var se *service.Error
			if !errors.As(err, &se) {
				se = &service.Error{Code: service.CodeInternal, Message: "internal error"}
			}
			writeJSON(w, StatusFor(se.Code), se)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
}

// decodeStrict rejects unknown fields at every nesting level (including the
// stack), trailing data and oversize bodies. An empty body means {}.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, stack.MaxRequestBytes+4096))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after JSON object")
	}
	return nil
}

// StatusFor maps a service error code to an HTTP status (also used by the admin API).
func StatusFor(code string) int {
	switch code {
	case service.CodeUnauthorized:
		return http.StatusUnauthorized
	case service.CodeValidation, service.CodeQuota, service.CodeSecurity:
		return http.StatusUnprocessableEntity
	case service.CodePortTaken, service.CodeConflict:
		return http.StatusConflict
	case service.CodeNotFound:
		return http.StatusNotFound
	case service.CodeDeployFailed:
		return http.StatusBadGateway
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/agentauth/ ./internal/api/`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/agentauth internal/api
git commit -m "feat(api): bearer token auth and JSON API for agent tools"
```

---

### Task 18: MCP server

**Files:**
- Create: `internal/mcpserver/mcpserver.go`
- Test: `internal/mcpserver/mcpserver_test.go`

**Interfaces:**
- Consumes: `service.*` (14–16), `agentauth.Authenticator.MCPVerifier` (17)
- Produces:
  - `mcpserver.New(svc *service.Service, version string) *mcp.Server` registering tools `whoami`, `list_templates`, `validate`, `deploy`, `list`, `logs`, `restart`, `stop`, `delete`
  - `mcpserver.Handler(srv *mcp.Server, a *agentauth.Authenticator) http.Handler` — stateless streamable HTTP behind bearer auth

- [ ] **Step 1: Write the failing test**

`internal/mcpserver/mcpserver_test.go`:

```go
package mcpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sebmahr/deploy-gateway/internal/agentauth"
	"github.com/sebmahr/deploy-gateway/internal/auth"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/service/servicetest"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func setup(t *testing.T) (string, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	compose, docker := &servicetest.FakeCompose{}, servicetest.NewFakeDocker()
	servicetest.Wire(compose, docker)
	svc := service.New(st, compose, docker, t.TempDir())
	svc.Chown = func(string, int, int) error { return nil }
	w, err := svc.CreateWorkspace(ctx, "admin", "alice", store.BuiltinDefaultQuota(), service.PortSettings{BindIP: "0.0.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svc.CreateToken(ctx, "admin", w, "ci")
	if err != nil {
		t.Fatal(err)
	}
	authn := &agentauth.Authenticator{Store: st, Limiter: auth.NewLimiter(10, time.Minute, time.Hour)}
	srv := httptest.NewServer(Handler(New(svc, "test"), authn))
	t.Cleanup(srv.Close)
	return srv.URL, tok
}

func connect(t *testing.T, url, token string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	return client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: bearer{token}},
	}, nil)
}

func TestToolsOverStreamableHTTP(t *testing.T) {
	url, tok := setup(t)
	cs, err := connect(t, url, tok)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	slices.Sort(names)
	want := []string{"delete", "deploy", "list", "list_templates", "logs", "restart", "stop", "validate", "whoami"}
	if !slices.Equal(names, want) {
		t.Fatalf("tools = %v", names)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "whoami", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("whoami: %v %+v", err, res)
	}
	if res.StructuredContent.(map[string]any)["workspace"] != "alice" {
		t.Fatalf("structured = %v", res.StructuredContent)
	}

	stack := map[string]any{"services": map[string]any{"web": map[string]any{"image": "nginx:1.27.0"}}}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "deploy", Arguments: map[string]any{"app": "shop", "stack": stack}})
	if err != nil || res.IsError {
		t.Fatalf("deploy: %v %+v", err, res)
	}

	bad := map[string]any{"services": map[string]any{"web": map[string]any{"image": "nginx"}}}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "deploy", Arguments: map[string]any{"app": "shop", "stack": bad}})
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "validation_failed") {
		t.Fatalf("expected tool error with validation_failed: %v %+v", err, res)
	}

	sneaky := map[string]any{"services": map[string]any{"web": map[string]any{"image": "nginx:1.27.0", "privileged": true}}}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "deploy", Arguments: map[string]any{"app": "shop", "stack": sneaky}})
	if err == nil && !res.IsError {
		t.Fatal("unknown field privileged must be rejected")
	}
}

func TestRejectsBadToken(t *testing.T) {
	url, _ := setup(t)
	if _, err := connect(t, url, "dgw_"+strings.Repeat("x", 43)); err == nil {
		t.Fatal("connect with a bad token must fail")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/mcpserver/`
Expected: FAIL — `undefined: Handler`, `undefined: New`.

- [ ] **Step 3: Write the implementation**

`internal/mcpserver/mcpserver.go`:

```go
// Package mcpserver exposes the agent tools over MCP streamable HTTP.
package mcpserver

import (
	"context"
	"errors"
	"net/http"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sebmahr/deploy-gateway/internal/agentauth"
	"github.com/sebmahr/deploy-gateway/internal/service"
)

const instructions = `deploy-gateway deploys containerized apps into your own workspace on a shared server.
Workflow: whoami (limits, port range, naming) -> list_templates (how services can be exposed) -> validate -> deploy -> list / logs.
Images must already be pushed to a registry with a pinned tag. You never supply compose files, volumes paths, labels or security options; the gateway renders a hardened configuration from the stack you send.`

func New(svc *service.Service, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "deploy-gateway", Version: version}, &mcp.ServerOptions{Instructions: instructions})
	destructive := true
	addTool(s, &mcp.Tool{Name: "whoami", Description: "Show your workspace, quotas, current usage, published port range and the container naming scheme.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, p service.Principal, _ service.NoInput) (service.WhoamiResult, error) { return svc.Whoami(ctx, p) })
	addTool(s, &mcp.Tool{Name: "list_templates", Description: "List the exposure templates granted to your workspace. Reference one per service via stack.services.<name>.expose.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, p service.Principal, _ service.NoInput) (service.TemplatesResult, error) {
			return svc.ListTemplates(ctx, p)
		})
	addTool(s, &mcp.Tool{Name: "validate", Description: "Dry-run a deploy: validates the stack, checks quotas and ports, and returns hostnames, URLs and resource totals. Starts nothing.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, p service.Principal, in service.DeployInput) (service.ValidateResult, error) {
			return svc.Validate(ctx, p, in)
		})
	addTool(s, &mcp.Tool{Name: "deploy", Description: "Create or update an app from prebuilt images. Pulls images, starts the services and waits for health checks."},
		func(ctx context.Context, p service.Principal, in service.DeployInput) (service.AppStatus, error) { return svc.Deploy(ctx, p, in) })
	addTool(s, &mcp.Tool{Name: "list", Description: "List your apps with service state, health, URLs, published ports and disk usage.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, p service.Principal, _ service.NoInput) (service.ListResult, error) { return svc.List(ctx, p) })
	addTool(s, &mcp.Tool{Name: "logs", Description: "Recent logs of one service of one of your apps.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, p service.Principal, in service.LogsInput) (service.LogsResult, error) { return svc.Logs(ctx, p, in) })
	addTool(s, &mcp.Tool{Name: "restart", Description: "Restart one service, or all services of an app when service is empty."},
		func(ctx context.Context, p service.Principal, in service.ServiceInput) (service.AppStatus, error) { return svc.Restart(ctx, p, in) })
	addTool(s, &mcp.Tool{Name: "stop", Description: "Stop one service, or all services of an app when service is empty. Data is kept."},
		func(ctx context.Context, p service.Principal, in service.ServiceInput) (service.AppStatus, error) { return svc.Stop(ctx, p, in) })
	addTool(s, &mcp.Tool{Name: "delete", Description: "Delete an app including its persistent data. confirm must equal the app name.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: &destructive}},
		func(ctx context.Context, p service.Principal, in service.DeleteInput) (service.DeleteResult, error) { return svc.Delete(ctx, p, in) })
	return s
}

func addTool[In, Out any](s *mcp.Server, t *mcp.Tool, fn func(context.Context, service.Principal, In) (Out, error)) {
	mcp.AddTool(s, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		p, err := principal(req)
		if err != nil {
			return nil, zero, err
		}
		out, err := fn(ctx, p, in)
		if err != nil {
			// service.Error.Error() is JSON, so the agent receives a structured error text.
			return nil, zero, err
		}
		return nil, out, nil
	})
}

func principal(req *mcp.CallToolRequest) (service.Principal, error) {
	if req.Extra == nil || req.Extra.TokenInfo == nil {
		return service.Principal{}, errors.New(`{"code":"unauthorized","message":"unauthorized"}`)
	}
	p, ok := req.Extra.TokenInfo.Extra["principal"].(service.Principal)
	if !ok {
		return service.Principal{}, errors.New(`{"code":"unauthorized","message":"unauthorized"}`)
	}
	return p, nil
}

// Handler serves stateless streamable HTTP behind bearer-token auth.
func Handler(srv *mcp.Server, a *agentauth.Authenticator) http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	return sdkauth.RequireBearerToken(a.MCPVerifier(), &sdkauth.RequireBearerTokenOptions{AllowMissingExpiration: true})(h)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/mcpserver/`
Expected: PASS. If `TestToolsOverStreamableHTTP` fails on output-schema validation, the offending result contains a `nil` slice: find it in `internal/service` and initialise it with `[]T{}` (Global Constraints).

- [ ] **Step 5: Commit**

```bash
git add internal/mcpserver
git commit -m "feat(mcp): expose agent tools over stateless streamable HTTP"
```

---

### Task 19: Admin API — server, authentication, SPA serving

**Files:**
- Create: `internal/webui/embed.go`, `internal/webui/dist/.keep` (empty), `internal/admin/server.go`, `internal/admin/workspaces.go` (stub), `internal/admin/misc.go` (stub)
- Modify: `.gitignore`
- Test: `internal/admin/server_test.go`

**Interfaces:**
- Consumes: `auth.Sessions`, `auth.Limiter`, `auth.CheckPassword`, `auth.ClientIP`, `auth.PeerTrusted` (11); `api.StatusFor` (17); `service.*` (14–16); `store.*` (9–10); `servicetest` (14)
- Produces:
  - `webui.FS() fs.FS` — embedded build output root (only `.keep` until the first frontend build)
  - `admin.Options{Svc *service.Service; Store *store.Store; PasswordHash string; TrustedProxies []netip.Prefix; UserHeader string; UI fs.FS; Logger *slog.Logger}`
  - `admin.New(o Options) *Server`, `(*Server).Handler() http.Handler`
  - Endpoints: `POST /api/admin/login` (password mode only, `{"password"}` → 204 + cookie), `POST /api/admin/logout` → 204, `GET /api/admin/me` → `{"actor","proxy_mode"}`
  - Auth error codes the frontend relies on: `login_required` (401, password mode: no/invalid session or wrong password), `unauthorized` (401, proxy mode: header missing), `forbidden` (403, proxy mode: untrusted peer), `rate_limited` (429)
  - Helpers used by Tasks 20–21: `apiError{Code, Message, Errors, Detail}`, `writeJSON(w, status, v)`, `writeErr(w, err)`, `badRequest(w, err)`, `notFound(w, what)`, `decodeJSON(w, r, v) error`, `actor(r) string`; route hooks `(*Server).workspaceRoutes(mux)`, `(*Server).miscRoutes(mux)`

- [ ] **Step 1: Create the embed package and ignore build output**

```bash
mkdir -p internal/webui/dist && touch internal/webui/dist/.keep
cat >> .gitignore <<'EOF'
/internal/webui/dist/*
!/internal/webui/dist/.keep
/web/node_modules/
EOF
```

`internal/webui/embed.go`:

```go
// Package webui embeds the built React admin app (web/ → internal/webui/dist).
package webui

import (
	"embed"
	"io/fs"
)

// dist always contains .keep so the package compiles before the first
// frontend build; the admin server reports "admin UI not built" until
// index.html exists.
//
//go:embed all:dist
var dist embed.FS

// FS returns the build output root.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
```

- [ ] **Step 2: Write the failing test**

`internal/admin/server_test.go`:

```go
package admin

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sebmahr/deploy-gateway/internal/auth"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/service/servicetest"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

var builtUI = fstest.MapFS{
	"index.html":        {Data: []byte(`<!doctype html><div id="root"></div>`)},
	"assets/app-123.js": {Data: []byte(`console.log(1)`)},
	"favicon.svg":       {Data: []byte(`<svg/>`)},
}

type testEnv struct {
	h      http.Handler
	svc    *service.Service
	st     *store.Store
	docker *servicetest.FakeDocker
	root   string
}

func newEnv(t *testing.T, proxy bool, ui fs.FS) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "admin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	compose, docker := &servicetest.FakeCompose{}, servicetest.NewFakeDocker()
	servicetest.Wire(compose, docker)
	docker.Networks["apps"] = true
	root := t.TempDir()
	svc := service.New(st, compose, docker, root)
	svc.Chown = func(string, int, int) error { return nil }
	o := Options{Svc: svc, Store: st, UI: ui}
	if proxy {
		o.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.1/32")}
		o.UserHeader = "Remote-User"
	} else {
		hash, err := auth.HashPassword("correct horse battery")
		if err != nil {
			t.Fatal(err)
		}
		o.PasswordHash = hash
	}
	return &testEnv{h: New(o).Handler(), svc: svc, st: st, docker: docker, root: root}
}

func (e *testEnv) do(method, path string, body any, mut ...func(*http.Request)) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	for _, m := range mut {
		m(r)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

func (e *testEnv) login(t *testing.T) func(*http.Request) {
	t.Helper()
	w := e.do("POST", "/api/admin/login", map[string]string{"password": "correct horse battery"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie = %+v", cookies)
	}
	c := cookies[0]
	return func(r *http.Request) { r.AddCookie(c) }
}

func TestPasswordModeSession(t *testing.T) {
	e := newEnv(t, false, builtUI)
	w := e.do("GET", "/api/admin/me", nil)
	if w.Code != http.StatusUnauthorized || decode[apiError](t, w).Code != "login_required" {
		t.Fatalf("me without session: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("POST", "/api/admin/login", map[string]string{"password": "wrong"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", w.Code)
	}
	if w := e.do("POST", "/api/admin/login", map[string]any{"password": "x", "admin": true}); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", w.Code)
	}
	c := e.login(t)
	w = e.do("GET", "/api/admin/me", nil, c)
	me := decode[map[string]any](t, w)
	if w.Code != 200 || me["actor"] != "admin" || me["proxy_mode"] != false || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("me: %d %v %v", w.Code, me, w.Header())
	}
	if w := e.do("POST", "/api/admin/logout", nil, c); w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := e.do("GET", "/api/admin/me", nil, c); w.Code != http.StatusUnauthorized {
		t.Fatal("session still valid after logout")
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t, false, builtUI)
	for i := 0; i < 10; i++ {
		e.do("POST", "/api/admin/login", map[string]string{"password": "wrong"})
	}
	if w := e.do("POST", "/api/admin/login", map[string]string{"password": "correct horse battery"}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", w.Code)
	}
}

func TestCrossOriginRejected(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	w := e.do("POST", "/api/admin/logout", nil, c, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST: %d", w.Code)
	}
}

func TestProxyMode(t *testing.T) {
	e := newEnv(t, true, builtUI)
	if w := e.do("GET", "/", nil); w.Code != http.StatusForbidden {
		t.Fatalf("untrusted peer must not even get the SPA: %d", w.Code)
	}
	trusted := func(r *http.Request) { r.RemoteAddr = "10.0.0.1:4444" }
	if w := e.do("GET", "/api/admin/me", nil, trusted); w.Code != http.StatusUnauthorized || decode[apiError](t, w).Code != "unauthorized" {
		t.Fatalf("missing header: %d %s", w.Code, w.Body.String())
	}
	user := func(r *http.Request) { trusted(r); r.Header.Set("Remote-User", "sebastian") }
	w := e.do("GET", "/api/admin/me", nil, user)
	me := decode[map[string]any](t, w)
	if w.Code != 200 || me["actor"] != "proxy:sebastian" || me["proxy_mode"] != true {
		t.Fatalf("me: %d %v", w.Code, me)
	}
	if w := e.do("POST", "/api/admin/login", map[string]string{"password": "x"}, user); w.Code != http.StatusNotFound {
		t.Fatalf("login must not exist in proxy mode: %d", w.Code)
	}
}

func TestSPAServing(t *testing.T) {
	e := newEnv(t, false, builtUI)
	w := e.do("GET", "/", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `id="root"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("index: %d %v", w.Code, w.Header())
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers: %v", w.Header())
	}
	if w := e.do("GET", "/workspaces/alice", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `id="root"`) {
		t.Fatalf("client route fallback: %d", w.Code)
	}
	w = e.do("GET", "/assets/app-123.js", nil)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %v", w.Code, w.Header())
	}
	if w := e.do("GET", "/assets/missing.js", nil); w.Code != http.StatusNotFound {
		t.Fatalf("missing asset: %d", w.Code)
	}
	if w := e.do("POST", "/", nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST to SPA: %d", w.Code)
	}
}

func TestUINotBuilt(t *testing.T) {
	e := newEnv(t, false, fstest.MapFS{".keep": {}})
	w := e.do("GET", "/", nil)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "admin UI not built") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestUnknownEndpointIsJSON404(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	w := e.do("GET", "/api/admin/nope", nil, c)
	if w.Code != http.StatusNotFound || decode[apiError](t, w).Code != "not_found" {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/admin/`
Expected: FAIL — `undefined: Options`, `undefined: New`.

- [ ] **Step 4: Write `server.go` and the two route stubs**

`internal/admin/server.go`:

```go
// Package admin serves the admin JSON API and the embedded React app
// (spec §14) on the admin listener. Authentication is either the built-in
// password login or a fail-closed trusted forward-auth proxy.
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"path"
	"strings"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/api"
	"github.com/sebmahr/deploy-gateway/internal/auth"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/stack"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

const cookieName = "dg_admin"

type Options struct {
	Svc            *service.Service
	Store          *store.Store
	PasswordHash   string // empty = proxy mode
	TrustedProxies []netip.Prefix
	UserHeader     string
	UI             fs.FS // built React app (index.html + assets/); may lack index.html
	Logger         *slog.Logger
}

type Server struct {
	opts     Options
	sessions *auth.Sessions
	limiter  *auth.Limiter
}

func New(o Options) *Server {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Server{opts: o, sessions: auth.NewSessions(12 * time.Hour), limiter: auth.NewLimiter(10, 15*time.Minute, 15*time.Minute)}
}

func (s *Server) proxyMode() bool { return s.opts.PasswordHash == "" }

// Handler routes /api/ to the admin API and everything else to the SPA.
// Password mode: the SPA and POST /api/admin/login are public, the rest of
// the API needs a session. Proxy mode: everything needs the trusted proxy.
func (s *Server) Handler() http.Handler {
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /api/admin/me", s.me)
	apiMux.HandleFunc("POST /api/admin/logout", s.logout)
	s.workspaceRoutes(apiMux)
	s.miscRoutes(apiMux)
	apiMux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, apiError{Code: service.CodeNotFound, Message: "no such endpoint"})
	})

	root := http.NewServeMux()
	var apiH http.Handler = apiMux
	if !s.proxyMode() {
		root.HandleFunc("POST /api/admin/login", s.login)
		apiH = s.requireAdmin(apiMux)
	}
	root.Handle("/api/", noStore(apiH))
	root.Handle("/", s.spa())

	var h http.Handler = root
	if s.proxyMode() {
		h = s.requireAdmin(root)
	}
	return securityHeaders(http.NewCrossOriginProtection().Handler(h))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; form-action 'self'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

type actorKey struct{}

func actor(r *http.Request) string {
	a, _ := r.Context().Value(actorKey{}).(string)
	return a
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var who string
		if s.proxyMode() {
			if !auth.PeerTrusted(r, s.opts.TrustedProxies) {
				writeJSON(w, http.StatusForbidden, apiError{Code: "forbidden", Message: "request did not come through the trusted proxy"})
				return
			}
			who = strings.TrimSpace(r.Header.Get(s.opts.UserHeader))
			if who == "" {
				writeJSON(w, http.StatusUnauthorized, apiError{Code: service.CodeUnauthorized, Message: "identity header missing"})
				return
			}
			who = "proxy:" + who
		} else {
			c, err := r.Cookie(cookieName)
			var ok bool
			if err == nil {
				who, ok = s.sessions.Lookup(c.Value)
			}
			if !ok {
				writeJSON(w, http.StatusUnauthorized, apiError{Code: "login_required", Message: "login required"})
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey{}, who)))
	})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"actor": actor(r), "proxy_mode": s.proxyMode()})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := auth.ClientIP(r, s.opts.TrustedProxies)
	if s.limiter.Blocked(ip) {
		writeJSON(w, http.StatusTooManyRequests, apiError{Code: "rate_limited", Message: "too many failed attempts; try again later"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		badRequest(w, err)
		return
	}
	if !auth.CheckPassword(s.opts.PasswordHash, body.Password) {
		s.limiter.Fail(ip)
		writeJSON(w, http.StatusUnauthorized, apiError{Code: "login_required", Message: "wrong password"})
		return
	}
	s.limiter.Reset(ip)
	id, err := s.sessions.Create("admin")
	if err != nil {
		writeErr(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: id, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

// spa serves built assets and falls back to index.html for client routes.
func (s *Server) spa() http.Handler {
	ui := s.opts.UI
	built := ui != nil && isFile(ui, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !built {
			http.Error(w, "admin UI not built: run `npm ci && npm run build` in web/", http.StatusServiceUnavailable)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		switch {
		case p != "" && p != "index.html" && isFile(ui, p):
			if strings.HasPrefix(p, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			http.ServeFileFS(w, r, ui, p)
		case strings.HasPrefix(p, "assets/"):
			http.NotFound(w, r)
		default:
			w.Header().Set("Cache-Control", "no-store")
			http.ServeFileFS(w, r, ui, "index.html")
		}
	})
}

func isFile(fsys fs.FS, name string) bool {
	st, err := fs.Stat(fsys, name)
	return err == nil && st.Mode().IsRegular()
}

// apiError is the admin error body. Unlike the agent API it includes the
// internal detail, because the admin is trusted.
type apiError struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Errors  stack.Errors `json:"errors,omitempty"`
	Detail  string       `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var se *service.Error
	if errors.As(err, &se) {
		writeJSON(w, api.StatusFor(se.Code), apiError{Code: se.Code, Message: se.Message, Errors: se.Fields, Detail: se.Detail()})
		return
	}
	writeJSON(w, http.StatusInternalServerError, apiError{Code: service.CodeInternal, Message: err.Error()})
}

func badRequest(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, apiError{Code: service.CodeValidation, Message: "invalid request body: " + err.Error()})
}

func notFound(w http.ResponseWriter, what string) {
	writeJSON(w, http.StatusNotFound, apiError{Code: service.CodeNotFound, Message: what + " not found"})
}

// decodeJSON rejects unknown fields, trailing data, empty and oversize bodies.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return errors.New("request body required")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after JSON object")
	}
	return nil
}
```

`internal/admin/workspaces.go` (stub, replaced in Task 20):

```go
package admin

import "net/http"

func (s *Server) workspaceRoutes(mux *http.ServeMux) {}
```

`internal/admin/misc.go` (stub, replaced in Task 21):

```go
package admin

import "net/http"

func (s *Server) miscRoutes(mux *http.ServeMux) {}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/admin/ ./internal/webui/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add .gitignore internal/webui internal/admin
git commit -m "feat(admin): admin API server with password/proxy auth and SPA serving"
```

---

### Task 20: Admin API — dashboard, workspaces, tokens, apps

**Files:**
- Create: `internal/admin/dto.go`
- Modify: `internal/admin/workspaces.go` (replace stub)
- Test: `internal/admin/workspaces_test.go`

**Interfaces:**
- Consumes: Task 19 helpers; `service.CreateWorkspace`, `UpdateWorkspace`, `DeleteWorkspace`, `SetGrants`, `CreateToken`, `RevokeToken`, `AdminApps`, `AdminStopApp`, `AdminDeleteApp`, `Orphans`, `Deploy` (14–16); `store.ListWorkspaces`, `WorkspaceUsage`, `ListTemplates`, `GrantedTemplates`, `ListTokens`, `ListAudit`, `InsertAudit` (9–10)
- Produces (JSON shapes are mirrored in `web/src/types.ts`, Task 22):
  - `GET /api/admin/dashboard` → `{workspaces: workspaceRow[], critical: auditDTO[], orphans: number}`
  - `GET /api/admin/workspaces` → `workspaceRow[]`; `POST` `{name, quota, ports}` → 201 `workspaceRow`
  - `GET /api/admin/workspaces/{name}` → `workspaceDetail`; `PUT` `{quota, ports}` → `workspaceDetail`; `DELETE` `{confirm}` → 204
  - `PUT /api/admin/workspaces/{name}/grants` `{template_ids}` → 204
  - `POST /api/admin/workspaces/{name}/tokens` `{label}` → 201 `{token}`; `POST .../tokens/{id}/revoke` → 204
  - `POST /api/admin/workspaces/{name}/apps/{app}/stop` → 204; `DELETE .../apps/{app}` `{confirm}` → 204
  - types `workspaceRow{name, quota, usage{apps, memory_mb, cpus}, ports{start, end, bind_ip}}`, `workspaceDetail` (row + `folder`, `templates[{id,name,host_pattern,granted}]`, `tokens[{id,label,prefix,created_at,last_used_at,revoked_at}]`, `apps: service.AppView[]`), `auditDTO{id,time,actor_type,actor,workspace,action,params(raw JSON),result,error,severity,duration_ms,client_ip}`, `toAuditDTOs`, `(*Server).row`, `(*Server).rows`

- [ ] **Step 1: Write the failing test**

`internal/admin/workspaces_test.go`:

```go
package admin

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sebmahr/deploy-gateway/internal/dockerx"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/stack"
	"github.com/sebmahr/deploy-gateway/internal/store"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
)

func workspaceBody(name string) map[string]any {
	return map[string]any{
		"name":  name,
		"quota": store.BuiltinDefaultQuota(),
		"ports": service.PortSettings{Start: 20000, End: 20099, BindIP: "0.0.0.0"},
	}
}

func publicWebTemplate() tmpl.Template {
	return tmpl.Template{Name: "public-web", Description: "Public HTTPS", HostPattern: "{{app}}.{{workspace}}.apps.example.com",
		Networks: []string{"apps"}, Labels: map[string]string{"traefik.enable": "true", "traefik.http.routers.{{id}}.rule": "Host(`{{host}}`)"}}
}

func TestWorkspaceLifecycle(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	w := e.do("POST", "/api/admin/workspaces", workspaceBody("alice"), c)
	if w.Code != http.StatusCreated || decode[workspaceRow](t, w).Name != "alice" {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(e.root, "alice")); err != nil {
		t.Fatalf("folder: %v", err)
	}
	if w := e.do("POST", "/api/admin/workspaces", workspaceBody("Bad-Name"), c); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(decode[apiError](t, w).Message, "workspace name must match") {
		t.Fatalf("bad name: %d %s", w.Code, w.Body.String())
	}
	if rows := decode[[]workspaceRow](t, e.do("GET", "/api/admin/workspaces", nil, c)); len(rows) != 1 || rows[0].Ports.Start != 20000 {
		t.Fatalf("list = %+v", rows)
	}

	bad := store.BuiltinDefaultQuota()
	bad.DefaultMemoryMB = 4096
	w = e.do("PUT", "/api/admin/workspaces/alice", map[string]any{"quota": bad, "ports": service.PortSettings{BindIP: "0.0.0.0"}}, c)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "default_memory_mb") {
		t.Fatalf("bad quota: %d %s", w.Code, w.Body.String())
	}
	good := store.BuiltinDefaultQuota()
	good.MaxApps = 9
	w = e.do("PUT", "/api/admin/workspaces/alice", map[string]any{"quota": good, "ports": service.PortSettings{BindIP: "127.0.0.1"}}, c)
	if d := decode[workspaceDetail](t, w); w.Code != 200 || d.Quota.MaxApps != 9 || d.Ports.BindIP != "127.0.0.1" {
		t.Fatalf("update: %d %+v", w.Code, d)
	}

	id, err := e.svc.SaveTemplate(context.Background(), "admin", 0, publicWebTemplate())
	if err != nil {
		t.Fatal(err)
	}
	if w := e.do("PUT", "/api/admin/workspaces/alice/grants", map[string]any{"template_ids": []int64{id}}, c); w.Code != http.StatusNoContent {
		t.Fatalf("grants: %d %s", w.Code, w.Body.String())
	}

	w = e.do("POST", "/api/admin/workspaces/alice/tokens", map[string]string{"label": "claude"}, c)
	tok := decode[map[string]string](t, w)["token"]
	if w.Code != http.StatusCreated || !strings.HasPrefix(tok, "dgw_") {
		t.Fatalf("token: %d %s", w.Code, w.Body.String())
	}
	w = e.do("GET", "/api/admin/workspaces/alice", nil, c)
	d := decode[workspaceDetail](t, w)
	if strings.Contains(w.Body.String(), tok) {
		t.Fatal("token must only be returned once")
	}
	if len(d.Tokens) != 1 || d.Tokens[0].Label != "claude" || len(d.Templates) != 1 || !d.Templates[0].Granted || d.Folder != filepath.Join(e.root, "alice") {
		t.Fatalf("detail = %+v", d)
	}
	if w := e.do("POST", "/api/admin/workspaces/alice/tokens/"+strconv.FormatInt(d.Tokens[0].ID, 10)+"/revoke", nil, c); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}

	if w := e.do("DELETE", "/api/admin/workspaces/alice", map[string]string{"confirm": "alic"}, c); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong confirm: %d", w.Code)
	}
	if w := e.do("DELETE", "/api/admin/workspaces/alice", map[string]string{"confirm": "alice"}, c); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("GET", "/api/admin/workspaces/alice", nil, c); w.Code != http.StatusNotFound {
		t.Fatalf("after delete: %d", w.Code)
	}
}

func TestAppsStopAndDelete(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	e.do("POST", "/api/admin/workspaces", workspaceBody("alice"), c)
	ws, _ := e.st.GetWorkspaceByName(context.Background(), "alice")
	st := stack.Stack{Services: map[string]stack.Service{"web": {Image: "nginx:1.27.0"}}}
	if _, err := e.svc.Deploy(context.Background(), service.Principal{Workspace: ws, TokenID: 1}, service.DeployInput{App: "shop", Stack: st}); err != nil {
		t.Fatal(err)
	}
	if w := e.do("POST", "/api/admin/workspaces/alice/apps/shop/stop", nil, c); w.Code != http.StatusNoContent {
		t.Fatalf("stop: %d %s", w.Code, w.Body.String())
	}
	d := decode[workspaceDetail](t, e.do("GET", "/api/admin/workspaces/alice", nil, c))
	if len(d.Apps) != 1 || d.Apps[0].Services[0].State != "exited" {
		t.Fatalf("apps = %+v", d.Apps)
	}
	if w := e.do("DELETE", "/api/admin/workspaces/alice/apps/shop", map[string]string{"confirm": "nope"}, c); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong confirm: %d", w.Code)
	}
	if w := e.do("DELETE", "/api/admin/workspaces/alice/apps/shop", map[string]string{"confirm": "shop"}, c); w.Code != http.StatusNoContent {
		t.Fatalf("delete app: %d %s", w.Code, w.Body.String())
	}
}

type dashboardResp struct {
	Workspaces []workspaceRow `json:"workspaces"`
	Critical   []auditDTO     `json:"critical"`
	Orphans    int            `json:"orphans"`
}

func TestDashboard(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	e.do("POST", "/api/admin/workspaces", workspaceBody("alice"), c)
	e.st.InsertAudit(context.Background(), store.AuditEntry{ActorType: "agent", Actor: "token:1", Workspace: "alice", Action: "deploy",
		Params: `{"app":"shop"}`, Result: "security_violation", Error: "rejected", Severity: "critical"})
	labels := map[string]string{"deploy-gateway.workspace": "ghost", "deploy-gateway.app": "x", "deploy-gateway.managed": "true"}
	e.docker.Add(dockerx.ContainerInfo{ID: "ghost-x-web", Name: "ghost-x-web", Labels: labels}, dockerx.Facts{})
	d := decode[dashboardResp](t, e.do("GET", "/api/admin/dashboard", nil, c))
	if len(d.Workspaces) != 1 || len(d.Critical) != 1 || string(d.Critical[0].Params) != `{"app":"shop"}` || d.Orphans != 1 {
		t.Fatalf("dashboard = %+v", d)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/admin/`
Expected: FAIL — `undefined: workspaceRow`, `undefined: workspaceDetail`, `undefined: auditDTO`.

- [ ] **Step 3: Write `dto.go`**

`internal/admin/dto.go`:

```go
package admin

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

// JSON shapes of the admin API; mirrored in web/src/types.ts.

type usageDTO struct {
	Apps     int     `json:"apps"`
	MemoryMB int64   `json:"memory_mb"`
	CPUs     float64 `json:"cpus"`
}

type workspaceRow struct {
	Name  string               `json:"name"`
	Quota store.Quota          `json:"quota"`
	Usage usageDTO             `json:"usage"`
	Ports service.PortSettings `json:"ports"`
}

type grantDTO struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	HostPattern string `json:"host_pattern"`
	Granted     bool   `json:"granted"`
}

type tokenDTO struct {
	ID         int64      `json:"id"`
	Label      string     `json:"label"`
	Prefix     string     `json:"prefix"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
}

type workspaceDetail struct {
	workspaceRow
	Folder    string            `json:"folder"`
	Templates []grantDTO        `json:"templates"`
	Tokens    []tokenDTO        `json:"tokens"`
	Apps      []service.AppView `json:"apps"`
}

type auditDTO struct {
	ID         int64           `json:"id"`
	Time       time.Time       `json:"time"`
	ActorType  string          `json:"actor_type"`
	Actor      string          `json:"actor"`
	Workspace  string          `json:"workspace"`
	Action     string          `json:"action"`
	Params     json.RawMessage `json:"params"`
	Result     string          `json:"result"`
	Error      string          `json:"error"`
	Severity   string          `json:"severity"`
	DurationMS int64           `json:"duration_ms"`
	ClientIP   string          `json:"client_ip"`
}

func toAuditDTOs(entries []store.AuditEntry) []auditDTO {
	out := make([]auditDTO, 0, len(entries))
	for _, e := range entries {
		params := json.RawMessage(e.Params)
		if !json.Valid(params) {
			params, _ = json.Marshal(e.Params)
		}
		out = append(out, auditDTO{ID: e.ID, Time: e.Time, ActorType: e.ActorType, Actor: e.Actor, Workspace: e.Workspace,
			Action: e.Action, Params: params, Result: e.Result, Error: e.Error, Severity: e.Severity,
			DurationMS: e.DurationMS, ClientIP: e.ClientIP})
	}
	return out
}

func (s *Server) row(ctx context.Context, w store.Workspace) (workspaceRow, error) {
	u, err := s.opts.Store.WorkspaceUsage(ctx, w.ID, "")
	if err != nil {
		return workspaceRow{}, err
	}
	return workspaceRow{
		Name: w.Name, Quota: w.Quota,
		Usage: usageDTO{Apps: u.Apps, MemoryMB: u.MemoryBytes >> 20, CPUs: u.CPUs},
		Ports: service.PortSettings{Start: w.PortRangeStart, End: w.PortRangeEnd, BindIP: w.PortBindIP},
	}, nil
}

func (s *Server) rows(ctx context.Context) ([]workspaceRow, error) {
	list, err := s.opts.Store.ListWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	out := []workspaceRow{}
	for _, w := range list {
		r, err := s.row(ctx, w)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
```

- [ ] **Step 4: Replace `workspaces.go`**

`internal/admin/workspaces.go`:

```go
package admin

import (
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

func (s *Server) workspaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/dashboard", s.dashboard)
	mux.HandleFunc("GET /api/admin/workspaces", s.listWorkspaces)
	mux.HandleFunc("POST /api/admin/workspaces", s.createWorkspace)
	mux.HandleFunc("GET /api/admin/workspaces/{name}", s.getWorkspace)
	mux.HandleFunc("PUT /api/admin/workspaces/{name}", s.updateWorkspace)
	mux.HandleFunc("DELETE /api/admin/workspaces/{name}", s.deleteWorkspace)
	mux.HandleFunc("PUT /api/admin/workspaces/{name}/grants", s.setGrants)
	mux.HandleFunc("POST /api/admin/workspaces/{name}/tokens", s.createToken)
	mux.HandleFunc("POST /api/admin/workspaces/{name}/tokens/{id}/revoke", s.revokeToken)
	mux.HandleFunc("POST /api/admin/workspaces/{name}/apps/{app}/stop", s.stopApp)
	mux.HandleFunc("DELETE /api/admin/workspaces/{name}/apps/{app}", s.deleteApp)
}

type confirmBody struct {
	Confirm string `json:"confirm"`
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.rows(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	crit, err := s.opts.Store.ListAudit(ctx, store.AuditFilter{Severity: "critical", Limit: 10})
	if err != nil {
		writeErr(w, err)
		return
	}
	orphans, err := s.opts.Svc.Orphans(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workspaces": rows, "critical": toAuditDTOs(crit), "orphans": len(orphans)})
}

func (s *Server) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	rows, err := s.rows(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  string               `json:"name"`
		Quota store.Quota          `json:"quota"`
		Ports service.PortSettings `json:"ports"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	ws, err := s.opts.Svc.CreateWorkspace(r.Context(), actor(r), in.Name, in.Quota, in.Ports)
	if err != nil {
		writeErr(w, err)
		return
	}
	row, err := s.row(r.Context(), ws)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (s *Server) loadWorkspace(w http.ResponseWriter, r *http.Request) (store.Workspace, bool) {
	ws, err := s.opts.Store.GetWorkspaceByName(r.Context(), r.PathValue("name"))
	if err != nil {
		notFound(w, "workspace")
		return store.Workspace{}, false
	}
	return ws, true
}

func (s *Server) detail(r *http.Request, ws store.Workspace) (workspaceDetail, error) {
	ctx := r.Context()
	row, err := s.row(ctx, ws)
	if err != nil {
		return workspaceDetail{}, err
	}
	all, err := s.opts.Store.ListTemplates(ctx)
	if err != nil {
		return workspaceDetail{}, err
	}
	granted, err := s.opts.Store.GrantedTemplates(ctx, ws.ID)
	if err != nil {
		return workspaceDetail{}, err
	}
	tokens, err := s.opts.Store.ListTokens(ctx, ws.ID)
	if err != nil {
		return workspaceDetail{}, err
	}
	apps, err := s.opts.Svc.AdminApps(ctx, ws)
	if err != nil {
		return workspaceDetail{}, err
	}
	d := workspaceDetail{workspaceRow: row, Folder: filepath.Join(s.opts.Svc.Root, ws.Name),
		Templates: []grantDTO{}, Tokens: []tokenDTO{}, Apps: apps}
	isGranted := map[int64]bool{}
	for _, g := range granted {
		isGranted[g.ID] = true
	}
	for _, t := range all {
		d.Templates = append(d.Templates, grantDTO{ID: t.ID, Name: t.Name, HostPattern: t.HostPattern, Granted: isGranted[t.ID]})
	}
	for _, t := range tokens {
		d.Tokens = append(d.Tokens, tokenDTO{ID: t.ID, Label: t.Label, Prefix: t.Prefix, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt})
	}
	return d, nil
}

func (s *Server) writeDetail(w http.ResponseWriter, r *http.Request, ws store.Workspace, status int) {
	d, err := s.detail(r, ws)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, status, d)
}

func (s *Server) getWorkspace(w http.ResponseWriter, r *http.Request) {
	if ws, ok := s.loadWorkspace(w, r); ok {
		s.writeDetail(w, r, ws, http.StatusOK)
	}
}

func (s *Server) updateWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	var in struct {
		Quota store.Quota          `json:"quota"`
		Ports service.PortSettings `json:"ports"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	ws.Quota, ws.PortRangeStart, ws.PortRangeEnd, ws.PortBindIP = in.Quota, in.Ports.Start, in.Ports.End, in.Ports.BindIP
	if err := s.opts.Svc.UpdateWorkspace(r.Context(), actor(r), ws); err != nil {
		writeErr(w, err)
		return
	}
	s.writeDetail(w, r, ws, http.StatusOK)
}

func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	var in confirmBody
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	if in.Confirm != ws.Name {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: service.CodeValidation, Message: "confirm must equal the workspace name"})
		return
	}
	if err := s.opts.Svc.DeleteWorkspace(r.Context(), actor(r), ws); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setGrants(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	var in struct {
		TemplateIDs []int64 `json:"template_ids"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	if err := s.opts.Svc.SetGrants(r.Context(), actor(r), ws, in.TemplateIDs); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	var in struct {
		Label string `json:"label"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	tok, err := s.opts.Svc.CreateToken(r.Context(), actor(r), ws, in.Label)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"token": tok})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		notFound(w, "token")
		return
	}
	if err := s.opts.Svc.RevokeToken(r.Context(), actor(r), ws, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) stopApp(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	if err := s.opts.Svc.AdminStopApp(r.Context(), actor(r), ws, r.PathValue("app")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteApp(w http.ResponseWriter, r *http.Request) {
	ws, ok := s.loadWorkspace(w, r)
	if !ok {
		return
	}
	var in confirmBody
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	app := r.PathValue("app")
	if in.Confirm != app {
		writeJSON(w, http.StatusUnprocessableEntity, apiError{Code: service.CodeValidation, Message: "confirm must equal the app name"})
		return
	}
	if err := s.opts.Svc.AdminDeleteApp(r.Context(), actor(r), ws, app); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/admin/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/admin
git commit -m "feat(admin): dashboard, workspace, token and app endpoints"
```

---

### Task 21: Admin API — templates with preview, audit, settings, orphans

**Files:**
- Create: `internal/admin/templates.go`
- Modify: `internal/admin/misc.go` (replace stub)
- Test: `internal/admin/misc_test.go`

**Interfaces:**
- Consumes: Task 19–20 helpers; `tmpl.Template`, `tmpl.Validate`, `tmpl.ValidationError`, `tmpl.Vars` (4); `service.SaveTemplate`, `DeleteTemplate`, `UpdateSettings`, `Orphans`, `RemoveOrphan` (14, 16); `store.ListTemplates`, `GetTemplate`, `ListAudit`, `DefaultQuota`, `AuditRetentionDays` (9–10); `names.LabelWorkspace`, `names.LabelApp` (1)
- Produces:
  - `GET /api/admin/templates` → `templateDTO[]`; `POST` (template JSON) → 201 `{id}`; `GET|PUT /api/admin/templates/{id}` → `templateDTO`; `DELETE` → 204
  - `POST /api/admin/templates/preview` `{id, template}` → `{valid, errors[], host, labels[{key,value}]}` (always 200 for a decodable body; never saves; no Docker network check)
  - `GET /api/admin/audit?workspace&action&result&severity&before&limit` → `{entries: auditDTO[], next_before}` (`next_before` 0 = no older page)
  - `GET|PUT /api/admin/settings` `{default_quota, audit_retention_days}`
  - `GET /api/admin/orphans` → `[{id,name,workspace,app,state,status}]`; `DELETE /api/admin/orphans/{id}` → 204

- [ ] **Step 1: Write the failing test**

`internal/admin/misc_test.go`:

```go
package admin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/sebmahr/deploy-gateway/internal/dockerx"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

func TestTemplatesCRUDAndPreview(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	draft := publicWebTemplate()
	draft.HostPattern = "{{app}}.apps.example.com"
	w := e.do("POST", "/api/admin/templates/preview", map[string]any{"id": 0, "template": draft}, c)
	p := decode[previewResponse](t, w)
	if w.Code != 200 || p.Valid || len(p.Errors) == 0 || !strings.Contains(strings.Join(p.Errors, ";"), "{{workspace}}") {
		t.Fatalf("invalid preview: %d %+v", w.Code, p)
	}
	draft.HostPattern = "{{app}}.{{workspace}}.apps.example.com"
	p = decode[previewResponse](t, e.do("POST", "/api/admin/templates/preview", map[string]any{"id": 0, "template": draft}, c))
	if !p.Valid || p.Host != "shop.alice.apps.example.com" || p.Labels[1].Key != "traefik.http.routers.alice-shop-web.rule" {
		t.Fatalf("valid preview: %+v", p)
	}
	if all, _ := e.st.ListTemplates(context.Background()); len(all) != 0 {
		t.Fatal("preview must not save")
	}

	w = e.do("POST", "/api/admin/templates", draft, c)
	id := decode[map[string]int64](t, w)["id"]
	if w.Code != http.StatusCreated || id == 0 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	path := "/api/admin/templates/" + strconv.FormatInt(id, 10)
	if got := decode[templateDTO](t, e.do("GET", path, nil, c)); got.Name != "public-web" || got.Labels["traefik.enable"] != "true" {
		t.Fatalf("get = %+v", got)
	}
	// Previewing the stored template while editing must not report an overlap with itself.
	if p := decode[previewResponse](t, e.do("POST", "/api/admin/templates/preview", map[string]any{"id": id, "template": draft}, c)); !p.Valid {
		t.Fatalf("self overlap: %+v", p)
	}
	draft.Description = "updated"
	if got := decode[templateDTO](t, e.do("PUT", path, draft, c)); got.Description != "updated" {
		t.Fatalf("update = %+v", got)
	}
	if list := decode[[]templateDTO](t, e.do("GET", "/api/admin/templates", nil, c)); len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if w := e.do("DELETE", path, nil, c); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("GET", path, nil, c); w.Code != http.StatusNotFound {
		t.Fatalf("after delete: %d", w.Code)
	}
}

func TestTemplateSaveRejectsMissingNetwork(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	tpl := publicWebTemplate()
	tpl.Networks = []string{"nope"}
	w := e.do("POST", "/api/admin/templates", tpl, c)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "does not exist") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestAuditFiltersAndPaging(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	for _, n := range []string{"alice", "bob", "carol"} {
		body := workspaceBody(n)
		body["ports"] = map[string]any{"start": 0, "end": 0, "bind_ip": "0.0.0.0"}
		if w := e.do("POST", "/api/admin/workspaces", body, c); w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d", n, w.Code)
		}
	}
	type page struct {
		Entries    []auditDTO `json:"entries"`
		NextBefore int64      `json:"next_before"`
	}
	p1 := decode[page](t, e.do("GET", "/api/admin/audit?action=create_workspace&limit=2", nil, c))
	if len(p1.Entries) != 2 || p1.NextBefore == 0 || p1.Entries[0].Workspace != "carol" {
		t.Fatalf("page 1 = %+v", p1)
	}
	p2 := decode[page](t, e.do("GET", "/api/admin/audit?action=create_workspace&limit=2&before="+strconv.FormatInt(p1.NextBefore, 10), nil, c))
	if len(p2.Entries) != 1 || p2.NextBefore != 0 || p2.Entries[0].Workspace != "alice" {
		t.Fatalf("page 2 = %+v", p2)
	}
	if p := decode[page](t, e.do("GET", "/api/admin/audit?workspace=bob", nil, c)); len(p.Entries) != 1 {
		t.Fatalf("workspace filter = %+v", p)
	}
}

func TestSettingsAPI(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	got := decode[settingsBody](t, e.do("GET", "/api/admin/settings", nil, c))
	if got.AuditRetentionDays != 90 || got.DefaultQuota != store.BuiltinDefaultQuota() {
		t.Fatalf("defaults = %+v", got)
	}
	got.DefaultQuota.MaxApps = 7
	got.AuditRetentionDays = 30
	if saved := decode[settingsBody](t, e.do("PUT", "/api/admin/settings", got, c)); saved.DefaultQuota.MaxApps != 7 || saved.AuditRetentionDays != 30 {
		t.Fatalf("saved = %+v", saved)
	}
	got.AuditRetentionDays = 0
	if w := e.do("PUT", "/api/admin/settings", got, c); w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid retention: %d", w.Code)
	}
}

func TestOrphansAPI(t *testing.T) {
	e := newEnv(t, false, builtUI)
	c := e.login(t)
	labels := map[string]string{"deploy-gateway.workspace": "ghost", "deploy-gateway.app": "x", "deploy-gateway.managed": "true"}
	e.docker.Add(dockerx.ContainerInfo{ID: "ghost-x-web", Name: "ghost-x-web", Labels: labels, State: "running"}, dockerx.Facts{})
	list := decode[[]orphanDTO](t, e.do("GET", "/api/admin/orphans", nil, c))
	if len(list) != 1 || list[0].Workspace != "ghost" {
		t.Fatalf("orphans = %+v", list)
	}
	if w := e.do("DELETE", "/api/admin/orphans/ghost-x-web", nil, c); w.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	if w := e.do("DELETE", "/api/admin/orphans/ghost-x-web", nil, c); w.Code != http.StatusNotFound {
		t.Fatalf("second remove: %d", w.Code)
	}
	if audit, _ := e.st.ListAudit(context.Background(), store.AuditFilter{Action: "remove_orphan"}); len(audit) != 2 {
		t.Fatalf("remove_orphan audit = %+v", audit)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/admin/`
Expected: FAIL — `undefined: previewResponse`, `undefined: templateDTO`, `undefined: settingsBody`, `undefined: orphanDTO`.

- [ ] **Step 3: Write `templates.go`**

`internal/admin/templates.go`:

```go
package admin

import (
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"

	"github.com/sebmahr/deploy-gateway/internal/store"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
)

type templateDTO struct {
	ID          int64             `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	HostPattern string            `json:"host_pattern"`
	Networks    []string          `json:"networks"`
	Labels      map[string]string `json:"labels"`
}

func toTemplateDTO(t store.StoredTemplate) templateDTO {
	d := templateDTO{ID: t.ID, Name: t.Name, Description: t.Description, HostPattern: t.HostPattern, Networks: t.Networks, Labels: t.Labels}
	if d.Networks == nil {
		d.Networks = []string{}
	}
	if d.Labels == nil {
		d.Labels = map[string]string{}
	}
	return d
}

type labelRow struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type previewResponse struct {
	Valid  bool       `json:"valid"`
	Errors []string   `json:"errors"`
	Host   string     `json:"host"`
	Labels []labelRow `json:"labels"`
}

func (s *Server) templateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/templates", s.listTemplates)
	mux.HandleFunc("POST /api/admin/templates", s.createTemplate)
	mux.HandleFunc("POST /api/admin/templates/preview", s.previewTemplate)
	mux.HandleFunc("GET /api/admin/templates/{id}", s.getTemplate)
	mux.HandleFunc("PUT /api/admin/templates/{id}", s.updateTemplate)
	mux.HandleFunc("DELETE /api/admin/templates/{id}", s.deleteTemplate)
}

func templateID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		notFound(w, "template")
		return 0, false
	}
	return id, true
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	all, err := s.opts.Store.ListTemplates(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]templateDTO, 0, len(all))
	for _, t := range all {
		out = append(out, toTemplateDTO(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := templateID(w, r)
	if !ok {
		return
	}
	t, err := s.opts.Store.GetTemplate(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w, "template")
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTemplateDTO(t))
}

func (s *Server) createTemplate(w http.ResponseWriter, r *http.Request) {
	var t tmpl.Template
	if err := decodeJSON(w, r, &t); err != nil {
		badRequest(w, err)
		return
	}
	id, err := s.opts.Svc.SaveTemplate(r.Context(), actor(r), 0, t)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) updateTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := templateID(w, r)
	if !ok {
		return
	}
	var t tmpl.Template
	if err := decodeJSON(w, r, &t); err != nil {
		badRequest(w, err)
		return
	}
	if _, err := s.opts.Svc.SaveTemplate(r.Context(), actor(r), id, t); err != nil {
		writeErr(w, err)
		return
	}
	stored, err := s.opts.Store.GetTemplate(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTemplateDTO(stored))
}

func (s *Server) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	id, ok := templateID(w, r)
	if !ok {
		return
	}
	if err := s.opts.Svc.DeleteTemplate(r.Context(), actor(r), id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// previewTemplate validates a draft (including hostname uniqueness against
// other stored templates) and renders it with sample values. It never saves
// and does not check Docker networks; that happens on save.
func (s *Server) previewTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID       int64         `json:"id"`
		Template tmpl.Template `json:"template"`
	}
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	all, err := s.opts.Store.ListTemplates(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	var others []tmpl.Template
	for _, o := range all {
		if o.ID != in.ID {
			others = append(others, o.Template)
		}
	}
	resp := previewResponse{Valid: true, Errors: []string{}, Labels: []labelRow{}}
	var verr tmpl.ValidationError
	if err := tmpl.Validate(in.Template, others); errors.As(err, &verr) {
		resp.Valid, resp.Errors = false, verr
	}
	vars := tmpl.Vars{Workspace: "alice", App: "shop", Service: "web", Port: 8080}
	resp.Host = in.Template.Host(vars)
	rendered := in.Template.RenderLabels(vars)
	for _, k := range slices.Sorted(maps.Keys(rendered)) {
		resp.Labels = append(resp.Labels, labelRow{Key: k, Value: rendered[k]})
	}
	writeJSON(w, http.StatusOK, resp)
}
```

- [ ] **Step 4: Replace `misc.go`**

`internal/admin/misc.go`:

```go
package admin

import (
	"net/http"
	"strconv"

	"github.com/sebmahr/deploy-gateway/internal/names"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

func (s *Server) miscRoutes(mux *http.ServeMux) {
	s.templateRoutes(mux)
	mux.HandleFunc("GET /api/admin/audit", s.audit)
	mux.HandleFunc("GET /api/admin/settings", s.getSettings)
	mux.HandleFunc("PUT /api/admin/settings", s.putSettings)
	mux.HandleFunc("GET /api/admin/orphans", s.listOrphans)
	mux.HandleFunc("DELETE /api/admin/orphans/{id}", s.removeOrphan)
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.AuditFilter{Workspace: q.Get("workspace"), Action: q.Get("action"), Result: q.Get("result"), Severity: q.Get("severity"), Limit: 100}
	f.BeforeID, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 0 && l <= 500 {
		f.Limit = l
	}
	entries, err := s.opts.Store.ListAudit(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	next := int64(0)
	if len(entries) == f.Limit {
		next = entries[len(entries)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": toAuditDTOs(entries), "next_before": next})
}

type settingsBody struct {
	DefaultQuota       store.Quota `json:"default_quota"`
	AuditRetentionDays int         `json:"audit_retention_days"`
}

func (s *Server) currentSettings(r *http.Request) (settingsBody, error) {
	q, err := s.opts.Store.DefaultQuota(r.Context())
	if err != nil {
		return settingsBody{}, err
	}
	days, err := s.opts.Store.AuditRetentionDays(r.Context())
	return settingsBody{DefaultQuota: q, AuditRetentionDays: days}, err
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	cur, err := s.currentSettings(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cur)
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsBody
	if err := decodeJSON(w, r, &in); err != nil {
		badRequest(w, err)
		return
	}
	if err := s.opts.Svc.UpdateSettings(r.Context(), actor(r), in.DefaultQuota, in.AuditRetentionDays); err != nil {
		writeErr(w, err)
		return
	}
	s.getSettings(w, r)
}

type orphanDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
	App       string `json:"app"`
	State     string `json:"state"`
	Status    string `json:"status"`
}

func (s *Server) listOrphans(w http.ResponseWriter, r *http.Request) {
	list, err := s.opts.Svc.Orphans(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]orphanDTO, 0, len(list))
	for _, c := range list {
		out = append(out, orphanDTO{ID: c.ID, Name: c.Name, Workspace: c.Labels[names.LabelWorkspace], App: c.Labels[names.LabelApp], State: c.State, Status: c.Status})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) removeOrphan(w http.ResponseWriter, r *http.Request) {
	if err := s.opts.Svc.RemoveOrphan(r.Context(), actor(r), r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/admin/ && go vet ./internal/admin/`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/admin
git commit -m "feat(admin): template preview, audit, settings and orphan endpoints"
```

---

### Task 22: React app — scaffold, API client, shell, login, dashboard

**Files:**
- Create: `web/package.json`, `web/package-lock.json` (generated), `web/tsconfig.json`, `web/vite.config.ts`, `web/postcss.config.cjs`, `web/index.html`, `web/src/main.tsx`, `web/src/App.tsx`, `web/src/theme.ts`, `web/src/types.ts`, `web/src/api.ts`, `web/src/components/ErrorAlert.tsx`, `web/src/components/Layout.tsx`, `web/src/components/AuditTable.tsx`, `web/src/pages/Login.tsx`, `web/src/pages/Dashboard.tsx`, `web/src/test/setup.ts`, `web/src/test/utils.tsx`
- Test: `web/src/api.test.ts`, `web/src/App.test.tsx`

**Interfaces:**
- Consumes: the admin API of Tasks 19–21 (shapes listed there)
- Produces:
  - `api` object (all admin endpoints), `ApiError{status, code, fields, detail}`, `errorMessage(e)` in `web/src/api.ts`
  - all DTO types in `web/src/types.ts` (`Quota`, `PortSettings`, `WorkspaceRow`, `WorkspaceDetail`, `AppView`, `Token`, `Grant`, `AuditEntry`, `Template`, `StoredTemplate`, `Preview`, `AuditFilter`, `AuditPage`, `Settings`, `Orphan`, `Me`, `Dashboard`)
  - `App` and `createQueryClient(opts?)` in `web/src/App.tsx`
  - components `ErrorAlert`, `Layout`, `AuditTable`; test helpers `renderWithProviders(ui, route?)`, `mockApi(routes) → Call[]`, `quota`

Versions are pinned exactly (verified together: `tsc`, `vitest`, `vite build` all pass). Notes for these majors: Mantine 9 `Grid` uses `gap` (not `gutter`); TypeScript 7 needs `vite/client` in `types` for CSS side-effect imports; jsdom needs `document.fonts`, `matchMedia` and `ResizeObserver` stubs for Mantine (in `setup.ts`).

- [ ] **Step 1: Create the project files**

`web/package.json`:

```json
{
  "name": "deploy-gateway-admin",
  "private": true,
  "version": "0.1.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "typecheck": "tsc -p .",
    "build": "tsc -p . && rm -rf ../internal/webui/dist/assets && vite build",
    "test": "vitest run"
  },
  "dependencies": {
    "@mantine/core": "9.6.3",
    "@mantine/form": "9.6.3",
    "@mantine/hooks": "9.6.3",
    "@mantine/notifications": "9.6.3",
    "@tanstack/react-query": "5.104.0",
    "react": "19.3.0",
    "react-dom": "19.3.0",
    "react-router": "8.4.0"
  },
  "devDependencies": {
    "@testing-library/dom": "10.4.2",
    "@testing-library/jest-dom": "7.0.1",
    "@testing-library/react": "16.3.3",
    "@testing-library/user-event": "14.6.7",
    "@types/react": "19.3.0",
    "@types/react-dom": "19.3.0",
    "@vitejs/plugin-react": "6.1.1",
    "jsdom": "30.1.1",
    "postcss": "8.5.28",
    "postcss-preset-mantine": "1.18.0",
    "postcss-simple-vars": "7.0.1",
    "typescript": "7.0.2",
    "vite": "8.3.2",
    "vitest": "5.0.3"
  }
}
```

`web/tsconfig.json`:

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "bundler",
    "jsx": "react-jsx",
    "strict": true,
    "noEmit": true,
    "skipLibCheck": true,
    "noUnusedLocals": true,
    "types": ["vite/client", "vitest/globals", "@testing-library/jest-dom"]
  },
  "include": ["src", "vite.config.ts"]
}
```

`web/vite.config.ts`:

```ts
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// The build is written into the Go embed directory; .keep stays so that
// `go test ./...` works without a frontend build.
export default defineConfig({
  plugins: [react()],
  build: { outDir: "../internal/webui/dist", emptyOutDir: false, chunkSizeWarningLimit: 1500 },
  server: { proxy: { "/api": "http://localhost:8081" } },
  test: { environment: "jsdom", globals: true, setupFiles: ["./src/test/setup.ts"], css: false },
});
```

`web/postcss.config.cjs`:

```js
module.exports = {
  plugins: {
    "postcss-preset-mantine": {},
    "postcss-simple-vars": {
      variables: {
        "mantine-breakpoint-xs": "36em",
        "mantine-breakpoint-sm": "48em",
        "mantine-breakpoint-md": "62em",
        "mantine-breakpoint-lg": "75em",
        "mantine-breakpoint-xl": "88em",
      },
    },
  },
};
```

`web/index.html`:

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>deploy-gateway admin</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

Then install (creates `web/package-lock.json`):

```bash
cd web && npm install --no-audit --no-fund && cd ..
```

- [ ] **Step 2: Write the test setup, helpers and failing tests**

`web/src/test/setup.ts`:

```ts
import "@testing-library/jest-dom/vitest";
import { afterEach, vi } from "vitest";

// jsdom lacks these browser APIs that Mantine uses.
Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addListener: () => {},
    removeListener: () => {},
    addEventListener: () => {},
    removeEventListener: () => {},
    dispatchEvent: () => false,
  }),
});
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
Object.defineProperty(window, "ResizeObserver", { writable: true, value: ResizeObserverStub });
window.HTMLElement.prototype.scrollIntoView = () => {};
Object.defineProperty(document, "fonts", {
  configurable: true,
  value: { addEventListener: () => {}, removeEventListener: () => {}, ready: Promise.resolve() },
});

afterEach(() => {
  vi.unstubAllGlobals();
});
```

`web/src/test/utils.tsx`:

```tsx
import { MantineProvider } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { MemoryRouter } from "react-router";
import { vi } from "vitest";
import { createQueryClient } from "../App";
import { theme } from "../theme";
import type { Quota } from "../types";

export function renderWithProviders(ui: ReactElement, route = "/") {
  const queryClient = createQueryClient({ retry: false });
  return render(
    <MantineProvider theme={theme} env="test">
      <Notifications />
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={[route]}>{ui}</MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

export interface Reply {
  status?: number;
  body?: unknown;
}
export type Handler = (body: any, url: URL) => Reply;
export interface Call {
  method: string;
  path: string;
  search: string;
  body: any;
}

/** Stubs fetch for /api/admin/*. Routes are keyed "METHOD /path" (path without /api/admin, without query). */
export function mockApi(routes: Record<string, Handler | Reply>): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      const method = (init?.method ?? "GET").toUpperCase();
      const path = url.pathname.replace(/^\/api\/admin/, "");
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, path, search: url.search, body });
      const route = routes[`${method} ${path}`];
      if (!route) {
        return new Response(JSON.stringify({ code: "not_found", message: `unmocked ${method} ${path}` }), { status: 404 });
      }
      const reply = typeof route === "function" ? route(body, url) : route;
      const status = reply.status ?? 200;
      return new Response(status === 204 ? null : JSON.stringify(reply.body ?? {}), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return calls;
}

export const quota: Quota = {
  max_apps: 5, max_services_per_app: 4, max_total_memory_mb: 2048, max_total_cpus: 2,
  default_memory_mb: 256, max_memory_mb: 1024, default_cpus: 0.5, max_cpus: 1, default_pids: 256, max_pids: 1024,
};
```

`web/src/api.test.ts`:

```ts
import { ApiError, api, errorMessage } from "./api";
import { mockApi } from "./test/utils";

test("ApiError carries code, fields and detail", async () => {
  mockApi({
    "POST /workspaces": {
      status: 422,
      body: { code: "validation_failed", message: "stack validation failed", errors: [{ field: "name", message: "too short" }], detail: "x" },
    },
  });
  const err = await api.createWorkspace({ name: "a", quota: {} as never, ports: { start: 0, end: 0, bind_ip: "0.0.0.0" } }).catch((e) => e);
  expect(err).toBeInstanceOf(ApiError);
  expect(err.status).toBe(422);
  expect(err.code).toBe("validation_failed");
  expect(errorMessage(err)).toBe("stack validation failed\nname: too short\nx");
});

test("204 resolves to undefined and audit builds a query string", async () => {
  const calls = mockApi({ "POST /logout": { status: 204 }, "GET /audit": { body: { entries: [], next_before: 0 } } });
  await expect(api.logout()).resolves.toBeUndefined();
  await api.audit({ workspace: "alice", severity: "", before: 0 });
  expect(calls[1].search).toBe("?workspace=alice");
});
```

`web/src/App.test.tsx`:

```tsx
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "./App";
import { mockApi, quota, renderWithProviders } from "./test/utils";

const dashboard = {
  workspaces: [{ name: "alice", quota, usage: { apps: 1, memory_mb: 256, cpus: 0.5 }, ports: { start: 20000, end: 20099, bind_ip: "0.0.0.0" } }],
  critical: [],
  orphans: 2,
};

test("login flow", async () => {
  let loggedIn = false;
  const calls = mockApi({
    "GET /me": () =>
      loggedIn ? { body: { actor: "admin", proxy_mode: false } } : { status: 401, body: { code: "login_required", message: "login required" } },
    "POST /login": (b) => {
      loggedIn = b.password === "correct horse battery";
      return loggedIn ? { status: 204 } : { status: 401, body: { code: "login_required", message: "wrong password" } };
    },
    "GET /dashboard": { body: dashboard },
  });
  renderWithProviders(<App />);
  const pw = await screen.findByLabelText(/admin password/i);
  await userEvent.type(pw, "nope");
  await userEvent.click(screen.getByRole("button", { name: "Log in" }));
  expect(await screen.findByText("wrong password")).toBeInTheDocument();
  await userEvent.clear(pw);
  await userEvent.type(pw, "correct horse battery");
  await userEvent.click(screen.getByRole("button", { name: "Log in" }));
  expect(await screen.findByRole("link", { name: "alice" })).toBeInTheDocument();
  expect(screen.getByText(/2 container\(s\)/)).toBeInTheDocument();
  expect(calls.filter((c) => c.path === "/login").at(-1)?.body).toEqual({ password: "correct horse battery" });
});

test("proxy-mode auth errors are shown instead of a login form", async () => {
  mockApi({ "GET /me": { status: 401, body: { code: "unauthorized", message: "identity header missing" } } });
  renderWithProviders(<App />);
  expect(await screen.findByText("identity header missing")).toBeInTheDocument();
  expect(screen.queryByLabelText(/admin password/i)).not.toBeInTheDocument();
});
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd web && npm test; cd ..`
Expected: FAIL — cannot resolve `./api`, `./App`.

- [ ] **Step 4: Write types, API client and theme**

`web/src/types.ts`:

```ts
// Mirrors the admin JSON API in internal/admin (Go).

export interface FieldError {
  field: string;
  message: string;
}

export interface Me {
  actor: string;
  proxy_mode: boolean;
}

export interface Quota {
  max_apps: number;
  max_services_per_app: number;
  max_total_memory_mb: number;
  max_total_cpus: number;
  default_memory_mb: number;
  max_memory_mb: number;
  default_cpus: number;
  max_cpus: number;
  default_pids: number;
  max_pids: number;
}

export interface PortSettings {
  start: number;
  end: number;
  bind_ip: string;
}

export interface Usage {
  apps: number;
  memory_mb: number;
  cpus: number;
}

export interface WorkspaceRow {
  name: string;
  quota: Quota;
  usage: Usage;
  ports: PortSettings;
}

export interface PortClaim {
  protocol: string;
  host_port: number;
  container_port: number;
  service: string;
  app: string;
}

export interface ServiceStatus {
  service: string;
  internal_host: string;
  url?: string;
  state: string;
  health?: string;
  ports: PortClaim[];
}

export interface AppView {
  app: string;
  services: ServiceStatus[];
  disk_bytes: number;
  last_deploy_at?: string;
  last_deploy_result: string;
}

export interface Grant {
  id: number;
  name: string;
  host_pattern: string;
  granted: boolean;
}

export interface Token {
  id: number;
  label: string;
  prefix: string;
  created_at: string;
  last_used_at: string | null;
  revoked_at: string | null;
}

export interface WorkspaceDetail extends WorkspaceRow {
  folder: string;
  templates: Grant[];
  tokens: Token[];
  apps: AppView[];
}

export interface AuditEntry {
  id: number;
  time: string;
  actor_type: string;
  actor: string;
  workspace: string;
  action: string;
  params: unknown;
  result: string;
  error: string;
  severity: string;
  duration_ms: number;
  client_ip: string;
}

export interface Dashboard {
  workspaces: WorkspaceRow[];
  critical: AuditEntry[];
  orphans: number;
}

export interface Template {
  name: string;
  description: string;
  host_pattern: string;
  networks: string[];
  labels: Record<string, string>;
}

export interface StoredTemplate extends Template {
  id: number;
}

export interface Preview {
  valid: boolean;
  errors: string[];
  host: string;
  labels: { key: string; value: string }[];
}

export interface AuditFilter {
  workspace?: string;
  action?: string;
  result?: string;
  severity?: string;
  before?: number;
}

export interface AuditPage {
  entries: AuditEntry[];
  next_before: number;
}

export interface Settings {
  default_quota: Quota;
  audit_retention_days: number;
}

export interface Orphan {
  id: string;
  name: string;
  workspace: string;
  app: string;
  state: string;
  status: string;
}
```

`web/src/api.ts`:

```ts
import type {
  AuditFilter, AuditPage, Dashboard, FieldError, Me, Orphan, PortSettings, Preview, Quota,
  Settings, StoredTemplate, Template, WorkspaceDetail, WorkspaceRow,
} from "./types";

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: FieldError[];
  readonly detail: string;

  constructor(status: number, code: string, message: string, fields: FieldError[] = [], detail = "") {
    super(message);
    this.status = status;
    this.code = code;
    this.fields = fields;
    this.detail = detail;
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/admin${path}`, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) {
    return undefined as T;
  }
  const text = await res.text();
  let data: any = undefined;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    data = undefined;
  }
  if (!res.ok) {
    throw new ApiError(res.status, data?.code ?? "error", data?.message ?? (text || res.statusText), data?.errors ?? [], data?.detail ?? "");
  }
  return data as T;
}

const enc = encodeURIComponent;

export const api = {
  me: () => request<Me>("GET", "/me"),
  login: (password: string) => request<void>("POST", "/login", { password }),
  logout: () => request<void>("POST", "/logout"),
  dashboard: () => request<Dashboard>("GET", "/dashboard"),

  workspaces: () => request<WorkspaceRow[]>("GET", "/workspaces"),
  workspace: (name: string) => request<WorkspaceDetail>("GET", `/workspaces/${enc(name)}`),
  createWorkspace: (body: { name: string; quota: Quota; ports: PortSettings }) =>
    request<WorkspaceRow>("POST", "/workspaces", body),
  updateWorkspace: (name: string, body: { quota: Quota; ports: PortSettings }) =>
    request<WorkspaceDetail>("PUT", `/workspaces/${enc(name)}`, body),
  deleteWorkspace: (name: string, confirm: string) => request<void>("DELETE", `/workspaces/${enc(name)}`, { confirm }),
  setGrants: (name: string, templateIDs: number[]) =>
    request<void>("PUT", `/workspaces/${enc(name)}/grants`, { template_ids: templateIDs }),
  createToken: (name: string, label: string) => request<{ token: string }>("POST", `/workspaces/${enc(name)}/tokens`, { label }),
  revokeToken: (name: string, id: number) => request<void>("POST", `/workspaces/${enc(name)}/tokens/${id}/revoke`),
  stopApp: (name: string, app: string) => request<void>("POST", `/workspaces/${enc(name)}/apps/${enc(app)}/stop`),
  deleteApp: (name: string, app: string, confirm: string) =>
    request<void>("DELETE", `/workspaces/${enc(name)}/apps/${enc(app)}`, { confirm }),

  templates: () => request<StoredTemplate[]>("GET", "/templates"),
  template: (id: number) => request<StoredTemplate>("GET", `/templates/${id}`),
  createTemplate: (t: Template) => request<{ id: number }>("POST", "/templates", t),
  updateTemplate: (id: number, t: Template) => request<StoredTemplate>("PUT", `/templates/${id}`, t),
  deleteTemplate: (id: number) => request<void>("DELETE", `/templates/${id}`),
  previewTemplate: (id: number, template: Template) => request<Preview>("POST", "/templates/preview", { id, template }),

  audit: (f: AuditFilter) => {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(f)) {
      if (v !== undefined && v !== "" && v !== 0) q.set(k, String(v));
    }
    const qs = q.toString();
    return request<AuditPage>("GET", `/audit${qs ? `?${qs}` : ""}`);
  },
  settings: () => request<Settings>("GET", "/settings"),
  saveSettings: (s: Settings) => request<Settings>("PUT", "/settings", s),
  orphans: () => request<Orphan[]>("GET", "/orphans"),
  removeOrphan: (id: string) => request<void>("DELETE", `/orphans/${enc(id)}`),
};

/** Human-readable text for any error, including field errors and admin detail. */
export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) {
    const lines = [e.message];
    for (const f of e.fields) {
      if (f.message !== e.message) lines.push(`${f.field}: ${f.message}`);
    }
    if (e.detail) lines.push(e.detail);
    return lines.join("\n");
  }
  return e instanceof Error ? e.message : String(e);
}
```

`web/src/theme.ts`:

```ts
import { createTheme } from "@mantine/core";

export const theme = createTheme({
  primaryColor: "indigo",
  defaultRadius: "md",
  fontFamily: "system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif",
});
```

- [ ] **Step 5: Write the components**

`web/src/components/ErrorAlert.tsx`:

```tsx
import { Alert } from "@mantine/core";
import { errorMessage } from "../api";

export function ErrorAlert({ error }: { error: unknown }) {
  if (!error) return null;
  return (
    <Alert color="red" title="Error" style={{ whiteSpace: "pre-wrap" }}>
      {errorMessage(error)}
    </Alert>
  );
}
```

`web/src/components/Layout.tsx`:

```tsx
import { ActionIcon, AppShell, Button, Group, NavLink, Text, Title, useComputedColorScheme, useMantineColorScheme } from "@mantine/core";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { Link, useLocation } from "react-router";
import { api } from "../api";
import type { Me } from "../types";

const links = [
  { to: "/", label: "Dashboard" },
  { to: "/workspaces", label: "Workspaces" },
  { to: "/templates", label: "Templates" },
  { to: "/audit", label: "Audit log" },
  { to: "/settings", label: "Settings" },
  { to: "/orphans", label: "Orphans" },
];

export function Layout({ me, children }: { me: Me; children: ReactNode }) {
  const location = useLocation();
  const queryClient = useQueryClient();
  const { setColorScheme } = useMantineColorScheme();
  const scheme = useComputedColorScheme("light");
  const logout = useMutation({ mutationFn: api.logout, onSuccess: () => queryClient.clear() });

  return (
    <AppShell header={{ height: 56 }} navbar={{ width: 220, breakpoint: "sm" }} padding="lg">
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between">
          <Title order={4}>deploy-gateway</Title>
          <Group gap="sm">
            <Text size="sm" c="dimmed">
              {me.actor}
            </Text>
            <ActionIcon
              variant="default"
              aria-label="Toggle color scheme"
              onClick={() => setColorScheme(scheme === "dark" ? "light" : "dark")}
            >
              {scheme === "dark" ? "☀" : "☾"}
            </ActionIcon>
            {!me.proxy_mode && (
              <Button variant="default" size="xs" onClick={() => logout.mutate()} loading={logout.isPending}>
                Log out
              </Button>
            )}
          </Group>
        </Group>
      </AppShell.Header>
      <AppShell.Navbar p="xs">
        {links.map((l) => (
          <NavLink
            key={l.to}
            component={Link}
            to={l.to}
            label={l.label}
            active={l.to === "/" ? location.pathname === "/" : location.pathname.startsWith(l.to)}
          />
        ))}
      </AppShell.Navbar>
      <AppShell.Main>{children}</AppShell.Main>
    </AppShell>
  );
}
```

`web/src/components/AuditTable.tsx`:

```tsx
import { Badge, Code, Table, Text } from "@mantine/core";
import type { AuditEntry } from "../types";

const severityColor: Record<string, string> = { info: "gray", warn: "yellow", critical: "red" };

export function AuditTable({ entries }: { entries: AuditEntry[] }) {
  if (entries.length === 0) {
    return <Text c="dimmed">No entries.</Text>;
  }
  return (
    <Table.ScrollContainer minWidth={900}>
      <Table striped verticalSpacing="xs">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Time</Table.Th>
            <Table.Th>Actor</Table.Th>
            <Table.Th>Workspace</Table.Th>
            <Table.Th>Action</Table.Th>
            <Table.Th>Result</Table.Th>
            <Table.Th>Error</Table.Th>
            <Table.Th>Params</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {entries.map((e) => (
            <Table.Tr key={e.id} bg={e.severity === "critical" ? "var(--mantine-color-red-light)" : undefined}>
              <Table.Td>{new Date(e.time).toLocaleString()}</Table.Td>
              <Table.Td>
                {e.actor}
                {e.client_ip && (
                  <Text size="xs" c="dimmed">
                    {e.client_ip}
                  </Text>
                )}
              </Table.Td>
              <Table.Td>{e.workspace}</Table.Td>
              <Table.Td>{e.action}</Table.Td>
              <Table.Td>
                <Badge color={e.result === "ok" ? "green" : severityColor[e.severity] ?? "gray"} variant="light">
                  {e.result}
                </Badge>
              </Table.Td>
              <Table.Td style={{ whiteSpace: "pre-wrap" }}>{e.error}</Table.Td>
              <Table.Td>
                <Code style={{ wordBreak: "break-all" }}>{JSON.stringify(e.params)}</Code>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Table.ScrollContainer>
  );
}
```

- [ ] **Step 6: Write the login and dashboard pages, the app and the entry point**

`web/src/pages/Login.tsx`:

```tsx
import { Button, Center, Paper, PasswordInput, Stack, Title } from "@mantine/core";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api";
import { ErrorAlert } from "../components/ErrorAlert";

export function Login() {
  const queryClient = useQueryClient();
  const [password, setPassword] = useState("");
  const login = useMutation({
    mutationFn: () => api.login(password),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["me"] }),
  });
  return (
    <Center h="100vh">
      <Paper withBorder shadow="sm" p="xl" w={380}>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            login.mutate();
          }}
        >
          <Stack>
            <Title order={3}>deploy-gateway</Title>
            <ErrorAlert error={login.error} />
            <PasswordInput label="Admin password" value={password} onChange={(e) => setPassword(e.currentTarget.value)} required autoFocus />
            <Button type="submit" loading={login.isPending}>
              Log in
            </Button>
          </Stack>
        </form>
      </Paper>
    </Center>
  );
}
```

`web/src/pages/Dashboard.tsx`:

```tsx
import { Alert, Anchor, Card, Loader, Progress, SimpleGrid, Stack, Text, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { api } from "../api";
import { AuditTable } from "../components/AuditTable";
import { ErrorAlert } from "../components/ErrorAlert";
import type { WorkspaceRow } from "../types";

const pct = (used: number, max: number) => (max > 0 ? Math.min(100, (used / max) * 100) : 0);

function Meter({ label, used, max, unit = "" }: { label: string; used: number; max: number; unit?: string }) {
  return (
    <div>
      <Text size="sm">
        {label}: {used}
        {unit} / {max}
        {unit}
      </Text>
      <Progress value={pct(used, max)} color={pct(used, max) > 90 ? "red" : "indigo"} size="sm" />
    </div>
  );
}

function WorkspaceCard({ w }: { w: WorkspaceRow }) {
  return (
    <Card withBorder>
      <Stack gap="xs">
        <Anchor component={Link} to={`/workspaces/${w.name}`} fw={600}>
          {w.name}
        </Anchor>
        <Meter label="Apps" used={w.usage.apps} max={w.quota.max_apps} />
        <Meter label="Memory" used={w.usage.memory_mb} max={w.quota.max_total_memory_mb} unit=" MB" />
        <Meter label="CPUs" used={Number(w.usage.cpus.toFixed(2))} max={w.quota.max_total_cpus} />
        <Text size="xs" c="dimmed">
          Ports: {w.ports.start > 0 ? `${w.ports.bind_ip}:${w.ports.start}-${w.ports.end}` : "publishing disabled"}
        </Text>
      </Stack>
    </Card>
  );
}

export function Dashboard() {
  const q = useQuery({ queryKey: ["dashboard"], queryFn: api.dashboard });
  if (q.isPending) return <Loader />;
  if (q.isError) return <ErrorAlert error={q.error} />;
  const d = q.data;
  return (
    <Stack>
      <Title order={2}>Dashboard</Title>
      {d.orphans > 0 && (
        <Alert color="orange" title="Orphaned containers">
          {d.orphans} container(s) carry gateway labels but belong to no known app.{" "}
          <Anchor component={Link} to="/orphans">
            Review them
          </Anchor>
        </Alert>
      )}
      {d.workspaces.length === 0 ? (
        <Text c="dimmed">
          No workspaces yet.{" "}
          <Anchor component={Link} to="/workspaces">
            Create one
          </Anchor>
        </Text>
      ) : (
        <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }}>
          {d.workspaces.map((w) => (
            <WorkspaceCard key={w.name} w={w} />
          ))}
        </SimpleGrid>
      )}
      <Title order={3}>Recent critical events</Title>
      <AuditTable entries={d.critical} />
    </Stack>
  );
}
```

`web/src/App.tsx` (Tasks 23–24 add the remaining routes):

```tsx
import { Center, Loader, Text } from "@mantine/core";
import { QueryCache, QueryClient, useQuery } from "@tanstack/react-query";
import { Route, Routes } from "react-router";
import { ApiError, api } from "./api";
import { ErrorAlert } from "./components/ErrorAlert";
import { Layout } from "./components/Layout";
import { Dashboard } from "./pages/Dashboard";
import { Login } from "./pages/Login";

/** A session that expires while the app is open sends the user back to the login form. */
export function createQueryClient(opts: { retry?: boolean } = {}): QueryClient {
  const client: QueryClient = new QueryClient({
    defaultOptions: { queries: { retry: opts.retry === false ? false : 1 }, mutations: { retry: false } },
    queryCache: new QueryCache({
      onError: (error, query) => {
        if (query.queryKey[0] !== "me" && error instanceof ApiError && error.code === "login_required") {
          client.invalidateQueries({ queryKey: ["me"] });
        }
      },
    }),
  });
  return client;
}

export function App() {
  const me = useQuery({ queryKey: ["me"], queryFn: api.me, retry: false });
  if (me.isPending) {
    return (
      <Center h="100vh">
        <Loader />
      </Center>
    );
  }
  if (me.isError) {
    if (me.error instanceof ApiError && me.error.code === "login_required") {
      return <Login />;
    }
    return (
      <Center h="100vh" p="xl">
        <ErrorAlert error={me.error} />
      </Center>
    );
  }
  return (
    <Layout me={me.data}>
      <Routes>
        <Route path="/" element={<Dashboard />} />
        <Route path="*" element={<Text>Page not found.</Text>} />
      </Routes>
    </Layout>
  );
}
```

`web/src/main.tsx`:

```tsx
import "@mantine/core/styles.css";
import "@mantine/notifications/styles.css";
import { MantineProvider } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { QueryClientProvider } from "@tanstack/react-query";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router";
import { App, createQueryClient } from "./App";
import { theme } from "./theme";

const queryClient = createQueryClient();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <MantineProvider theme={theme} defaultColorScheme="auto">
      <Notifications position="top-right" />
      <QueryClientProvider client={queryClient}>
        <BrowserRouter>
          <App />
        </BrowserRouter>
      </QueryClientProvider>
    </MantineProvider>
  </StrictMode>,
);
```

- [ ] **Step 7: Typecheck, test and build**

```bash
cd web && npm run typecheck && npm test && npm run build && cd ..
ls -a internal/webui/dist
go test ./internal/admin/ ./internal/webui/
git status --short internal/webui
```

Expected: typecheck and all tests pass; `internal/webui/dist` contains `.keep`, `index.html` and `assets/`; Go tests pass; `git status` shows nothing under `internal/webui/dist` except possibly `.keep` (build output is ignored).

- [ ] **Step 8: Commit**

```bash
git add web/package.json web/package-lock.json web/tsconfig.json web/vite.config.ts web/postcss.config.cjs web/index.html web/src
git commit -m "feat(web): React admin scaffold with login, layout and dashboard"
```

---

### Task 23: React app — workspaces, workspace detail, tokens

**Files:**
- Create: `web/src/components/QuotaFields.tsx`, `web/src/components/PortFields.tsx`, `web/src/components/ConfirmDelete.tsx`, `web/src/pages/Workspaces.tsx`, `web/src/pages/WorkspaceDetail.tsx`
- Modify: `web/src/App.tsx` (add routes)
- Test: `web/src/pages/Workspaces.test.tsx`, `web/src/pages/WorkspaceDetail.test.tsx`

**Interfaces:**
- Consumes: Task 22 (`api`, types, `ErrorAlert`, test helpers)
- Produces: `QuotaFields({value, onChange})`, `PortFields({value, onChange})`, `ConfirmDelete({name, label, loading?, onConfirm})` (`QuotaFields` is reused by Task 24's Settings page), pages `Workspaces`, `WorkspaceDetail`

- [ ] **Step 1: Write the failing tests**

`web/src/pages/Workspaces.test.tsx`:

```tsx
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { mockApi, quota, renderWithProviders } from "../test/utils";
import { Workspaces } from "./Workspaces";

test("create workspace with defaults from settings", async () => {
  const calls = mockApi({
    "GET /workspaces": { body: [] },
    "GET /settings": { body: { default_quota: quota, audit_retention_days: 90 } },
    "POST /workspaces": (b) =>
      b.name === "Bad-Name"
        ? { status: 422, body: { code: "validation_failed", message: "workspace name must match ^[a-z][a-z0-9]{1,15}$" } }
        : { status: 201, body: { name: b.name, quota: b.quota, usage: { apps: 0, memory_mb: 0, cpus: 0 }, ports: b.ports } },
  });
  renderWithProviders(
    <Routes>
      <Route path="/workspaces" element={<Workspaces />} />
      <Route path="/workspaces/:name" element={<div>detail page</div>} />
    </Routes>,
    "/workspaces",
  );
  await userEvent.click(await screen.findByRole("button", { name: "New workspace" }));
  const name = await screen.findByLabelText(/^Name/);
  await userEvent.type(name, "Bad-Name");
  await userEvent.click(screen.getByRole("button", { name: "Create workspace" }));
  expect(await screen.findByText(/workspace name must match/)).toBeInTheDocument();

  await userEvent.clear(name);
  await userEvent.type(name, "alice");
  const maxApps = screen.getByLabelText("Max apps");
  await userEvent.clear(maxApps);
  await userEvent.type(maxApps, "3");
  await userEvent.click(screen.getByRole("button", { name: "Create workspace" }));
  expect(await screen.findByText("detail page")).toBeInTheDocument();
  const post = calls.filter((c) => c.method === "POST").at(-1)!;
  expect(post.body).toEqual({ name: "alice", quota: { ...quota, max_apps: 3 }, ports: { start: 0, end: 0, bind_ip: "0.0.0.0" } });
});
```

`web/src/pages/WorkspaceDetail.test.tsx`:

```tsx
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { mockApi, quota, renderWithProviders } from "../test/utils";
import type { WorkspaceDetail as Detail } from "../types";
import { WorkspaceDetail } from "./WorkspaceDetail";

const detail: Detail = {
  name: "alice",
  quota,
  usage: { apps: 1, memory_mb: 256, cpus: 0.5 },
  ports: { start: 20000, end: 20099, bind_ip: "0.0.0.0" },
  folder: "/srv/workspaces/alice",
  templates: [{ id: 1, name: "public-web", host_pattern: "{{app}}.{{workspace}}.apps.example.com", granted: false }],
  tokens: [],
  apps: [
    {
      app: "shop",
      disk_bytes: 0,
      last_deploy_result: "ok",
      last_deploy_at: "2026-10-01T10:00:00Z",
      services: [{ service: "web", internal_host: "alice-shop-web", url: "https://shop.alice.apps.example.com", state: "running", ports: [] }],
    },
  ],
};

function renderDetail() {
  renderWithProviders(
    <Routes>
      <Route path="/workspaces/:name" element={<WorkspaceDetail />} />
    </Routes>,
    "/workspaces/alice",
  );
}

test("a new token is shown exactly once", async () => {
  mockApi({
    "GET /workspaces/alice": { body: detail },
    "POST /workspaces/alice/tokens": { status: 201, body: { token: "dgw_secretsecretsecret" } },
  });
  renderDetail();
  await userEvent.click(await screen.findByRole("tab", { name: "Tokens" }));
  await userEvent.type(screen.getByLabelText(/Token label/), "claude");
  await userEvent.click(screen.getByRole("button", { name: "Create token" }));
  expect(await screen.findByText("dgw_secretsecretsecret")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "I have stored it" }));
  expect(screen.queryByText("dgw_secretsecretsecret")).not.toBeInTheDocument();
});

test("deleting an app requires typing its name", async () => {
  const calls = mockApi({
    "GET /workspaces/alice": { body: detail },
    "DELETE /workspaces/alice/apps/shop": { status: 204 },
  });
  renderDetail();
  expect(await screen.findByText("https://shop.alice.apps.example.com")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Delete" }));
  const confirmButton = await screen.findByRole("button", { name: "Delete app" });
  expect(confirmButton).toBeDisabled();
  await userEvent.type(screen.getByLabelText("Type shop to confirm"), "shop");
  expect(confirmButton).toBeEnabled();
  await userEvent.click(confirmButton);
  expect(await screen.findByText("App deleted")).toBeInTheDocument();
  expect(calls.find((c) => c.method === "DELETE")?.body).toEqual({ confirm: "shop" });
});

test("grants are saved as template ids", async () => {
  const calls = mockApi({ "GET /workspaces/alice": { body: detail }, "PUT /workspaces/alice/grants": { status: 204 } });
  renderDetail();
  await userEvent.click(await screen.findByRole("tab", { name: "Templates" }));
  await userEvent.click(screen.getByLabelText(/public-web/));
  await userEvent.click(screen.getByRole("button", { name: "Save grants" }));
  expect(await screen.findByText("Grants saved")).toBeInTheDocument();
  expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ template_ids: [1] });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm test; cd ..`
Expected: FAIL — cannot resolve `./Workspaces`, `./WorkspaceDetail`.

- [ ] **Step 3: Write the shared form components**

`web/src/components/QuotaFields.tsx`:

```tsx
import { NumberInput, SimpleGrid } from "@mantine/core";
import type { Quota } from "../types";

const fields: { key: keyof Quota; label: string; decimals?: boolean }[] = [
  { key: "max_apps", label: "Max apps" },
  { key: "max_services_per_app", label: "Max services per app" },
  { key: "max_total_memory_mb", label: "Total memory (MB)" },
  { key: "max_total_cpus", label: "Total CPUs", decimals: true },
  { key: "default_memory_mb", label: "Default memory per service (MB)" },
  { key: "max_memory_mb", label: "Max memory per service (MB)" },
  { key: "default_cpus", label: "Default CPUs per service", decimals: true },
  { key: "max_cpus", label: "Max CPUs per service", decimals: true },
  { key: "default_pids", label: "Default pids per service" },
  { key: "max_pids", label: "Max pids per service" },
];

export function QuotaFields({ value, onChange }: { value: Quota; onChange: (q: Quota) => void }) {
  return (
    <SimpleGrid cols={{ base: 1, sm: 2 }}>
      {fields.map((f) => (
        <NumberInput
          key={f.key}
          label={f.label}
          value={value[f.key]}
          min={0}
          step={f.decimals ? 0.1 : 1}
          decimalScale={f.decimals ? 2 : 0}
          onChange={(v) => onChange({ ...value, [f.key]: typeof v === "number" ? v : Number(v) || 0 })}
        />
      ))}
    </SimpleGrid>
  );
}
```

`web/src/components/PortFields.tsx`:

```tsx
import { Group, NumberInput, Stack, Text, TextInput } from "@mantine/core";
import type { PortSettings } from "../types";

export function PortFields({ value, onChange }: { value: PortSettings; onChange: (p: PortSettings) => void }) {
  return (
    <Stack gap={4}>
      <Group grow align="start">
        <NumberInput
          label="Published ports from"
          value={value.start}
          min={0}
          max={65535}
          onChange={(v) => onChange({ ...value, start: Number(v) || 0 })}
        />
        <NumberInput label="to" value={value.end} min={0} max={65535} onChange={(v) => onChange({ ...value, end: Number(v) || 0 })} />
        <TextInput label="Bind IP" value={value.bind_ip} onChange={(e) => onChange({ ...value, bind_ip: e.currentTarget.value })} />
      </Group>
      <Text size="xs" c="dimmed">
        0 to 0 disables publishing. Published ports bypass the host firewall (ufw); choose the bind IP deliberately.
      </Text>
    </Stack>
  );
}
```

`web/src/components/ConfirmDelete.tsx`:

```tsx
import { Button, Group, TextInput } from "@mantine/core";
import { useState } from "react";

/** A destructive action that is only enabled after typing the exact name. */
export function ConfirmDelete({ name, label, loading, onConfirm }: {
  name: string;
  label: string;
  loading?: boolean;
  onConfirm: (confirm: string) => void;
}) {
  const [typed, setTyped] = useState("");
  return (
    <Group align="end">
      <TextInput label={`Type ${name} to confirm`} value={typed} onChange={(e) => setTyped(e.currentTarget.value)} />
      <Button color="red" disabled={typed !== name} loading={loading} onClick={() => onConfirm(typed)}>
        {label}
      </Button>
    </Group>
  );
}
```

- [ ] **Step 4: Write the pages**

`web/src/pages/Workspaces.tsx`:

```tsx
import { Anchor, Button, Group, Loader, Modal, Stack, Table, Text, TextInput, Title } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router";
import { api } from "../api";
import { ErrorAlert } from "../components/ErrorAlert";
import { PortFields } from "../components/PortFields";
import { QuotaFields } from "../components/QuotaFields";
import type { PortSettings, Quota } from "../types";

function CreateWorkspaceForm({ onDone }: { onDone: () => void }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const settings = useQuery({ queryKey: ["settings"], queryFn: api.settings });
  const [name, setName] = useState("");
  const [quota, setQuota] = useState<Quota | null>(null);
  const [ports, setPorts] = useState<PortSettings>({ start: 0, end: 0, bind_ip: "0.0.0.0" });
  useEffect(() => {
    if (settings.data && quota === null) setQuota(settings.data.default_quota);
  }, [settings.data, quota]);
  const create = useMutation({
    mutationFn: () => api.createWorkspace({ name, quota: quota!, ports }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["workspaces"] });
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notifications.show({ color: "green", message: `Workspace ${name} created` });
      onDone();
      navigate(`/workspaces/${name}`);
    },
  });
  if (settings.isError) return <ErrorAlert error={settings.error} />;
  if (quota === null) return <Loader />;
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate();
      }}
    >
      <Stack>
        <ErrorAlert error={create.error} />
        <TextInput
          label="Name"
          description="Lowercase letters and digits, 2-16 characters, no hyphens"
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          required
        />
        <QuotaFields value={quota} onChange={setQuota} />
        <PortFields value={ports} onChange={setPorts} />
        <Group justify="flex-end">
          <Button type="submit" loading={create.isPending}>
            Create workspace
          </Button>
        </Group>
      </Stack>
    </form>
  );
}

export function Workspaces() {
  const q = useQuery({ queryKey: ["workspaces"], queryFn: api.workspaces });
  const [opened, { open, close }] = useDisclosure(false);
  return (
    <Stack>
      <Group justify="space-between">
        <Title order={2}>Workspaces</Title>
        <Button onClick={open}>New workspace</Button>
      </Group>
      <Modal opened={opened} onClose={close} title="New workspace" size="lg">
        <CreateWorkspaceForm onDone={close} />
      </Modal>
      {q.isPending && <Loader />}
      <ErrorAlert error={q.error} />
      {q.data && q.data.length === 0 && <Text c="dimmed">No workspaces yet.</Text>}
      {q.data && q.data.length > 0 && (
        <Table striped highlightOnHover>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Workspace</Table.Th>
              <Table.Th>Apps</Table.Th>
              <Table.Th>Memory (MB)</Table.Th>
              <Table.Th>CPUs</Table.Th>
              <Table.Th>Published ports</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {q.data.map((w) => (
              <Table.Tr key={w.name}>
                <Table.Td>
                  <Anchor component={Link} to={`/workspaces/${w.name}`}>
                    {w.name}
                  </Anchor>
                </Table.Td>
                <Table.Td>
                  {w.usage.apps} / {w.quota.max_apps}
                </Table.Td>
                <Table.Td>
                  {w.usage.memory_mb} / {w.quota.max_total_memory_mb}
                </Table.Td>
                <Table.Td>
                  {w.usage.cpus.toFixed(2)} / {w.quota.max_total_cpus}
                </Table.Td>
                <Table.Td>{w.ports.start > 0 ? `${w.ports.bind_ip}:${w.ports.start}-${w.ports.end}` : "disabled"}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
    </Stack>
  );
}
```

`web/src/pages/WorkspaceDetail.tsx`:

```tsx
import {
  Alert, Anchor, Badge, Button, Checkbox, Code, CopyButton, Group, Loader, Modal, Stack, Table, Tabs, Text, TextInput, Title,
} from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { api } from "../api";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { ErrorAlert } from "../components/ErrorAlert";
import { PortFields } from "../components/PortFields";
import { QuotaFields } from "../components/QuotaFields";
import type { AppView, WorkspaceDetail as Detail } from "../types";

const stateColor = (s: string) => (s === "running" ? "green" : s === "missing" ? "gray" : "orange");
const fmt = (t: string | null | undefined) => (t ? new Date(t).toLocaleString() : "never");

function useInvalidate(name: string) {
  const queryClient = useQueryClient();
  return () => {
    queryClient.invalidateQueries({ queryKey: ["workspace", name] });
    queryClient.invalidateQueries({ queryKey: ["workspaces"] });
    queryClient.invalidateQueries({ queryKey: ["dashboard"] });
  };
}

function AppsTab({ ws }: { ws: Detail }) {
  const invalidate = useInvalidate(ws.name);
  const [deleting, setDeleting] = useState<AppView | null>(null);
  const stop = useMutation({ mutationFn: (app: string) => api.stopApp(ws.name, app), onSuccess: invalidate });
  const del = useMutation({
    mutationFn: ({ app, confirm }: { app: string; confirm: string }) => api.deleteApp(ws.name, app, confirm),
    onSuccess: () => {
      setDeleting(null);
      notifications.show({ color: "green", message: "App deleted" });
      invalidate();
    },
  });
  if (ws.apps.length === 0) return <Text c="dimmed">No apps deployed.</Text>;
  return (
    <Stack>
      <ErrorAlert error={stop.error} />
      <Table striped verticalSpacing="sm">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>App</Table.Th>
            <Table.Th>Services</Table.Th>
            <Table.Th>Disk</Table.Th>
            <Table.Th>Last deploy</Table.Th>
            <Table.Th />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {ws.apps.map((a) => (
            <Table.Tr key={a.app}>
              <Table.Td fw={600}>{a.app}</Table.Td>
              <Table.Td>
                <Stack gap={4}>
                  {a.services.map((s) => (
                    <Group key={s.service} gap="xs">
                      <Code>{s.internal_host}</Code>
                      <Badge color={stateColor(s.state)} variant="light">
                        {s.state}
                        {s.health ? ` · ${s.health}` : ""}
                      </Badge>
                      {s.url && (
                        <Anchor href={s.url} target="_blank" size="sm">
                          {s.url}
                        </Anchor>
                      )}
                      {s.ports.map((p) => (
                        <Text key={`${p.protocol}${p.host_port}`} size="xs" c="dimmed">
                          {p.host_port}/{p.protocol} → {p.container_port}
                        </Text>
                      ))}
                    </Group>
                  ))}
                </Stack>
              </Table.Td>
              <Table.Td>{Math.round(a.disk_bytes / 1048576)} MB</Table.Td>
              <Table.Td>
                {a.last_deploy_at ? new Date(a.last_deploy_at).toLocaleString() : "-"}{" "}
                <Badge color={a.last_deploy_result === "ok" ? "green" : "red"} variant="light">
                  {a.last_deploy_result || "-"}
                </Badge>
              </Table.Td>
              <Table.Td>
                <Group gap="xs" wrap="nowrap">
                  <Button size="xs" variant="default" loading={stop.isPending && stop.variables === a.app} onClick={() => stop.mutate(a.app)}>
                    Stop
                  </Button>
                  <Button size="xs" color="red" variant="light" onClick={() => setDeleting(a)}>
                    Delete
                  </Button>
                </Group>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      <Modal opened={deleting !== null} onClose={() => setDeleting(null)} title={`Delete app ${deleting?.app ?? ""}`}>
        {deleting && (
          <Stack>
            <Text size="sm">Stops and removes all containers and deletes the app's data folder. This cannot be undone.</Text>
            <ErrorAlert error={del.error} />
            <ConfirmDelete
              name={deleting.app}
              label="Delete app"
              loading={del.isPending}
              onConfirm={(confirm) => del.mutate({ app: deleting.app, confirm })}
            />
          </Stack>
        )}
      </Modal>
    </Stack>
  );
}

function TokensTab({ ws }: { ws: Detail }) {
  const invalidate = useInvalidate(ws.name);
  const [label, setLabel] = useState("");
  const [newToken, setNewToken] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: () => api.createToken(ws.name, label),
    onSuccess: (res) => {
      setNewToken(res.token);
      setLabel("");
      invalidate();
    },
  });
  const revoke = useMutation({ mutationFn: (id: number) => api.revokeToken(ws.name, id), onSuccess: invalidate });
  return (
    <Stack>
      <ErrorAlert error={create.error ?? revoke.error} />
      {ws.tokens.length > 0 && (
        <Table striped>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Label</Table.Th>
              <Table.Th>Prefix</Table.Th>
              <Table.Th>Created</Table.Th>
              <Table.Th>Last used</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {ws.tokens.map((t) => (
              <Table.Tr key={t.id}>
                <Table.Td>{t.label}</Table.Td>
                <Table.Td>
                  <Code>{t.prefix}…</Code>
                </Table.Td>
                <Table.Td>{fmt(t.created_at)}</Table.Td>
                <Table.Td>{fmt(t.last_used_at)}</Table.Td>
                <Table.Td>
                  {t.revoked_at ? (
                    <Badge color="gray">revoked</Badge>
                  ) : (
                    <Button size="xs" color="red" variant="light" onClick={() => revoke.mutate(t.id)}>
                      Revoke
                    </Button>
                  )}
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <Group align="end">
          <TextInput label="Token label" placeholder="e.g. claude-laptop" value={label} onChange={(e) => setLabel(e.currentTarget.value)} required />
          <Button type="submit" loading={create.isPending}>
            Create token
          </Button>
        </Group>
      </form>
      <Modal opened={newToken !== null} onClose={() => setNewToken(null)} title="New token" size="lg" closeOnClickOutside={false}>
        <Stack>
          <Alert color="yellow">Copy this token now. It is shown only once and stored only as a hash.</Alert>
          <Code block>{newToken}</Code>
          <Group justify="space-between">
            <CopyButton value={newToken ?? ""}>
              {({ copied, copy }) => (
                <Button variant="light" onClick={copy}>
                  {copied ? "Copied" : "Copy token"}
                </Button>
              )}
            </CopyButton>
            <Button onClick={() => setNewToken(null)}>I have stored it</Button>
          </Group>
          <Text size="sm" c="dimmed">
            Agents connect to https://&lt;agent-host&gt;/mcp with the header Authorization: Bearer &lt;token&gt;.
          </Text>
        </Stack>
      </Modal>
    </Stack>
  );
}

function GrantsTab({ ws }: { ws: Detail }) {
  const invalidate = useInvalidate(ws.name);
  const [selected, setSelected] = useState<string[]>(ws.templates.filter((t) => t.granted).map((t) => String(t.id)));
  const save = useMutation({
    mutationFn: () => api.setGrants(ws.name, selected.map(Number)),
    onSuccess: () => {
      notifications.show({ color: "green", message: "Grants saved" });
      invalidate();
    },
  });
  if (ws.templates.length === 0) {
    return (
      <Text c="dimmed">
        No templates defined yet.{" "}
        <Anchor component={Link} to="/templates/new">
          Create one
        </Anchor>
      </Text>
    );
  }
  return (
    <Stack>
      <ErrorAlert error={save.error} />
      <Checkbox.Group label="Templates this workspace may use" value={selected} onChange={setSelected}>
        <Stack gap="xs" mt="xs">
          {ws.templates.map((t) => (
            <Checkbox key={t.id} value={String(t.id)} label={`${t.name}${t.host_pattern ? ` (${t.host_pattern})` : ""}`} />
          ))}
        </Stack>
      </Checkbox.Group>
      <Group>
        <Button onClick={() => save.mutate()} loading={save.isPending}>
          Save grants
        </Button>
      </Group>
    </Stack>
  );
}

function QuotaTab({ ws }: { ws: Detail }) {
  const invalidate = useInvalidate(ws.name);
  const [quota, setQuota] = useState(ws.quota);
  const [ports, setPorts] = useState(ws.ports);
  const save = useMutation({
    mutationFn: () => api.updateWorkspace(ws.name, { quota, ports }),
    onSuccess: () => {
      notifications.show({ color: "green", message: "Quota and ports saved" });
      invalidate();
    },
  });
  return (
    <Stack>
      <ErrorAlert error={save.error} />
      <QuotaFields value={quota} onChange={setQuota} />
      <PortFields value={ports} onChange={setPorts} />
      <Group>
        <Button onClick={() => save.mutate()} loading={save.isPending}>
          Save
        </Button>
      </Group>
    </Stack>
  );
}

function DangerTab({ ws }: { ws: Detail }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const del = useMutation({
    mutationFn: (confirm: string) => api.deleteWorkspace(ws.name, confirm),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["workspaces"] });
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notifications.show({ color: "green", message: `Workspace ${ws.name} deleted` });
      navigate("/workspaces");
    },
  });
  return (
    <Stack>
      <Text>Deletes all apps of this workspace including their data, the workspace folder and all tokens.</Text>
      <ErrorAlert error={del.error} />
      <ConfirmDelete name={ws.name} label="Delete workspace" loading={del.isPending} onConfirm={(c) => del.mutate(c)} />
    </Stack>
  );
}

export function WorkspaceDetail() {
  const { name = "" } = useParams();
  const q = useQuery({ queryKey: ["workspace", name], queryFn: () => api.workspace(name) });
  if (q.isPending) return <Loader />;
  if (q.isError) return <ErrorAlert error={q.error} />;
  const ws = q.data;
  return (
    <Stack>
      <div>
        <Title order={2}>Workspace {ws.name}</Title>
        <Text size="sm" c="dimmed">
          Folder <Code>{ws.folder}</Code>
        </Text>
      </div>
      <Tabs defaultValue="apps" keepMounted={false}>
        <Tabs.List>
          <Tabs.Tab value="apps">Apps ({ws.apps.length})</Tabs.Tab>
          <Tabs.Tab value="tokens">Tokens</Tabs.Tab>
          <Tabs.Tab value="templates">Templates</Tabs.Tab>
          <Tabs.Tab value="quota">Quota &amp; ports</Tabs.Tab>
          <Tabs.Tab value="danger" color="red">
            Danger zone
          </Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="apps" pt="md">
          <AppsTab ws={ws} />
        </Tabs.Panel>
        <Tabs.Panel value="tokens" pt="md">
          <TokensTab ws={ws} />
        </Tabs.Panel>
        <Tabs.Panel value="templates" pt="md">
          <GrantsTab ws={ws} />
        </Tabs.Panel>
        <Tabs.Panel value="quota" pt="md">
          <QuotaTab ws={ws} />
        </Tabs.Panel>
        <Tabs.Panel value="danger" pt="md">
          <DangerTab ws={ws} />
        </Tabs.Panel>
      </Tabs>
    </Stack>
  );
}
```

- [ ] **Step 5: Add the routes**

In `web/src/App.tsx` add the imports

```tsx
import { WorkspaceDetail } from "./pages/WorkspaceDetail";
import { Workspaces } from "./pages/Workspaces";
```

and, directly after `<Route path="/" element={<Dashboard />} />`, the routes

```tsx
        <Route path="/workspaces" element={<Workspaces />} />
        <Route path="/workspaces/:name" element={<WorkspaceDetail />} />
```

- [ ] **Step 6: Typecheck, test and build**

Run: `cd web && npm run typecheck && npm test && npm run build && cd ..`
Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add web/src
git commit -m "feat(web): workspace management with tokens, grants, quota and apps"
```

---

### Task 24: React app — templates with live preview, audit, settings, orphans

**Files:**
- Create: `web/src/pages/Templates.tsx`, `web/src/pages/TemplateEditor.tsx`, `web/src/pages/Audit.tsx`, `web/src/pages/Settings.tsx`, `web/src/pages/Orphans.tsx`
- Modify: `web/src/App.tsx` (final version below)
- Test: `web/src/pages/TemplateEditor.test.tsx`, `web/src/pages/Misc.test.tsx`

**Interfaces:**
- Consumes: Tasks 22–23 (`api`, types, `ErrorAlert`, `AuditTable`, `QuotaFields`, test helpers)
- Produces: pages `Templates`, `TemplateEditor` (exports `parseLabels(text) → {labels, errors}`), `Audit`, `Settings`, `Orphans`; final `App.tsx`

- [ ] **Step 1: Write the failing tests**

`web/src/pages/TemplateEditor.test.tsx`:

```tsx
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { mockApi, renderWithProviders } from "../test/utils";
import { TemplateEditor, parseLabels } from "./TemplateEditor";

test("parseLabels splits at the first = and reports bad lines", () => {
  expect(parseLabels("a=b=c\n\n x = y ")).toEqual({ labels: { a: "b=c", x: "y" }, errors: [] });
  expect(parseLabels("novalue").errors).toEqual(["Labels line 1: expected key=value"]);
});

test("live preview shows errors and hosts, then saves", async () => {
  const calls = mockApi({
    "POST /templates/preview": (b) =>
      b.template.host_pattern.includes("{{workspace}}")
        ? { body: { valid: true, errors: [], host: "shop.alice.apps.example.com", labels: [{ key: "traefik.enable", value: "true" }] } }
        : { body: { valid: false, errors: ["host_pattern must contain {{workspace}} and {{app}}"], host: "", labels: [] } },
    "POST /templates": { status: 201, body: { id: 7 } },
  });
  renderWithProviders(
    <Routes>
      <Route path="/templates/new" element={<TemplateEditor />} />
      <Route path="/templates" element={<div>template list</div>} />
    </Routes>,
    "/templates/new",
  );
  await userEvent.type(await screen.findByLabelText(/^Name/), "public-web");
  const host = screen.getByLabelText(/^Host pattern/);
  await userEvent.click(host);
  await userEvent.paste("{{app}}.apps.example.com");
  expect(await screen.findByText("host_pattern must contain {{workspace}} and {{app}}", {}, { timeout: 3000 })).toBeInTheDocument();

  await userEvent.clear(host);
  await userEvent.click(host);
  await userEvent.paste("{{app}}.{{workspace}}.apps.example.com");
  await userEvent.click(screen.getByLabelText(/^Networks/));
  await userEvent.paste("apps");
  await userEvent.click(screen.getByLabelText(/^Labels/));
  await userEvent.paste("traefik.enable=true");
  expect(await screen.findByText("shop.alice.apps.example.com", {}, { timeout: 3000 })).toBeInTheDocument();

  await userEvent.click(screen.getByRole("button", { name: "Save template" }));
  expect(await screen.findByText("template list")).toBeInTheDocument();
  expect(calls.find((c) => c.method === "POST" && c.path === "/templates")?.body).toEqual({
    name: "public-web",
    description: "",
    host_pattern: "{{app}}.{{workspace}}.apps.example.com",
    networks: ["apps"],
    labels: { "traefik.enable": "true" },
  });
});
```

`web/src/pages/Misc.test.tsx`:

```tsx
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockApi, quota, renderWithProviders } from "../test/utils";
import { Audit } from "./Audit";
import { Orphans } from "./Orphans";
import { Settings } from "./Settings";

const entry = {
  id: 1, time: "2026-10-01T10:00:00Z", actor_type: "agent", actor: "token:1", workspace: "alice", action: "deploy",
  params: { app: "shop" }, result: "security_violation", error: "rejected", severity: "critical", duration_ms: 12, client_ip: "203.0.113.1",
};

test("audit severity filter is sent to the API", async () => {
  const calls = mockApi({ "GET /audit": { body: { entries: [entry], next_before: 0 } } });
  renderWithProviders(<Audit />);
  expect(await screen.findByText("security_violation")).toBeInTheDocument();
  await userEvent.click(screen.getByText("critical"));
  await waitFor(() => expect(calls.at(-1)?.search).toBe("?severity=critical"));
});

test("settings are saved", async () => {
  const calls = mockApi({
    "GET /settings": { body: { default_quota: quota, audit_retention_days: 90 } },
    "PUT /settings": (b) => ({ body: b }),
  });
  renderWithProviders(<Settings />);
  const retention = await screen.findByLabelText(/retention/i);
  await userEvent.clear(retention);
  await userEvent.type(retention, "30");
  await userEvent.click(screen.getByRole("button", { name: "Save settings" }));
  expect(await screen.findByText("Settings saved")).toBeInTheDocument();
  expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ default_quota: quota, audit_retention_days: 30 });
});

test("orphans can be removed", async () => {
  let gone = false;
  const calls = mockApi({
    "GET /orphans": () => ({ body: gone ? [] : [{ id: "ghost-x-web", name: "ghost-x-web", workspace: "ghost", app: "x", state: "running", status: "Up" }] }),
    "DELETE /orphans/ghost-x-web": () => {
      gone = true;
      return { status: 204 };
    },
  });
  renderWithProviders(<Orphans />);
  await userEvent.click(await screen.findByRole("button", { name: "Remove" }));
  expect(await screen.findByText("No orphaned containers.")).toBeInTheDocument();
  expect(calls.some((c) => c.method === "DELETE")).toBe(true);
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && npm test; cd ..`
Expected: FAIL — cannot resolve `./TemplateEditor`, `./Audit`, `./Settings`, `./Orphans`.

- [ ] **Step 3: Write the pages**

`web/src/pages/Templates.tsx`:

```tsx
import { Anchor, Badge, Button, Code, Group, Loader, Stack, Table, Text, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router";
import { api } from "../api";
import { ErrorAlert } from "../components/ErrorAlert";

export function Templates() {
  const queryClient = useQueryClient();
  const q = useQuery({ queryKey: ["templates"], queryFn: api.templates });
  const del = useMutation({
    mutationFn: api.deleteTemplate,
    onSuccess: () => {
      notifications.show({ color: "green", message: "Template deleted" });
      queryClient.invalidateQueries({ queryKey: ["templates"] });
    },
  });
  return (
    <Stack>
      <Group justify="space-between">
        <Title order={2}>Exposure templates</Title>
        <Button component={Link} to="/templates/new">
          New template
        </Button>
      </Group>
      <Text size="sm" c="dimmed">
        Templates define how agent services are exposed (networks, reverse-proxy labels, host pattern). Grant them per workspace.
      </Text>
      <ErrorAlert error={q.error ?? del.error} />
      {q.isPending && <Loader />}
      {q.data && q.data.length === 0 && <Text c="dimmed">No templates yet.</Text>}
      {q.data && q.data.length > 0 && (
        <Table striped highlightOnHover>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Description</Table.Th>
              <Table.Th>Host pattern</Table.Th>
              <Table.Th>Networks</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {q.data.map((t) => (
              <Table.Tr key={t.id}>
                <Table.Td>
                  <Anchor component={Link} to={`/templates/${t.id}`}>
                    {t.name}
                  </Anchor>
                </Table.Td>
                <Table.Td>{t.description}</Table.Td>
                <Table.Td>{t.host_pattern ? <Code>{t.host_pattern}</Code> : <Text c="dimmed">none</Text>}</Table.Td>
                <Table.Td>
                  <Group gap={4}>
                    {t.networks.map((n) => (
                      <Badge key={n} variant="light">
                        {n}
                      </Badge>
                    ))}
                  </Group>
                </Table.Td>
                <Table.Td>
                  <Button size="xs" color="red" variant="light" loading={del.isPending && del.variables === t.id} onClick={() => del.mutate(t.id)}>
                    Delete
                  </Button>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
    </Stack>
  );
}
```

`web/src/pages/TemplateEditor.tsx`:

```tsx
import { Alert, Badge, Button, Card, Code, Grid, Group, List, Loader, Stack, Table, Text, TextInput, Textarea, Title } from "@mantine/core";
import { useDebouncedValue } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { api } from "../api";
import { ErrorAlert } from "../components/ErrorAlert";
import type { Template } from "../types";

interface Draft {
  name: string;
  description: string;
  host_pattern: string;
  networks: string;
  labels: string;
}

const emptyDraft: Draft = { name: "", description: "", host_pattern: "", networks: "", labels: "" };

/** Parses one key=value per line; the first = separates key and value. */
export function parseLabels(text: string): { labels: Record<string, string>; errors: string[] } {
  const labels: Record<string, string> = {};
  const errors: string[] = [];
  text.split("\n").forEach((raw, i) => {
    const line = raw.trim();
    if (line === "") return;
    const eq = line.indexOf("=");
    if (eq <= 0) {
      errors.push(`Labels line ${i + 1}: expected key=value`);
      return;
    }
    labels[line.slice(0, eq).trim()] = line.slice(eq + 1).trim();
  });
  return { labels, errors };
}

function toTemplate(d: Draft): Template {
  return {
    name: d.name.trim(),
    description: d.description.trim(),
    host_pattern: d.host_pattern.trim(),
    networks: d.networks.split(",").map((n) => n.trim()).filter(Boolean),
    labels: parseLabels(d.labels).labels,
  };
}

function fromTemplate(t: Template): Draft {
  return {
    name: t.name,
    description: t.description,
    host_pattern: t.host_pattern,
    networks: t.networks.join(", "),
    labels: Object.entries(t.labels)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
  };
}

const placeholders = "{{workspace}} {{app}} {{service}} {{id}} {{host}} {{port}}";

export function TemplateEditor() {
  const { id: idParam } = useParams();
  const id = idParam ? Number(idParam) : 0;
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const existing = useQuery({ queryKey: ["template", id], queryFn: () => api.template(id), enabled: id > 0 });
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [loaded, setLoaded] = useState(id === 0);
  useEffect(() => {
    if (existing.data && !loaded) {
      setDraft(fromTemplate(existing.data));
      setLoaded(true);
    }
  }, [existing.data, loaded]);

  const [debounced] = useDebouncedValue(draft, 400);
  const labelErrors = parseLabels(draft.labels).errors;
  const preview = useQuery({
    queryKey: ["template-preview", id, debounced],
    queryFn: () => api.previewTemplate(id, toTemplate(debounced)),
    enabled: loaded && (debounced.name !== "" || debounced.host_pattern !== ""),
    placeholderData: keepPreviousData,
  });

  const save = useMutation({
    mutationFn: () => (id > 0 ? api.updateTemplate(id, toTemplate(draft)) : api.createTemplate(toTemplate(draft))),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["templates"] });
      notifications.show({ color: "green", message: `Template ${draft.name} saved` });
      navigate("/templates");
    },
  });

  if (id > 0 && existing.isPending) return <Loader />;
  if (existing.isError) return <ErrorAlert error={existing.error} />;
  const set = (k: keyof Draft) => (e: { currentTarget: { value: string } }) => setDraft({ ...draft, [k]: e.currentTarget.value });

  return (
    <Stack>
      <Title order={2}>{id > 0 ? `Template ${draft.name}` : "New template"}</Title>
      <Grid gap="lg">
        <Grid.Col span={{ base: 12, md: 7 }}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              save.mutate();
            }}
          >
            <Stack>
              <ErrorAlert error={save.error} />
              <TextInput label="Name" value={draft.name} onChange={set("name")} required />
              <TextInput label="Description" value={draft.description} onChange={set("description")} />
              <TextInput
                label="Host pattern"
                description="Optional. Placeholders separated by - or ., must contain {{workspace}} and {{app}}; the domain suffix must not overlap other templates."
                placeholder="{{app}}.{{workspace}}.apps.example.com"
                value={draft.host_pattern}
                onChange={set("host_pattern")}
              />
              <TextInput label="Networks" description="Comma-separated; must exist in Docker" value={draft.networks} onChange={set("networks")} />
              <Textarea
                label="Labels"
                description={`One key=value per line. Placeholders: ${placeholders}`}
                value={draft.labels}
                onChange={set("labels")}
                autosize
                minRows={6}
                styles={{ input: { fontFamily: "var(--mantine-font-family-monospace)" } }}
                error={labelErrors.length > 0 ? labelErrors.join("; ") : undefined}
              />
              <Group>
                <Button type="submit" loading={save.isPending} disabled={labelErrors.length > 0}>
                  Save template
                </Button>
              </Group>
            </Stack>
          </form>
        </Grid.Col>
        <Grid.Col span={{ base: 12, md: 5 }}>
          <Card withBorder>
            <Stack gap="sm">
              <Group justify="space-between">
                <Title order={4}>Live preview</Title>
                {preview.isFetching && <Loader size="xs" />}
                {preview.data && (preview.data.valid ? <Badge color="green">valid</Badge> : <Badge color="red">invalid</Badge>)}
              </Group>
              <Text size="xs" c="dimmed">
                Sample: workspace alice, app shop, service web, port 8080
              </Text>
              <ErrorAlert error={preview.error} />
              {preview.data && preview.data.errors.length > 0 && (
                <Alert color="red" title="Validation errors">
                  <List size="sm">
                    {preview.data.errors.map((e) => (
                      <List.Item key={e}>{e}</List.Item>
                    ))}
                  </List>
                </Alert>
              )}
              {preview.data && (
                <>
                  <Text size="sm">
                    Host: {preview.data.host ? <Code>{preview.data.host}</Code> : <Text span c="dimmed">none</Text>}
                  </Text>
                  <Table withTableBorder fz="xs">
                    <Table.Tbody>
                      {preview.data.labels.map((l) => (
                        <Table.Tr key={l.key}>
                          <Table.Td>
                            <Code>{l.key}</Code>
                          </Table.Td>
                          <Table.Td>
                            <Code>{l.value}</Code>
                          </Table.Td>
                        </Table.Tr>
                      ))}
                    </Table.Tbody>
                  </Table>
                </>
              )}
              {!preview.data && !preview.isFetching && <Text c="dimmed">Start typing to see a preview.</Text>}
            </Stack>
          </Card>
        </Grid.Col>
      </Grid>
    </Stack>
  );
}
```

`web/src/pages/Audit.tsx`:

```tsx
import { Button, Group, Loader, SegmentedControl, Stack, TextInput, Title } from "@mantine/core";
import { useDebouncedValue } from "@mantine/hooks";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api";
import { AuditTable } from "../components/AuditTable";
import { ErrorAlert } from "../components/ErrorAlert";

export function Audit() {
  const [workspace, setWorkspace] = useState("");
  const [action, setAction] = useState("");
  const [result, setResult] = useState("");
  const [severity, setSeverity] = useState("any");
  const [cursors, setCursors] = useState<number[]>([]);
  const [text] = useDebouncedValue({ workspace, action, result }, 300);
  const before = cursors.at(-1) ?? 0;
  const filter = { ...text, severity: severity === "any" ? "" : severity, before };
  const q = useQuery({ queryKey: ["audit", filter], queryFn: () => api.audit(filter), placeholderData: keepPreviousData });
  const reset = () => setCursors([]);

  return (
    <Stack>
      <Title order={2}>Audit log</Title>
      <Group align="end">
        <TextInput label="Workspace" value={workspace} onChange={(e) => { setWorkspace(e.currentTarget.value); reset(); }} />
        <TextInput label="Action" value={action} onChange={(e) => { setAction(e.currentTarget.value); reset(); }} />
        <TextInput label="Result" value={result} onChange={(e) => { setResult(e.currentTarget.value); reset(); }} />
        <SegmentedControl
          data={["any", "info", "warn", "critical"]}
          value={severity}
          onChange={(v) => {
            setSeverity(v);
            reset();
          }}
        />
        {q.isFetching && <Loader size="sm" />}
      </Group>
      <ErrorAlert error={q.error} />
      {q.data && <AuditTable entries={q.data.entries} />}
      <Group>
        <Button variant="default" disabled={cursors.length === 0} onClick={() => setCursors(cursors.slice(0, -1))}>
          Newer
        </Button>
        <Button variant="default" disabled={!q.data || q.data.next_before === 0} onClick={() => q.data && setCursors([...cursors, q.data.next_before])}>
          Older
        </Button>
      </Group>
    </Stack>
  );
}
```

`web/src/pages/Settings.tsx`:

```tsx
import { Button, Card, Group, Loader, NumberInput, Stack, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api } from "../api";
import { ErrorAlert } from "../components/ErrorAlert";
import { QuotaFields } from "../components/QuotaFields";
import type { Settings as SettingsT } from "../types";

export function Settings() {
  const queryClient = useQueryClient();
  const q = useQuery({ queryKey: ["settings"], queryFn: api.settings });
  const [draft, setDraft] = useState<SettingsT | null>(null);
  useEffect(() => {
    if (q.data && draft === null) setDraft(q.data);
  }, [q.data, draft]);
  const save = useMutation({
    mutationFn: () => api.saveSettings(draft!),
    onSuccess: (s) => {
      queryClient.setQueryData(["settings"], s);
      notifications.show({ color: "green", message: "Settings saved" });
    },
  });
  if (q.isError) return <ErrorAlert error={q.error} />;
  if (draft === null) return <Loader />;
  return (
    <Stack>
      <Title order={2}>Settings</Title>
      <ErrorAlert error={save.error} />
      <Card withBorder>
        <Title order={4} mb="sm">
          Default quota for new workspaces
        </Title>
        <QuotaFields value={draft.default_quota} onChange={(qq) => setDraft({ ...draft, default_quota: qq })} />
      </Card>
      <Card withBorder>
        <NumberInput
          label="Audit log retention (days)"
          min={1}
          max={3650}
          value={draft.audit_retention_days}
          onChange={(v) => setDraft({ ...draft, audit_retention_days: Number(v) || 0 })}
          maw={300}
        />
      </Card>
      <Group>
        <Button onClick={() => save.mutate()} loading={save.isPending}>
          Save settings
        </Button>
      </Group>
    </Stack>
  );
}
```

`web/src/pages/Orphans.tsx`:

```tsx
import { Badge, Button, Loader, Stack, Table, Text, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { ErrorAlert } from "../components/ErrorAlert";

export function Orphans() {
  const queryClient = useQueryClient();
  const q = useQuery({ queryKey: ["orphans"], queryFn: api.orphans });
  const remove = useMutation({
    mutationFn: api.removeOrphan,
    onSuccess: () => {
      notifications.show({ color: "green", message: "Container removed" });
      queryClient.invalidateQueries({ queryKey: ["orphans"] });
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
  return (
    <Stack>
      <Title order={2}>Orphaned containers</Title>
      <Text size="sm" c="dimmed">
        Containers carrying deploy-gateway labels whose workspace or app no longer exists.
      </Text>
      <ErrorAlert error={q.error ?? remove.error} />
      {q.isPending && <Loader />}
      {q.data && q.data.length === 0 && <Text c="dimmed">No orphaned containers.</Text>}
      {q.data && q.data.length > 0 && (
        <Table striped>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Workspace</Table.Th>
              <Table.Th>App</Table.Th>
              <Table.Th>State</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {q.data.map((o) => (
              <Table.Tr key={o.id}>
                <Table.Td>{o.name}</Table.Td>
                <Table.Td>{o.workspace}</Table.Td>
                <Table.Td>{o.app}</Table.Td>
                <Table.Td>
                  <Badge variant="light">{o.state}</Badge>
                </Table.Td>
                <Table.Td>
                  <Button size="xs" color="red" variant="light" loading={remove.isPending && remove.variables === o.id} onClick={() => remove.mutate(o.id)}>
                    Remove
                  </Button>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
    </Stack>
  );
}
```

- [ ] **Step 4: Replace `App.tsx` with the final version**

`web/src/App.tsx`:

```tsx
import { Center, Loader, Text } from "@mantine/core";
import { QueryCache, QueryClient, useQuery } from "@tanstack/react-query";
import { Route, Routes } from "react-router";
import { ApiError, api } from "./api";
import { ErrorAlert } from "./components/ErrorAlert";
import { Layout } from "./components/Layout";
import { Audit } from "./pages/Audit";
import { Dashboard } from "./pages/Dashboard";
import { Login } from "./pages/Login";
import { Orphans } from "./pages/Orphans";
import { Settings } from "./pages/Settings";
import { TemplateEditor } from "./pages/TemplateEditor";
import { Templates } from "./pages/Templates";
import { WorkspaceDetail } from "./pages/WorkspaceDetail";
import { Workspaces } from "./pages/Workspaces";

/** A session that expires while the app is open sends the user back to the login form. */
export function createQueryClient(opts: { retry?: boolean } = {}): QueryClient {
  const client: QueryClient = new QueryClient({
    defaultOptions: { queries: { retry: opts.retry === false ? false : 1 }, mutations: { retry: false } },
    queryCache: new QueryCache({
      onError: (error, query) => {
        if (query.queryKey[0] !== "me" && error instanceof ApiError && error.code === "login_required") {
          client.invalidateQueries({ queryKey: ["me"] });
        }
      },
    }),
  });
  return client;
}

export function App() {
  const me = useQuery({ queryKey: ["me"], queryFn: api.me, retry: false });
  if (me.isPending) {
    return (
      <Center h="100vh">
        <Loader />
      </Center>
    );
  }
  if (me.isError) {
    if (me.error instanceof ApiError && me.error.code === "login_required") {
      return <Login />;
    }
    return (
      <Center h="100vh" p="xl">
        <ErrorAlert error={me.error} />
      </Center>
    );
  }
  return (
    <Layout me={me.data}>
      <Routes>
        <Route path="/" element={<Dashboard />} />
        <Route path="/workspaces" element={<Workspaces />} />
        <Route path="/workspaces/:name" element={<WorkspaceDetail />} />
        <Route path="/templates" element={<Templates />} />
        <Route path="/templates/new" element={<TemplateEditor />} />
        <Route path="/templates/:id" element={<TemplateEditor />} />
        <Route path="/audit" element={<Audit />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/orphans" element={<Orphans />} />
        <Route path="*" element={<Text>Page not found.</Text>} />
      </Routes>
    </Layout>
  );
}
```

- [ ] **Step 5: Typecheck, test and build**

Run: `cd web && npm run typecheck && npm test && npm run build && cd ..`
Expected: all pass (13 tests).

- [ ] **Step 6: Commit**

```bash
git add web/src
git commit -m "feat(web): templates with live preview, audit log, settings, orphans"
```

---

### Task 25: Binary — wiring, startup checks, hash-password

**Files:**
- Create: `cmd/deploy-gateway/main.go`
- Test: `cmd/deploy-gateway/main_test.go`

**Interfaces:**
- Consumes: everything above; `dockerx.Client.MountSource`, `Ping`, `NetworkExists` (13); `runner.New` (12); `config.Load` (8); `admin.New`, `webui.FS` (19)
- Produces: binary `deploy-gateway` with subcommands `serve` (default) and `hash-password`; `GET /healthz` on the agent listener

- [ ] **Step 1: Write the failing test**

`cmd/deploy-gateway/main_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/agentauth"
	"github.com/sebmahr/deploy-gateway/internal/auth"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/service/servicetest"
	"github.com/sebmahr/deploy-gateway/internal/store"
)

func TestHashPassword(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := hashPassword(strings.NewReader("a long enough secret\n"), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	h := strings.TrimSpace(out.String())
	if !auth.CheckPassword(h, "a long enough secret") {
		t.Fatalf("hash %q does not verify", h)
	}
	out.Reset()
	if code := hashPassword(strings.NewReader("short\n"), &out, &errOut); code == 0 || out.Len() != 0 {
		t.Fatal("short password must be rejected")
	}
}

type fakeMounts struct {
	src         string
	inContainer bool
	err         error
}

func (f fakeMounts) MountSource(context.Context, string, string) (string, bool, error) {
	return f.src, f.inContainer, f.err
}

func TestCheckSameMount(t *testing.T) {
	root := "/srv/deploy-gateway/workspaces"
	cases := []struct {
		name    string
		m       fakeMounts
		wantErr bool
	}{
		{"on host", fakeMounts{inContainer: false}, false},
		{"same path", fakeMounts{src: root, inContainer: true}, false},
		{"different host path", fakeMounts{src: "/var/lib/other", inContainer: true}, true},
		{"not mounted", fakeMounts{src: "", inContainer: true}, true},
		{"inspect error", fakeMounts{err: errors.New("boom")}, true},
	}
	for _, c := range cases {
		err := checkSameMount(context.Background(), c.m, "abc123", root)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestAgentHandlerHealthz(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	compose, docker := &servicetest.FakeCompose{}, servicetest.NewFakeDocker()
	svc := service.New(st, compose, docker, t.TempDir())
	authn := &agentauth.Authenticator{Store: st, Limiter: auth.NewLimiter(10, time.Minute, time.Hour)}
	for _, c := range []struct {
		err  error
		want int
	}{{nil, 200}, {errors.New("down"), 503}} {
		h := agentHandler(svc, authn, fakePinger{c.err})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
		if w.Code != c.want {
			t.Errorf("healthz with ping err %v: %d", c.err, w.Code)
		}
	}
	w := httptest.NewRecorder()
	agentHandler(svc, authn, fakePinger{}).ServeHTTP(w, httptest.NewRequest("POST", "/api/v1/whoami", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("api without token: %d", w.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/deploy-gateway/`
Expected: FAIL — `undefined: hashPassword`, `checkSameMount`, `agentHandler`.

- [ ] **Step 3: Write `main.go`**

`cmd/deploy-gateway/main.go`:

```go
// Command deploy-gateway runs the deployment gateway (see README.md).
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/admin"
	"github.com/sebmahr/deploy-gateway/internal/agentauth"
	"github.com/sebmahr/deploy-gateway/internal/api"
	"github.com/sebmahr/deploy-gateway/internal/auth"
	"github.com/sebmahr/deploy-gateway/internal/config"
	"github.com/sebmahr/deploy-gateway/internal/dockerx"
	"github.com/sebmahr/deploy-gateway/internal/mcpserver"
	"github.com/sebmahr/deploy-gateway/internal/runner"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/store"
	"github.com/sebmahr/deploy-gateway/internal/webui"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "hash-password":
		os.Exit(hashPassword(os.Stdin, os.Stdout, os.Stderr))
	case "serve":
		cfg, err := config.Load(os.Getenv)
		if err != nil {
			logger.Error("invalid configuration", "err", err)
			os.Exit(2)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := serve(ctx, cfg, logger); err != nil {
			logger.Error("fatal", "err", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "usage: deploy-gateway [serve|hash-password]")
		os.Exit(2)
	}
}

// hashPassword reads one line from in and prints its argon2id hash.
func hashPassword(in io.Reader, out, errOut io.Writer) int {
	fmt.Fprint(errOut, "Password (min. 12 characters): ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		fmt.Fprintln(errOut, err)
		return 1
	}
	pw := strings.TrimRight(line, "\r\n")
	if len(pw) < 12 {
		fmt.Fprintln(errOut, "password must be at least 12 characters")
		return 1
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 1
	}
	fmt.Fprintln(out, h)
	return 0
}

type mountInspector interface {
	MountSource(ctx context.Context, containerID, destination string) (string, bool, error)
}

// checkSameMount enforces that WORKSPACES_ROOT is bind-mounted from the
// identical host path, because the Docker daemon resolves bind sources on
// the host. Running directly on the host passes trivially.
func checkSameMount(ctx context.Context, m mountInspector, self, root string) error {
	src, inContainer, err := m.MountSource(ctx, self, root)
	if err != nil {
		return fmt.Errorf("inspect own container %q: %w", self, err)
	}
	if !inContainer {
		return nil
	}
	if src != root {
		return fmt.Errorf("WORKSPACES_ROOT %s must be bind-mounted from the identical host path (found %q)", root, src)
	}
	return nil
}

type pinger interface{ Ping(context.Context) error }

func agentHandler(svc *service.Service, authn *agentauth.Authenticator, p pinger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.Handler(mcpserver.New(svc, version), authn))
	mux.Handle("/api/v1/", (&api.Handler{Svc: svc, Auth: authn}).Routes())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := p.Ping(r.Context()); err != nil {
			http.Error(w, "degraded: docker unreachable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	return mux
}

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	root, err := filepath.EvalSymlinks(cfg.WorkspacesRoot)
	if err != nil {
		return fmt.Errorf("WORKSPACES_ROOT: %w", err)
	}
	if root != cfg.WorkspacesRoot {
		return fmt.Errorf("WORKSPACES_ROOT must not contain symlinks (resolves to %s)", root)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, "deploy-gateway.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	dc, err := dockerx.New()
	if err != nil {
		return err
	}
	if err := dc.Ping(ctx); err != nil {
		return fmt.Errorf("docker unreachable: %w", err)
	}
	self, _ := os.Hostname()
	if err := checkSameMount(ctx, dc, self, root); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, cfg.ComposeBin, "version", "--short").CombinedOutput(); err != nil {
		return fmt.Errorf("compose binary %s not usable: %v: %s", cfg.ComposeBin, err, out)
	}

	run := runner.New(runner.Options{Bin: cfg.ComposeBin, DockerConfig: cfg.DockerConfig, DockerHost: cfg.DockerHost,
		Timeout: cfg.DeployTimeout, Concurrency: cfg.DeployConcurrency})
	svc := service.New(st, run, dc, root)
	svc.Logger = logger
	warnMissingNetworks(ctx, st, dc, logger)
	if orphans, err := svc.Orphans(ctx); err == nil && len(orphans) > 0 {
		logger.Warn("orphaned gateway containers found; see the admin UI", "count", len(orphans))
	}

	authn := &agentauth.Authenticator{Store: st, Limiter: auth.NewLimiter(10, time.Minute, 15*time.Minute), TrustedProxies: cfg.TrustedProxies}
	adminSrv := admin.New(admin.Options{Svc: svc, Store: st, PasswordHash: cfg.AdminPasswordHash,
		TrustedProxies: cfg.AdminTrustedProxies, UserHeader: cfg.AdminUserHeader, UI: webui.FS(), Logger: logger})
	servers := []*http.Server{
		{Addr: cfg.AgentListen, Handler: agentHandler(svc, authn, dc), ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout: cfg.DeployTimeout + 2*time.Minute, IdleTimeout: 2 * time.Minute},
		{Addr: cfg.AdminListen, Handler: adminSrv.Handler(), ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout: cfg.DeployTimeout + 2*time.Minute, IdleTimeout: 2 * time.Minute},
	}
	go pruneAudit(ctx, st, logger)

	errc := make(chan error, len(servers))
	for _, srv := range servers {
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	mode := "password"
	if cfg.ProxyMode() {
		mode = "proxy"
	}
	logger.Info("deploy-gateway started", "version", version, "agent", cfg.AgentListen, "admin", cfg.AdminListen, "admin_auth", mode, "root", root)

	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, srv := range servers {
		srv.Shutdown(shutdownCtx)
	}
	return nil
}

func warnMissingNetworks(ctx context.Context, st *store.Store, dc *dockerx.Client, logger *slog.Logger) {
	templates, err := st.ListTemplates(ctx)
	if err != nil {
		logger.Warn("cannot list templates", "err", err)
		return
	}
	for _, t := range templates {
		for _, n := range t.Networks {
			if ok, err := dc.NetworkExists(ctx, n); err != nil || !ok {
				logger.Warn("template network missing", "template", t.Name, "network", n, "err", err)
			}
		}
	}
}

func pruneAudit(ctx context.Context, st *store.Store, logger *slog.Logger) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		days, err := st.AuditRetentionDays(ctx)
		if err == nil {
			if n, err := st.PruneAudit(ctx, time.Now().AddDate(0, 0, -days)); err == nil && n > 0 {
				logger.Info("pruned audit log", "rows", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
```

- [ ] **Step 4: Run tests and the full suite**

Run: `go test ./cmd/deploy-gateway/ && go vet ./... && go test ./...`
Expected: PASS for all packages.

- [ ] **Step 5: Commit**

```bash
git add cmd
git commit -m "feat: deploy-gateway binary with startup checks and hash-password"
```

---

### Task 26: Packaging, documentation and Docker integration test

**Files:**
- Create: `Dockerfile` (Node build stage for `web/`), `.dockerignore`, `deploy/docker-compose.yml`, `deploy/.env.example`, `README.md`, `internal/service/integration_test.go`

**Interfaces:**
- Consumes: everything above
- Produces: container image `deploy-gateway`, example host stack, operator documentation

- [ ] **Step 1: Write the integration test**

`internal/service/integration_test.go`:

```go
//go:build integration

package service_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebmahr/deploy-gateway/internal/dockerx"
	"github.com/sebmahr/deploy-gateway/internal/runner"
	"github.com/sebmahr/deploy-gateway/internal/service"
	"github.com/sebmahr/deploy-gateway/internal/store"
	"github.com/sebmahr/deploy-gateway/internal/tmpl"
)

func detail(err error) string {
	var se *service.Error
	if errors.As(err, &se) {
		return se.Detail()
	}
	return ""
}

func TestIntegrationDeployLifecycle(t *testing.T) {
	bin := os.Getenv("COMPOSE_BIN")
	if bin == "" {
		t.Skip("COMPOSE_BIN not set")
	}
	ctx := context.Background()
	dc, err := dockerx.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := dc.Ping(ctx); err != nil {
		t.Skipf("docker unreachable: %v", err)
	}
	if out, err := exec.Command("docker", "network", "create", "dgtest-apps").CombinedOutput(); err != nil && !strings.Contains(string(out), "already exists") {
		t.Fatalf("network create: %v %s", err, out)
	}
	t.Cleanup(func() { exec.Command("docker", "network", "rm", "dgtest-apps").Run() })

	root := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "it.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dockerHost := os.Getenv("DOCKER_HOST")
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}
	run := runner.New(runner.Options{Bin: bin, DockerConfig: filepath.Join(os.Getenv("HOME"), ".docker"), DockerHost: dockerHost,
		Timeout: 2 * time.Minute, Concurrency: 1})
	svc := service.New(st, run, dc, root)

	tplID, err := svc.SaveTemplate(ctx, "itest", 0, tmpl.Template{Name: "itest-web", HostPattern: "{{app}}.{{workspace}}.itest.example",
		Networks: []string{"dgtest-apps"}, Labels: map[string]string{"itest.host": "{{host}}"}})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string, start int) service.Principal {
		w, err := svc.CreateWorkspace(ctx, "itest", name, store.BuiltinDefaultQuota(), service.PortSettings{Start: start, End: start + 9, BindIP: "127.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.SetGrants(ctx, "itest", w, []int64{tplID}); err != nil {
			t.Fatal(err)
		}
		w, _ = st.GetWorkspaceByName(ctx, name)
		return service.Principal{Workspace: w, TokenID: 1, ClientIP: "127.0.0.1"}
	}
	p := mk("itest", 28080)
	other := mk("itother", 28090)
	t.Cleanup(func() { svc.Delete(context.Background(), p, service.DeleteInput{App: "web", Confirm: "web"}) })

	stk := mustStack(t, `{"services": {"nginx": {
		"image": "nginxinc/nginx-unprivileged:1.27-alpine",
		"environment": {"GREETING": "${HOME} costs $5"},
		"expose": {"template": "itest-web", "port": 8080},
		"ports": [{"container": 8080, "host": 28080}],
		"healthcheck": {"command": ["wget", "-qO-", "http://127.0.0.1:8080/"], "interval": "5s"}}}}`)
	res, err := svc.Deploy(ctx, p, service.DeployInput{App: "web", Stack: stk})
	if err != nil {
		t.Fatalf("deploy: %v\ndetail: %s", err, detail(err))
	}
	if res.Services[0].State != "running" || res.Services[0].Health != "healthy" {
		t.Fatalf("status = %+v", res)
	}

	resp, err := http.Get("http://127.0.0.1:28080/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "nginx") {
		t.Fatalf("published port: %d %s", resp.StatusCode, body)
	}

	out, err := exec.Command("docker", "exec", "itest-web-nginx", "printenv", "GREETING").Output()
	if err != nil || strings.TrimSpace(string(out)) != "${HOME} costs $5" {
		t.Fatalf("env value must arrive literally, got %q (%v)", out, err)
	}
	insp, err := exec.Command("docker", "inspect", "--format", "{{.HostConfig.ReadonlyRootfs}} {{.HostConfig.CapDrop}} {{.HostConfig.Privileged}}", "itest-web-nginx").Output()
	if err != nil || strings.TrimSpace(string(insp)) != "true [ALL] false" {
		t.Fatalf("hardening not applied: %q %v", insp, err)
	}

	logs, err := svc.Logs(ctx, p, service.LogsInput{App: "web", Service: "nginx", Tail: 20})
	if err != nil || logs.Logs == "" {
		t.Fatalf("logs: %+v %v", logs, err)
	}
	if _, err := svc.Restart(ctx, p, service.ServiceInput{App: "web"}); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if _, err := svc.Logs(ctx, other, service.LogsInput{App: "web", Service: "nginx"}); code(err) != service.CodeNotFound {
		t.Fatalf("cross-workspace logs: %v", err)
	}

	if _, err := svc.Delete(ctx, p, service.DeleteInput{App: "web", Confirm: "web"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	left, _ := exec.Command("docker", "ps", "-aq", "--filter", "label=deploy-gateway.workspace=itest").Output()
	if strings.TrimSpace(string(left)) != "" {
		t.Fatalf("containers left after delete: %s", left)
	}
	if _, err := os.Stat(filepath.Join(root, "itest", "web")); !os.IsNotExist(err) {
		t.Fatal("app folder left after delete")
	}
}
```

- [ ] **Step 2: Run the integration test against local Docker**

```bash
export COMPOSE_BIN="$(docker info --format '{{range .ClientInfo.Plugins}}{{if eq .Name "compose"}}{{.Path}}{{end}}{{end}}')"
go test -tags integration ./internal/service/ -run TestIntegrationDeployLifecycle -v -count=1
```

Expected: PASS (first run pulls `nginxinc/nginx-unprivileged:1.27-alpine`). If the deploy fails, the `detail:` line contains compose's full output.

- [ ] **Step 3: Write `Dockerfile` and `.dockerignore`**

`Dockerfile`:

```dockerfile
# syntax=docker/dockerfile:1.7
FROM node:24.18.0-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN mkdir -p ../internal/webui/dist && npm run build

FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist ./internal/webui/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/deploy-gateway ./cmd/deploy-gateway

FROM gcr.io/distroless/static-debian12
# Standalone Docker Compose; checksum from the release's docker-compose-linux-x86_64.sha256
ARG COMPOSE_VERSION=v5.5.1
ARG COMPOSE_SHA256=db1889184726840f75c4f9c001048430d4f25b3be3cb084d3ddd762bc0aed576
ADD --chmod=0755 --checksum=sha256:${COMPOSE_SHA256} https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-linux-x86_64 /usr/local/bin/docker-compose
COPY --from=build /out/deploy-gateway /usr/local/bin/deploy-gateway
ENV DATA_DIR=/data DOCKER_CONFIG=/docker
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/deploy-gateway"]
CMD ["serve"]
```

`.dockerignore`:

```
.git
data
workspaces
.env
*.db
docs
web/node_modules
internal/webui/dist/assets
internal/webui/dist/index.html
```

- [ ] **Step 4: Write the example host stack**

`deploy/docker-compose.yml`:

```yaml
services:
  deploy-gateway:
    image: ghcr.io/sebbi08/deployment-gateway:0.1.0
    container_name: deploy-gateway
    restart: unless-stopped
    env_file: .env
    environment:
      # Must be the identical absolute path on the host and in the container.
      WORKSPACES_ROOT: /srv/deploy-gateway/workspaces
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./data:/data
      - /srv/deploy-gateway/workspaces:/srv/deploy-gateway/workspaces
      - /root/.docker/config.json:/docker/config.json:ro
    cap_drop: [ALL]
    cap_add: [CHOWN, DAC_OVERRIDE, FOWNER]
    security_opt: ["no-new-privileges:true"]
    read_only: true
    tmpfs: [/tmp]
    labels:
      traefik.enable: true
      traefik.docker.network: all
      traefik.http.routers.deploy-gateway-mcp.rule: Host(`mcp.example.com`)
      traefik.http.routers.deploy-gateway-mcp.entrypoints: websecure
      traefik.http.routers.deploy-gateway-mcp.service: deploy-gateway-mcp
      traefik.http.services.deploy-gateway-mcp.loadbalancer.server.port: 8080
      traefik.http.routers.deploy-gateway-admin.rule: Host(`deploy-admin.example.com`)
      traefik.http.routers.deploy-gateway-admin.entrypoints: websecure
      traefik.http.routers.deploy-gateway-admin.middlewares: authelia@docker
      traefik.http.routers.deploy-gateway-admin.service: deploy-gateway-admin
      traefik.http.services.deploy-gateway-admin.loadbalancer.server.port: 8081
    networks:
      - all
networks:
  all:
    external: true
```

`deploy/.env.example`:

```dotenv
# Admin login. Generate with:  docker run --rm -i ghcr.io/sebbi08/deployment-gateway:0.1.0 hash-password
# Keep the single quotes: the hash contains $ characters that compose would otherwise interpolate.
ADMIN_PASSWORD_HASH='$argon2id$v=19$m=65536,t=3,p=2$REPLACE$REPLACE'

# Alternative: leave ADMIN_PASSWORD_HASH empty and rely on a forward-auth proxy only.
# Both of the following are then required, otherwise the gateway refuses to start.
#ADMIN_TRUSTED_PROXIES=172.19.0.149
#ADMIN_USER_HEADER=Remote-User

# Reverse proxy address(es) whose X-Forwarded-For is trusted for audit/rate limiting.
TRUSTED_PROXIES=172.19.0.149

#DEPLOY_TIMEOUT=300s
#DEPLOY_CONCURRENCY=1
```

- [ ] **Step 5: Write `README.md`**

`README.md`:

````markdown
# deploy-gateway

A small self-hosted gateway that lets AI agents deploy containerized apps to a Docker host **without** SSH, Docker socket or arbitrary compose files. Agents talk to an MCP server (streamable HTTP) or the matching JSON API; the gateway validates a strict stack description, renders a hardened `docker-compose.yml` into the agent's workspace folder, verifies it independently, runs it, and re-inspects the running containers.

Design: `docs/superpowers/specs/2026-10-01-deploy-gateway-design.md`.

## How it is secured

- Agents never send compose files, labels, volume paths, capabilities or networks. They send a JSON stack (image, command, env, exposure template, ports in their range, one volume path per service, healthcheck, resources).
- Every container runs with `cap_drop: [ALL]`, `no-new-privileges`, read-only root fs (opt-out per service), `/tmp` tmpfs, memory/CPU/pids limits, no swap.
- Volumes are bind mounts under `<WORKSPACES_ROOT>/<workspace>/<app>/data/<service>` only.
- Three checks: input allowlist → post-render verifier → runtime inspect (containers are removed on any violation and a critical audit event is written).
- Tokens are per workspace, stored as SHA-256, revocable; every call is audited with env values redacted.

## Setup on the host

1. Build and push the image (or build on the host):
   ```bash
   docker build -t ghcr.io/sebbi08/deployment-gateway:0.1.0 --build-arg VERSION=0.1.0 .
   ```
2. Create the stack folder and the workspaces folder:
   ```bash
   mkdir -p ~/docker/deploy-gateway/{data,workspaces}
   cp deploy/docker-compose.yml deploy/.env.example ~/docker/deploy-gateway/
   mv ~/docker/deploy-gateway/.env.example ~/docker/deploy-gateway/.env
   ```
3. Generate the admin password hash and paste it into `.env` (single quotes!):
   ```bash
   docker run --rm -i ghcr.io/sebbi08/deployment-gateway:0.1.0 hash-password
   ```
4. Registry access: `docker login <registry>` on the host. `~/.docker/config.json` must contain inline `auths` (no `credsStore`), because it is mounted into the gateway.
5. Start: `cd ~/docker/deploy-gateway && docker compose up -d && docker logs deploy-gateway`.

### Operator checklist (manual)

- Create the shared network for agent apps and attach Traefik to it: `docker network create apps`, then add `apps` to Traefik's networks.
- DNS: a wildcard record for the apps domain (e.g. `*.apps.example.com`) pointing to the host, and a wildcard certificate via the DNS challenge (or per-host certificates).
- Published ports bypass `ufw`. Choose each workspace's bind IP (`0.0.0.0`, the LAN IP, or `127.0.0.1`) and port range deliberately.
- Do not set `hostname:` on the gateway container; the startup mount check finds its own container by hostname.

## Admin UI

Open the admin host (e.g. `https://deploy-admin.example.com`).

1. **Templates** → create an exposure template, for example:
   - Name `public-web`, host pattern `{{app}}.{{workspace}}.apps.example.com` (or `{{app}}-{{workspace}}.apps.example.com` for a single-level wildcard cert), network `apps`
   - Labels:
     ```
     traefik.enable=true
     traefik.docker.network=apps
     traefik.http.routers.{{id}}.rule=Host(`{{host}}`)
     traefik.http.routers.{{id}}.entrypoints=websecure
     traefik.http.services.{{id}}.loadbalancer.server.port={{port}}
     ```
   The editor shows a live preview (validation errors, example host, rendered labels) while you type.
2. **Workspaces** → create a workspace (name, quota, published port range, bind IP); on its page grant templates (Templates tab) and create a token (Tokens tab, shown once with a copy button).

## Connecting an agent

Claude Code:

```bash
claude mcp add --transport http deploy https://mcp.example.com/mcp \
  --header "Authorization: Bearer dgw_..."
```

Tools: `whoami`, `list_templates`, `validate`, `deploy`, `list`, `logs`, `restart`, `stop`, `delete`. The same tools are available as `POST /api/v1/<tool>` with a JSON body.

Example stack:

```json
{"app": "shop", "stack": {"services": {
  "web": {"image": "ghcr.io/me/shop:1.4.2", "expose": {"template": "public-web", "port": 8080},
          "environment": {"DATABASE_URL": "postgres://alice-shop-db:5432/shop"}, "depends_on": ["db"]},
  "db":  {"image": "postgres:16.4", "user": "999:999", "volume": {"path": "/var/lib/postgresql/data"},
          "writable_rootfs": true, "healthcheck": {"command": ["pg_isready"]}}
}}}
```

## Development

```bash
go test ./...                       # Go unit tests, no Docker or Node needed
go test ./internal/render -run '^$' -fuzz FuzzRenderVerify -fuzztime 60s
COMPOSE_BIN=/path/to/docker-compose go test -tags integration ./internal/service -run Integration -v

cd web
npm ci
npm test                            # Vitest + React Testing Library
npm run build                       # writes internal/webui/dist (embedded on the next go build)
npm run dev                         # http://localhost:5173, proxies /api to a gateway on :8081
```

The admin UI is a React app (Vite, TypeScript, Mantine) in `web/`; its build output is embedded into the binary, so the container needs no Node at runtime. Until `npm run build` has run, the admin listener answers "admin UI not built".
````

- [ ] **Step 6: Build the image and smoke-test it**

```bash
docker build -t deploy-gateway:dev .
SMOKE=$(mktemp -d)
mkdir -p "$SMOKE/workspaces" "$SMOKE/data"
HASH=$(printf 'smoke-test-password\n' | docker run --rm -i deploy-gateway:dev hash-password 2>/dev/null)
docker run -d --name dg-smoke -p 18080:8080 -p 18081:8081 \
  -e WORKSPACES_ROOT="$SMOKE/workspaces" -e ADMIN_PASSWORD_HASH="$HASH" \
  -v /var/run/docker.sock:/var/run/docker.sock -v "$SMOKE/data:/data" \
  -v "$SMOKE/workspaces:$SMOKE/workspaces" \
  deploy-gateway:dev
sleep 2
curl -fsS http://127.0.0.1:18080/healthz
curl -s -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:18080/api/v1/whoami
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18081/
curl -s -w ' %{http_code}\n' http://127.0.0.1:18081/api/admin/me
docker logs dg-smoke
docker rm -f dg-smoke
```

Expected: `ok`, then `401`, then `200` (the React app's `index.html`), then `401` with `{"code":"login_required",...}`; the log shows `deploy-gateway started` with `admin_auth=password`.

- [ ] **Step 7: Commit**

```bash
git add Dockerfile .dockerignore deploy README.md internal/service/integration_test.go
git commit -m "chore: container image, example host stack, README, integration test"
```

---

## Spec coverage map

| Spec section | Tasks |
|---|---|
| §3 decisions, §16 config | 8, 25 |
| §4 architecture / packages | all; layout in File Structure |
| §5 naming | 1 |
| §6 folder layout, same-path mount | 15 (files), 25 (startup check), 26 (compose example) |
| §7 stack format, quotas | 2, 3, 14 |
| §8 templates, hostname uniqueness | 4, 14 (SaveTemplate), 21 (preview API), 24 (editor with live preview) |
| §9 rendering / hardening | 5 |
| §10 three check layers | 3 (layer 1), 6 + 7 (layer 2), 13 + 15 (layer 3) |
| §11 runner, deploy flow | 12, 15 |
| §12 tools / API, error codes | 14–18 |
| §13 authentication | 11, 17, 19, 22 (login / auth gate) |
| §14 admin UI | 19–21 (admin API, SPA serving), 22–24 (React app) |
| §15 data model | 9, 10 |
| §17 deployment, operator checklist | 25, 26 |
| §18 error handling | 14 (track/sanitize), 15, 25 |
| §19 testing | every task; fuzz 7; admin API 19–21; frontend 22–24; integration 26 |
