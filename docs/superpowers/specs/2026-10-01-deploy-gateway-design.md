# deploy-gateway — Design Spec

Date: 2026-10-01
Status: Draft for review

## 1. Purpose

A small, self-hosted gateway that lets semi-trusted AI agents deploy containerized apps to a Docker host **without** SSH, Docker socket, or arbitrary container configuration. Agents talk only to the gateway (MCP over streamable HTTP + a matching JSON API). The gateway is the only component that talks to Docker and is the trust boundary.

The gateway is **generic**: nothing is hard-coded to a specific host or domain. Domains, proxy networks, entrypoints, cert resolvers and middlewares are expressed in admin-defined *exposure templates*.

### Success criteria

- An admin creates a **workspace** (= token/folder pair) in a web UI and hands the token to an agent.
- With that token the agent can deploy, list, inspect logs of, restart, stop and delete apps **inside its workspace only**.
- No agent input — however malformed or malicious — can produce a container with host access (host bind mounts, docker.sock, privileged, capabilities, devices, host namespaces, custom runtimes) or affect other workspaces or the host's own stacks.
- Container configuration that reaches Docker is checked by three independent layers (input allowlist, post-render verifier, runtime inspect).

### Non-goals (v1)

Git/Dockerfile builds (agents build images in their own CI/CD), image allowlists (any image may be deployed), registry credential management (done on the host), auto-rollback, disk quota enforcement, multiple admin users, YAML stack input.

## 2. Threat model

- Agents are semi-trusted: buggy, prompt-injected, or leaking their token.
- A compromised token may only affect apps in its own workspace.
- The gateway holds the Docker socket; it must be small, auditable and strict.
- The admin is trusted (templates and quotas are admin input), but the UI guards against obvious footguns.
- Other containers on shared networks (e.g. a network shared with the reverse proxy) are untrusted with respect to the admin listener.

## 3. Key decisions

| Topic | Decision |
|---|---|
| Language | Go (official Docker SDK, official MCP Go SDK `github.com/modelcontextprotocol/go-sdk`) |
| MCP transport | Streamable HTTP behind the reverse proxy, `Authorization: Bearer <token>` |
| Agent input model | JSON object shaped like a strict compose subset; parsed with unknown-field rejection; re-rendered by the gateway |
| Execution | Gateway renders `docker-compose.yml` into the app folder and runs the standalone `docker compose` v2 binary with fixed argv |
| Folder model | Managed workspaces under `WORKSPACES_ROOT`; admin chooses the workspace name only |
| Volumes | Bind mounts only, always under `<WORKSPACES_ROOT>/<ws>/<app>/data/<svc>/`; no named volumes. Anonymous volumes from an image's `VOLUME` are allowed; the gateway removes them on app delete, workspace delete, orphan removal and when a redeploy stops mounting them |
| Images | Unrestricted; explicit tag or digest required, `:latest` rejected; always pulled on deploy |
| Registry auth | Host's `~/.docker/config.json` mounted read-only (inline `auths` only; credential helpers unsupported) |
| Networking | One private network per app stack; only services with `expose` join the template's networks; outbound internet allowed |
| Reverse proxy | Admin-defined exposure templates (labels + networks + optional host pattern), granted per workspace; hostname uniqueness guaranteed by template validation in the admin UI |
| Published ports | Allowed within a per-workspace admin-set host port range and bind IP; ranges must not overlap between workspaces |
| Exposure | Optional; services without `expose` get no hostname (workers, databases) |
| Admin auth | Built-in argon2id login, or fail-closed proxy mode when `ADMIN_PASSWORD_HASH` is empty |
| Storage | SQLite (`modernc.org/sqlite`, pure Go) |
| Admin UI | React + TypeScript SPA (Vite, Mantine, React Router, TanStack Query) talking to an admin JSON API; build output embedded into the Go binary via `go:embed` |

## 4. Architecture

Single Go binary, single container, two listeners:

- **Agent listener** (`AGENT_LISTEN`, default `:8080`): `/mcp` (MCP streamable HTTP) and `/api/v1/<tool>` (JSON). Bearer-token auth.
- **Admin listener** (`ADMIN_LISTEN`, default `:8081`): admin UI. Never served on the agent listener.

### Packages

```
cmd/deploy-gateway/      main: serve, hash-password subcommands
internal/config/         env parsing + validation
internal/store/          SQLite schema, migrations, queries
internal/names/          name regexes and composition (<ws>-<app>-<svc>)
internal/stack/          agent stack types, strict JSON decode, validation
internal/tmpl/           exposure template model, placeholder rendering, validation
internal/render/         stack + templates + quotas -> compose YAML (hardening hard-coded)
internal/verify/         independent post-render invariant checker (generic map parse)
internal/runner/         docker compose execution (fixed argv, scrubbed env, timeouts, locks)
internal/dockerx/        Docker SDK: label queries, runtime HostConfig check, logs, network checks
internal/service/        orchestration: deploy/list/logs/restart/stop/delete, quotas, port claims, audit writing + redaction
internal/agentauth/      agent bearer-token authentication (API + MCP)
internal/mcpserver/      MCP tool adapters over service
internal/api/            HTTP JSON adapters over service
internal/auth/           token hashing/lookup, admin sessions, client IP, rate limits
internal/admin/          admin JSON API, admin auth (password/proxy mode), SPA file serving
web/                     React admin app (Vite + TypeScript + Mantine); `web/dist` is embedded
```

Dependency direction: adapters (`mcpserver`, `api`, `admin`) → `service` → (`stack`, `tmpl`, `render`, `verify`, `runner`, `dockerx`, `store`). `verify` must not import `render`.

## 5. Naming

| Item | Rule |
|---|---|
| Workspace | `^[a-z][a-z0-9]{1,15}$` (no hyphens) |
| App | `^[a-z][a-z0-9-]{0,22}[a-z0-9]$` (hyphens allowed, max 24) |
| Service | `^[a-z][a-z0-9]{0,15}$` (no hyphens) |
| Container name / compose service key / DNS hostname | `<ws>-<app>-<svc>` (max 58 chars < 63 DNS label limit) |
| Compose project | `dg-<ws>-<app>` |
| Private network | `dg-<ws>-<app>` |
| Template `{{id}}` (router/service ids) | `<ws>-<app>-<svc>` |

Because workspace and service names contain no hyphens, every composed name parses back uniquely (workspace = before first hyphen, service = after last hyphen); two distinct (ws, app, svc) triples can never produce the same name. **No short aliases** are added on any network: services reach each other only by full name (e.g. `postgres://alice-shop-db:5432`). `whoami`, `validate`, `deploy` and `list` report the exact hostnames.

If a non-gateway container already has a composed name, compose fails and the deploy returns an error; the gateway never adopts or modifies containers it did not create.

## 6. Workspace folder layout

```
<WORKSPACES_ROOT>/<ws>/
└── <app>/
    ├── docker-compose.yml        # rendered by gateway
    ├── docker-compose.prev.yml   # previous successful render (kept for manual rollback)
    └── data/
        └── <svc>/                # created by gateway; bind-mounted at the agent's volume.path
```

- The gateway creates all directories. Agents never supply host paths.
- If a service sets `user: "uid:gid"` with a non-zero uid, `data/<svc>/` is chowned to that uid:gid (mode 0755); otherwise it is owned by root with mode 0700, because a root container can create setuid-root files in it. Operators should mount `WORKSPACES_ROOT` with `nosuid,nodev`.
- If `FILE_OWNER` (`uid:gid`) is set, the workspace folder, app folder, `data/` and the compose files (`docker-compose.yml`, `.prev.yml`, `.next.yml`) get group `gid` (owner stays root; folders 0750, files 0640) on creation and on every deploy, so that group can read them but not change what the root gateway runs; `data/<svc>/` is excluded (see above). Ownership changes never follow symlinks.
- `WORKSPACES_ROOT` must be mounted into the gateway container **at the identical absolute path** as on the host, because the daemon resolves bind paths on the host. Verified at startup by inspecting the gateway's own container mounts; mismatch → refuse to start.

## 7. Agent stack format

`deploy(app, stack)` / `validate(app, stack)`, where `stack` is a JSON object. Decoding uses `DisallowUnknownFields` at every level. Request body limit: 128 KB.

```json
{
  "services": {
    "web": {
      "image": "ghcr.io/me/shop:1.4.2",
      "command": ["node", "server.js"],
      "environment": { "DATABASE_URL": "postgres://alice-shop-db:5432/shop" },
      "expose": { "template": "public-web", "port": 8080 },
      "healthcheck": { "command": ["wget", "-qO-", "http://localhost:8080/health"], "interval": "10s", "retries": 3 },
      "depends_on": ["db"],
      "resources": { "memory": "256m", "cpus": 0.5 }
    },
    "db": {
      "image": "postgres:16.4",
      "user": "999:999",
      "volume": { "path": "/var/lib/postgresql/data" },
      "writable_rootfs": true
    }
  }
}
```

### Fields and validation

Top level: only `services` (map, 1..`max_services_per_app`).

Per service (all optional except `image`):

| Field | Rules |
|---|---|
| `image` | Docker reference grammar; explicit tag or digest required; tag `latest` rejected; any registry |
| `command`, `entrypoint` | Exec-form string arrays; ≤ 32 items; each ≤ 4 KB; no NUL |
| `environment` | Map; keys `^[A-Za-z_][A-Za-z0-9_]{0,63}$`; values ≤ 8 KB, no NUL; ≤ 100 entries; ≤ 64 KB total |
| `expose` | `{template, port}`; template must be granted to the workspace; port 1–65535; at most one per service. Optional — a service without `expose` gets no hostname and joins only the private stack network. If the template's `host_pattern` lacks `{{service}}`, at most one service per stack may use that template |
| `ports` | List of `{container, host, protocol?}`; ≤ 10 per service; `container` 1–65535; `host` a single port inside the workspace's published port range (no port ranges); `protocol` `tcp` (default) or `udp`; bound to the workspace's bind IP (agent cannot choose the IP). Rejected entirely if the workspace has no port range. A host port/protocol already claimed by another app of the workspace → `port_taken` |
| `volume` | `{path}`; absolute, already normalized (`path.Clean(p) == p`), no `..`, not `/`, not under `/proc`, `/sys`, `/dev`, `/tmp` (tmpfs); one per service |
| `healthcheck` | `{command, interval?, retries?}`; exec-form (rendered as `CMD`); interval 5s–5m; retries 1–10 |
| `depends_on` | List of service names in the same stack; no self-reference; no cycles |
| `resources` | `{memory?, cpus?, pids?}`; each ≤ workspace per-service max; defaults from workspace |
| `user` | Numeric `^\d{1,10}:\d{1,10}$` only |
| `writable_rootfs` | bool, default false |

Validation errors are returned as structured lists: `{code: "validation_failed", errors: [{field: "services.web.environment.FOO", message: "..."}]}`.

### Quotas (per workspace, admin-set)

`max_apps`, `max_services_per_app`, `max_total_memory` and `max_total_cpus` (summed over all services of all apps in the workspace — running or stopped, so `restart` never needs a quota re-check — counting the new deploy instead of the app's current version), `default_/max_memory`, `default_/max_cpus`, `default_/max_pids` per service. Checked in `validate` and `deploy` before rendering. Disk usage per app is measured (cached, refreshed at most every 10 min) and reported but not enforced.

## 8. Exposure templates

Admin-defined, stored in SQLite, granted to workspaces explicitly.

```yaml
name: public-web            # ^[a-z][a-z0-9-]{1,30}$
description: Public HTTPS via Traefik
host_pattern: "{{app}}.{{workspace}}.apps.example.com"   # optional
networks: [apps]
labels:
  traefik.enable: "true"
  traefik.docker.network: apps
  traefik.http.routers.{{id}}.rule: Host(`{{host}}`)
  traefik.http.routers.{{id}}.entrypoints: websecure
  traefik.http.routers.{{id}}.tls.certresolver: letsencrypt
  traefik.http.services.{{id}}.loadbalancer.server.port: "{{port}}"
```

- Placeholders: `{{workspace}}`, `{{app}}`, `{{service}}`, `{{id}}`, `{{host}}`, `{{port}}`. `host_pattern` may use only `{{workspace}}`, `{{app}}`, `{{service}}`. Unknown placeholders are rejected on save. Rendering is plain string substitution (no template language).
- All substituted values come from validated names, except `{{port}}` (validated integer).
- Rendered host must be a valid DNS name, each label ≤ 63 chars.
- Save-time checks: networks exist in Docker, are not `host`, `none`, or `bridge`, and do not start with `dg-` (reserved for per-app networks); label keys must not start with `deploy-gateway.` or with a placeholder; Traefik router and service names must contain `{{id}}`; keys must not render to the same key; labels using `{{host}}` require a `host_pattern`; template in use cannot be deleted; while deployed apps hold hosts under a template's suffix, its host pattern cannot be changed and it cannot be deleted (those apps keep their old hosts until redeployed).

### Hostname uniqueness (validated in the admin UI)

The admin decides hostnames via `host_pattern`; the admin UI rejects any template that could produce a hostname colliding with another deployment. There are no runtime host claims — uniqueness holds by construction:

- `host_pattern` is optional. Without it, the template only adds networks and labels (e.g. internal-only routing); no hostname is produced.
- If set, it has the form `<subdomain>.<suffix>`:
  - `<subdomain>` consists **only** of placeholders separated by literal `-` or `.`; each placeholder at most once; `{{workspace}}` and `{{app}}` are required, `{{service}}` is optional. Examples: `{{app}}.{{workspace}}`, `{{app}}-{{workspace}}`, `{{service}}-{{app}}-{{workspace}}`.
  - `<suffix>` is a literal, valid domain (e.g. `apps.example.com`).
- Within one template, rendered hosts are unique because workspace and service names contain no hyphens and no name contains a dot (§5), so the subdomain parses back to exactly one (workspace, app[, service]). When `{{service}}` is absent, the one-service-per-template-per-stack rule (§7) keeps hosts unique.
- Across templates, the UI rejects a suffix that equals another template's suffix or is nested in it at a label boundary (`apps.example.com` vs `x.apps.example.com`). Two templates can therefore never produce the same host.
- Separation from manually deployed stacks is the admin's choice of suffix (e.g. manual stacks on `x.example.com`, gateway apps on `x.apps.example.com`). The template editor shows a live preview of example hosts.

## 9. Rendering and hardening

`render` produces, for each service, a compose service keyed `<ws>-<app>-<svc>` with:

- `image`, `command`, `entrypoint`, `environment` from the stack; **every `$` in agent-supplied strings and in rendered template label values is escaped to `$$`** so compose performs no interpolation.
- `container_name: <ws>-<app>-<svc>`, `hostname: <ws>-<app>-<svc>`
- `restart: unless-stopped`
- `cap_drop: [ALL]`, `security_opt: ["no-new-privileges:true"]`
- `read_only: true` unless `writable_rootfs`; `tmpfs: ["/tmp"]` always
- `mem_limit`, `memswap_limit` (= `mem_limit`), `cpus`, `pids_limit`
- `logging: {driver: json-file, options: {max-size: 10m, max-file: "3"}}`
- `user` if given
- `volumes`: at most one bind `<WORKSPACES_ROOT>/<ws>/<app>/data/<svc>:<volume.path>`
- `ports` in long syntax: `{target, published, host_ip: <workspace bind IP>, protocol}` — never short syntax, never ranges
- `healthcheck` with `test: ["CMD", ...]`
- `depends_on` with `condition: service_healthy` if the dependency has a healthcheck, else `service_started` (keys mapped to full names)
- `networks`: `dg-<ws>-<app>` (no aliases) plus, for exposed services, the template's networks (no aliases)
- `labels`: `deploy-gateway.workspace`, `deploy-gateway.app`, `deploy-gateway.service`, `deploy-gateway.managed=true`, plus the rendered template labels

Top-level `networks`: `dg-<ws>-<app>` (gateway-managed, created by compose) and template networks as `external: true`. No top-level `volumes`, `configs`, `secrets`, `include`, `x-*`.

Output is marshalled with `gopkg.in/yaml.v3` from typed structs (never string concatenation).

## 10. Three check layers

1. **Input allowlist** — §7. Unknown fields rejected; strict value validation.
2. **Post-render verifier** (`verify`) — parses the rendered YAML into `map[string]any` independently of `render` and asserts:
   - Top-level keys ⊆ {`name`, `services`, `networks`}; service keys ⊆ the set emitted in §9.
   - Explicitly forbidden anywhere in a service: `privileged`, `cap_add`, `devices`, `device_cgroup_rules`, `network_mode`, `pid`, `ipc`, `uts`, `userns_mode`, `cgroup`, `cgroup_parent`, `runtime`, `extra_hosts`, `build`, `extends`, `include`, `configs`, `secrets`, `sysctls`, `ulimits`, `volumes_from`, `links`, `external_links`, `security_opt` other than exactly `no-new-privileges:true`, `storage_opt`, `isolation`, `gpus`.
   - Each `ports` entry is long syntax with a single `published` port inside the workspace range, `host_ip` equal to the workspace bind IP, protocol `tcp`/`udp`; none if the workspace has no range.
   - `cap_drop == [ALL]`; `mem_limit`, `memswap_limit`, `cpus`, `pids_limit` present and within quota.
   - Each volume is a bind whose host path, after `filepath.EvalSymlinks` on the existing parent, is inside `<WORKSPACES_ROOT>/<ws>/<app>/data/`; no path contains `docker.sock`; no named volumes.
   - Networks ⊆ {`dg-<ws>-<app>`} ∪ networks of templates granted to the workspace; no aliases.
   - Owner labels equal the token's workspace/app; only the gateway's own four `deploy-gateway.*` labels exist.
   - No unescaped `$` (single `$` not followed by `$`) in any string value.
   Verifier failure aborts the deploy before compose runs and is audited as `critical` (it indicates a gateway bug).
3. **Runtime inspect** (`dockerx`) — after `compose up`, for each container of the project: `Privileged=false`, `CapAdd` empty, `CapDrop` contains `ALL`, `NetworkMode` not `host`, `PidMode`/`IpcMode`/`UTSMode`/`UsernsMode` not `host`, `Devices` empty, `Binds`/`Mounts` only under the app data dir, tmpfs, or anonymous (64-hex-named) volumes from an image's `VOLUME` instruction, `Memory` and `PidsLimit` set, `Runtime` is the daemon default, `SecurityOpt` contains `no-new-privileges:true`, `PortBindings` exactly match the rendered ports (host IP and port), owner labels match. Containers that already existed before this `up` (left behind when `up` fails early) get every check except the exact match of labels and ports against the new render; they were checked against their own render when deployed. On any mismatch: `compose down`, audit `critical`, return error.

## 11. Runner

- Invocation: `docker-compose -f <app>/docker-compose.yml -p dg-<ws>-<app> <subcommand>` via `exec.CommandContext` (no shell).
- Environment: scrubbed; only `PATH`, `HOME=/tmp`, `DOCKER_CONFIG`, `DOCKER_HOST=unix:///var/run/docker.sock`. All `COMPOSE_*` unset.
- Deploy: `pull`, then `up -d --pull never --remove-orphans --wait --wait-timeout <DEPLOY_TIMEOUT>`.
- Other: `down -v --remove-orphans` (delete; `-v` removes anonymous volumes created by an image's `VOLUME` instruction), `restart [svc]`, `stop [svc]`. Logs and status via Docker SDK, not compose.
- Process group killed on context timeout.
- Concurrency: per-app mutex; global semaphore `DEPLOY_CONCURRENCY` (default 1). Each workspace has at most one compose operation waiting for the global semaphore, so one tenant cannot queue all others behind its operations. The wait is bounded by `DEPLOY_TIMEOUT`; after that the operation fails with "the gateway is busy" and changes nothing.
- Deploy flow: validate → quota → render to `docker-compose.next.yml` → verify → `pull` (of next) → image-label check → re-check quota/ports and commit the reservation and port claims → rotate (`current → prev`, `next → current`) → `up` → runtime inspect → every planned service must have a running container, healthy if it has a healthcheck (compose's exit code alone is not trusted). A failure before the commit changes no DB state and leaves the running deployment and its reservation untouched (a first deploy leaves no app record). On `up` failure, the app keeps the larger of its previous and new memory/CPU reservation until a deploy succeeds, because containers with the previous limits may still run; return the error plus the last 50 log lines of failing services, and state that `docker-compose.prev.yml` is available.

## 12. Agent tools / API

Each MCP tool has a matching `POST /api/v1/<tool>` with the same JSON input/output. Request bodies are limited to 128 KB on both endpoints (plus 4 KB for the JSON-RPC envelope on `/mcp`); JSON-RPC batches are rejected with 400 for every protocol version, because the SDK would run all calls of a batch concurrently. The body is read completely (within 30 s) before the token is checked, so a revoked token cannot finish a request whose body it held back.

| Tool | Input | Output |
|---|---|---|
| `whoami` | — | workspace, quotas, current usage, published port range and bind IP, ports in use, naming scheme, agent guidance (e.g. use `user:` for images that drop privileges) |
| `list_templates` | — | granted templates: name, description, host pattern |
| `validate` | `app`, `stack` | errors, or rendered hostnames, URLs, published ports and resource totals; starts nothing |
| `deploy` | `app`, `stack` | per-service status, health, hostname, URL (if exposed), published ports |
| `list` | — | apps: services, state, health, URL (if exposed), published ports, disk usage, last deploy time/result |
| `logs` | `app`, `service`, `tail` (default 200, max 1000), `since` | combined stdout/stderr, capped at 256 KB |
| `restart` | `app`, `service?` | status |
| `stop` | `app`, `service?` | status |
| `delete` | `app`, `confirm` (must equal `app`) | ok; removes containers, network, app folder, port claims |

Ownership on every call: workspace comes only from the token (DB lookup); the app must exist in the workspace's DB records and folder; affected containers are found by Docker label filter `deploy-gateway.workspace=<ws>,deploy-gateway.app=<app>`. A request for an app in another workspace returns `not_found` (indistinguishable from nonexistent).

Error codes: `unauthorized`, `validation_failed`, `quota_exceeded`, `port_taken`, `not_found`, `conflict` (app locked), `deploy_failed`, `security_violation`, `too_many_requests` (HTTP 429: more than 8 concurrent calls, or more than a burst of 30 calls refilled at 5 per second, with one token), `internal`. Docker/compose output to agents is sanitized (truncated, no host paths outside the workspace); the full output goes to the audit log.

## 13. Authentication

### Agent tokens

- Format `dgw_<43 chars base64url>` (256 bits). Shown once at creation.
- Stored as SHA-256 hash plus an 8-char display prefix. Lookup by hash; constant-time compare.
- Multiple tokens per workspace; revocable; `last_used_at` tracked.
- Failed auth: uniform 401; per-IP rate limit (10 failures within 1 min → 15 min block). The block applies to failing attempts only: a valid token is always accepted, because the client IP may be a shared proxy address. Failures and the start of a block are logged with the client IP.
- Client IP from `X-Forwarded-For` only if the direct peer is in `TRUSTED_PROXIES`.

### Admin

- **Password mode** (`ADMIN_PASSWORD_HASH` set): argon2id hash (generated by `deploy-gateway hash-password`); random session ID in a cookie `__Host-dg_admin` (`HttpOnly; Secure; SameSite=Strict; Path=/`, host-only, so sibling subdomains cannot set or shadow it), sessions held in memory (a restart logs the admin out); 12 h idle timeout; login rate-limited per client IP (taken from `X-Forwarded-For` when the peer is in `ADMIN_TRUSTED_PROXIES` or `TRUSTED_PROXIES`).
- **Proxy mode** (`ADMIN_PASSWORD_HASH` empty): requires `ADMIN_TRUSTED_PROXIES` (the reverse proxy's pinned single address; a range warns at startup, because every container with an address in it could assert an identity) and a forward-auth middleware on the admin router that sets or overwrites the identity header and `ADMIN_USER_HEADER` (e.g. `Remote-User`); missing config → refuse to start. Requests from other peers → 403; missing/empty header → 401. Header value recorded as audit actor.
- CSRF: Go's `http.CrossOriginProtection` (Fetch metadata / `Origin` checks) on every request in both modes, plus `SameSite=Strict` cookies. The SPA uses same-origin `fetch` only.

## 14. Admin UI

A React single-page app served by the admin listener only. It talks exclusively to the admin JSON API on the same origin.

### Frontend (`web/`)

- React + TypeScript, built with Vite; Mantine for components, forms, modals, notifications and dark mode; React Router; TanStack Query for data fetching and cache invalidation.
- Pages:
  - **Login** (password mode only).
  - **Dashboard:** usage per workspace, recent critical audit events, orphan banner.
  - **Workspaces:** table plus "New workspace" modal (name, quota, published port range, bind IP).
  - **Workspace detail** (tabs): *Apps* (state, health, URLs, ports, disk usage, stop, delete with type-to-confirm), *Tokens* (create, revoke, last used; a new token is shown exactly once in a modal with a copy button), *Templates* (grant checkboxes), *Quota & ports*, *Danger zone* (delete workspace with type-to-confirm: compose down all apps, revoke tokens, release port claims, remove folder).
  - **Templates:** list and editor with **live preview** while typing (debounced call to the preview endpoint: validation errors, example hosts, rendered labels).
  - **Audit log:** filters (workspace, action, result, severity), cursor pagination, params JSON viewer.
  - **Settings:** default quota for new workspaces, audit retention days.
  - **Orphans:** containers with `deploy-gateway.managed=true` whose workspace/app is unknown; remove button.
- Development: `npm run dev` proxies `/api` to a locally running gateway.

### Admin JSON API (`/api/admin/`)

| Method & path | Purpose |
|---|---|
| `POST login`, `POST logout`, `GET me` | password login / logout; `me` returns actor and proxy mode |
| `GET dashboard` | workspace usage, recent critical events, orphan count |
| `GET`, `POST workspaces` | list / create |
| `GET`, `PUT`, `DELETE workspaces/{name}` | detail (quota, ports, grants, tokens, apps) / update quota+ports / delete (`{"confirm": name}`) |
| `PUT workspaces/{name}/grants` | set granted template IDs |
| `POST workspaces/{name}/tokens`, `POST workspaces/{name}/tokens/{id}/revoke` | create (token returned once) / revoke |
| `POST workspaces/{name}/apps/{app}/stop`, `DELETE workspaces/{name}/apps/{app}` | stop / delete (`{"confirm": app}`) |
| `GET`, `POST templates`, `GET`, `PUT`, `DELETE templates/{id}` | template CRUD (save-time checks and hostname-uniqueness validation from §8) |
| `POST templates/preview` | validate and render sample values; never saves |
| `GET audit` | filters + `before` cursor |
| `GET`, `PUT settings` | default quota, audit retention |
| `GET orphans`, `DELETE orphans/{id}` | list / remove |

- Errors use the same `{code, message, errors}` structure as the agent API; admin responses additionally include the internal `detail` (the admin is trusted).
- Request bodies are decoded strictly (unknown fields rejected).

### Serving

- `web/dist` is embedded with `go:embed`. Paths under `/api/` go to the API; every other path serves a static asset if it exists, otherwise `index.html` (client-side routing).
- Hashed assets: `Cache-Control: public, max-age=31536000, immutable`; `index.html` and API responses: `no-store`.
- Headers: `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; form-action 'self'`, `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`. (`'unsafe-inline'` for styles is required by Mantine's runtime CSS variables; no inline scripts are allowed.)
- `go test ./...` must work without Node: `web/dist` contains a committed placeholder, and the server answers non-API paths with "admin UI not built" until `npm run build` has produced `index.html`.
- Node and `node_modules` exist only at build time (Docker build stage, `npm ci` from the committed lockfile).

All admin actions are audited with `actor_type=admin`.

## 15. Data model (SQLite)

```
workspaces(id, name UNIQUE, created_at,
           max_apps, max_services_per_app, max_total_memory_mb, max_total_cpus,
           default_memory_mb, max_memory_mb, default_cpus, max_cpus, default_pids, max_pids,
           port_range_start NULL, port_range_end NULL, port_bind_ip)
tokens(id, workspace_id FK, label, hash UNIQUE, prefix, created_at, last_used_at, revoked_at)
templates(id, name UNIQUE, description, host_pattern, networks_json, labels_json, created_at, updated_at)
workspace_templates(workspace_id FK, template_id FK, PRIMARY KEY(workspace_id, template_id))
apps(id, workspace_id FK, name, memory_bytes, cpus, hosts_json, created_at, updated_at, last_deploy_at, last_deploy_result,
     UNIQUE(workspace_id, name))
port_claims(protocol, host_port, container_port, service, app_id FK, PRIMARY KEY(protocol, host_port))
audit(id, ts, actor_type, actor, workspace, token_id, action, params_json, result, error,
      severity, duration_ms, client_ip)
settings(key PRIMARY KEY, value)
```

Audit params redact environment **values** (`***`); keys are kept. Params are stored without HTML escaping and limited to 8 KiB; larger params are replaced by a summary (app, service names and images, size and SHA-256 of the full redacted params). Retention job deletes rows older than the configured days (default 90).

## 16. Configuration (env)

| Var | Default | Notes |
|---|---|---|
| `AGENT_LISTEN` | `:8080` | |
| `ADMIN_LISTEN` | `:8081` | |
| `DATA_DIR` | `/data` | SQLite location |
| `WORKSPACES_ROOT` | required | absolute; must match host path |
| `ADMIN_PASSWORD_HASH` | empty | empty → proxy mode |
| `ADMIN_TRUSTED_PROXIES` | — | required in proxy mode |
| `ADMIN_USER_HEADER` | — | required in proxy mode |
| `TRUSTED_PROXIES` | empty | for agent client IP extraction |
| `DOCKER_CONFIG` | `/docker` | contains host `config.json` |
| `COMPOSE_BIN` | `/usr/local/bin/docker-compose` | |
| `DEPLOY_TIMEOUT` | `300s` | |
| `DEPLOY_CONCURRENCY` | `1` | env only |
| `FILE_OWNER` | empty | optional numeric `uid:gid`; group `gid` gets read access to gateway-managed folders and compose files (owner stays root) |

## 17. Deployment

- Image: multi-stage; `node` stage builds `web/` (`npm ci && npm run build`) → `golang` stage builds the binary with the embedded UI → `gcr.io/distroless/static` + gateway binary + standalone `docker-compose` v2 binary (pinned version, SHA-256 verified).
- Example stack `deploy/docker-compose.yml` (pinned image tag, `container_name: deploy-gateway`, `restart: unless-stopped`):
  - mounts: `/var/run/docker.sock`, `./data:/data`, `<abs>/workspaces:<abs>/workspaces`, `~/.docker/config.json:/docker/config.json:ro`
  - `cap_drop: [ALL]`, `cap_add: [CHOWN, DAC_OVERRIDE, FOWNER]`, `read_only: true`, `tmpfs: /tmp`, `no-new-privileges:true`
  - the reverse-proxy network; Traefik routers: agent host → 8080; admin host → 8081 (optionally behind a forward-auth middleware)
  - the gateway does not join `apps`
- Startup checks (fail fast): Docker reachable; compose binary works; same-path workspaces mount; `WORKSPACES_ROOT` contains no symlinks; admin auth config valid. Warnings only: template networks missing (so the admin UI that fixes them stays reachable); orphaned labelled containers.
- Apps keep running if the gateway is down (`restart: unless-stopped`).

### Operator checklist (manual, not automated by the gateway)

- Create external network `apps`; attach Traefik to it.
- Configure `default-address-pools` in `daemon.json` (e.g. base `10.201.0.0/16`, size 24): every app needs one network, and the built-in pools allow about 31 host-wide. Startup warns when they are not configured.
- Wildcard DNS for the apps domain (e.g. `*.apps.example.com`) and a wildcard certificate via the DNS challenge, or per-host certs.
- Host `~/.docker/config.json` must use inline `auths`, not a `credsStore`.
- Published ports: Docker port publishing bypasses `ufw`/host firewall rules. Anything published on bind IP `0.0.0.0` is reachable from every network the host is on, without Traefik or Authelia. Choose each workspace's bind IP (`0.0.0.0`, LAN IP, or `127.0.0.1`) and port range deliberately, and keep ranges clear of ports used by host services.
- Create exposure templates and the first workspace in the admin UI.

## 18. Error handling

- Structured errors per §12. Validation errors list every failing field, not just the first.
- Compose timeouts kill the process group; the app lock is released; the error states the timeout.
- Verifier or runtime-inspect violations: deploy aborted/rolled down, `critical` audit, generic `security_violation` to the agent.
- Docker unreachable: `internal` error; health endpoint `/healthz` on the agent listener reports degraded.
- SQLite in WAL mode; all multi-step DB changes in transactions.

## 19. Testing

- **Unit (table-driven):** names (including hyphen-collision cases and length limits), stack decode/validation with an escape corpus (unknown keys like `privileged`/`cap_add`, `$`/newline/NUL in env, `..`/`/proc`/non-normalized volume paths, `:latest`, oversize payloads, dependency cycles, ports outside range / port ranges / ports without a workspace range, two services on a `{{service}}`-less template), template validation (host pattern grammar, missing `{{workspace}}`/`{{app}}`, literal text in subdomain, equal and nested suffixes) and placeholder rendering, workspace port-range overlap checks, quota math, token hashing, proxy-mode IP/header checks.
- **Render golden files** for representative stacks.
- **Verifier** fed hand-written malicious compose files: each forbidden key, bind outside data dir, symlinked bind, docker.sock, foreign network, foreign owner label, unescaped `$`, short-syntax ports, published port outside range, wrong `host_ip` → must fail. Tested independently of `render`.
- **Fuzz:** `go test -fuzz` — any input that passes stack validation must render to output that passes the verifier.
- **Integration** (`//go:build integration`, real Docker): deploy `nginxinc/nginx-unprivileged` with a template, assert runtime inspect, logs, restart, stop, delete; cross-workspace access returns `not_found`; published port reachable on the bind IP and runtime `PortBindings` checked; port claim conflict returns `port_taken`.
- **MCP adapter:** go-sdk client over streamable HTTP (httptest) exercising each tool and bearer auth.
- **Admin API:** httptest coverage of both auth modes, cross-origin rejection, login rate limit and every endpoint.
- **Admin UI:** Vitest + React Testing Library with a mocked API for login, create workspace, token shown once, template live preview (errors and hosts); `tsc --noEmit` and `npm run build` must pass.
