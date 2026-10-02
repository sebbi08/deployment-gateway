# deploy-gateway

A small, self-hosted service that lets AI agents deploy apps to a self-hosted Docker server **without** giving them SSH, Docker socket, or Coolify-style full-API access. Agents talk only to this gateway (MCP server + small HTTP API); the gateway is the only component that talks to Docker.

## Why this exists

Tools like Coolify are not an option: Any tool that lets a client define arbitrary containers (compose files, `docker run` flags) is equivalent to root on the host: a single `volumes: ["/:/host"]`, `privileged: true`, or a mounted `docker.sock` escapes. A separate VM/server per agent is often not an option on small, resource-limited hosts. So safety must come from the gateway **never accepting container configuration from agents**, only a small set of parameters that get rendered into a fixed, hardened template.

## Threat model

- Agents are semi-trusted: they may be buggy, prompt-injected, or leak their token.
- A compromised agent token must only be able to affect **that agent's own apps** — never the host, other stacks, or other agents' apps.
- The gateway itself holds the Docker socket, so it is the trust boundary. Keep it small, auditable, and strict about input validation.

## Required behaviour

Agent-facing operations (exposed as MCP tools and matching HTTP endpoints):

- `deploy` — create or update an app from **either** a prebuilt image (`image:tag`) **or** a git repo + ref built with its **Dockerfile** (no compose files from agents, ever).
  - Agent-controlled inputs, and nothing else: app name, image or repo/ref, container port, env vars, optional health path, optional persistent volume (yes/no + size hint).
- `list` — apps owned by the calling agent, with status and URL.
- `logs` — recent logs of one of its apps.
- `restart`, `stop`, `delete` — own apps only.

Every generated container **must**:

- Join only a dedicated network for agent apps (e.g. `apps`), never a network shared with the host's own stacks. The reverse proxy must be attached to it too.
- Get reverse-proxy routing (e.g. Traefik labels for `https://<app>.apps.example.com`) only via admin-defined exposure templates.
- Have hard limits: `mem_limit`, `cpus`, `pids_limit` (defaults configurable per workspace, low by default — target hosts are small and shared).
- Run with `cap_drop: [ALL]`, `security_opt: [no-new-privileges:true]`, non-privileged, `read_only` root fs where feasible (+ tmpfs `/tmp`).
- Never get: host bind mounts, `docker.sock`, `privileged`, `cap_add`, `devices`, `network_mode: host`, `pid`/`ipc: host`, custom `runtime`, `extra_hosts` to the host.
- Persistent data only via a gateway-created named volume `<agent>-<app>-data`.
- Be labelled with owner info (e.g. `deploy-gateway.owner=<agent>`, `deploy-gateway.app=<app>`); ownership checks use these labels, never agent-supplied data.

Validation:

- App names: strict regex (e.g. `^[a-z][a-z0-9-]{1,30}$`), prefixed with the agent id internally so agents can't collide with or target existing containers.
- Env vars: key regex, size limits. Reject anything that looks like an attempt to inject labels/options.
- Git builds: build with plain `docker build` of the repo's Dockerfile — no build args that reach the host, no `--network host`, time and size limits. Consider BuildKit with no host access.

Auth:

- One token per agent, stored hashed. Per-agent quotas (max apps, max total memory).
- Audit log of every call (agent, action, params, result).

## Reference deployment (example values)

The gateway is generic; nothing may be hard-coded to a specific host. A typical target looks like this — use it for examples, never as defaults in code:

- A small Linux host (e.g. 4 cores / 16 GB RAM), Docker 28+, every service as a docker compose stack in its own folder (e.g. `/srv/<name>/docker-compose.yml`).
- Reverse proxy: Traefik v3 on a shared external network (e.g. `proxy`), TLS on entrypoint `websecure`, certificates via DNS challenge. Ask before changing the operator's proxy or DNS configuration (e.g. a wildcard cert for `*.apps.example.com`).
- Optionally a forward-auth middleware (Authelia, oauth2-proxy, …). The gateway's admin UI may sit behind it; the agent API uses tokens instead.
- Other MCP servers may run as compose stacks on the same proxy network; the gateway is deployable the same way.

### Docker compose conventions (example)

```yaml
services:
  myapp:
    image: vendor/app:1.2.3 # pinned versions, no :latest
    container_name: myapp # unique, descriptive names
    restart: unless-stopped
    volumes:
      - ./data:/data # only bind mounts inside the stack folder, no named volumes
    labels:
      traefik.enable: true
      traefik.http.routers.myapp.rule: Host(`myapp.example.com`)
      traefik.http.routers.myapp.entrypoints: websecure
      traefik.http.services.myapp.loadbalancer.server.port: 8080
    networks:
      - proxy
networks:
  proxy:
    external: true
```

- All data for a stack lives in its folder as bind mounts. Agent app volumes follow this rule: bind mounts under the gateway's workspaces folder, created by the gateway; agents never choose host paths.
- Secrets in a `.env` next to the compose file (gitignored).

## Lessons from the Coolify attempt (don't repeat)

- **Hostname collisions on shared networks:** generic service names like `redis`/`postgres` resolve to _other stacks'_ containers on a shared network. Always use unique container names for internal dependencies, and keep agent apps off networks shared with other stacks.
- **`host.docker.internal:host-gateway`** may resolve to an IPv6 address first while containers have no IPv6 route → use the host's LAN IPv4 address if host access is ever needed (the gateway shouldn't need it).
- **fail2ban** on SSH may ban container IPs after failed logins. Another reason not to use SSH at all.
- Never require root SSH logins (`PermitRootLogin no` should stay).

## Decisions

Settled in the design spec (`docs/superpowers/specs/2026-10-01-deploy-gateway-design.md`, which supersedes details above): Go; MCP over streamable HTTP behind the reverse proxy; images only (agents build in their own CI); workspaces as token/folder pairs; a restricted, re-rendered compose subset instead of agent compose files; admin-defined exposure templates; per-app private networks with outbound internet allowed. Ask before changing any of these.
