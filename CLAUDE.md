# deploy-gateway

A small, self-hosted service that lets AI agents deploy apps to my home server **without** giving them SSH, Docker socket, or Coolify-style full-API access. Agents talk only to this gateway (MCP server + small HTTP API); the gateway is the only component that talks to Docker.

## Why this exists

I tried Coolify first and removed it again. Any tool that lets a client define arbitrary containers (compose files, `docker run` flags) is equivalent to root on the host: a single `volumes: ["/:/host"]`, `privileged: true`, or a mounted `docker.sock` escapes. A separate VM/server isn't an option — the server is resource-limited. So safety must come from the gateway **never accepting container configuration from agents**, only a small set of parameters that get rendered into a fixed, hardened template.

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

- Join only the dedicated `apps` network (not `all` — see below). Traefik must be attached to `apps` too.
- Get Traefik labels for `https://<app>.apps.sebmahr.de` on entrypoint `websecure`, with `traefik.docker.network: apps`.
- Have hard limits: `mem_limit`, `cpus`, `pids_limit` (defaults configurable per agent, low by default — the server has 4 cores / 15 GB RAM and already swaps).
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

## My server (context)

- Ubuntu, 4 cores, 15 GB RAM, Docker 28.x, all services as docker compose stacks in `~/docker/<name>/docker-compose.yml`.
- Reverse proxy: Traefik v3 (`~/docker/traefik`), static IP `172.19.0.149` on the shared external network `all`. TLS via entrypoint `websecure`; DNS challenge with Netcup. Check whether a wildcard cert for `*.apps.sebmahr.de` is possible/needed and whether DNS for it exists — ask me before changing Traefik or DNS.
- Authentication middleware available: `authelia@docker` (I use it for internal tools). The gateway's own admin UI/endpoints (if any) should sit behind it; agent API uses tokens instead.
- Existing MCP servers live in `~/docker/mcp` (e.g. `ghcr.io/tbxark/mcp-proxy`), exposed on the `all` network. The gateway should be deployable the same way.
- Host LAN IP: `192.168.0.53`.

### My docker-compose conventions

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
      traefik.http.routers.myapp.rule: Host(`myapp.sebmahr.de`)
      traefik.http.routers.myapp.entrypoints: websecure
      traefik.http.services.myapp.loadbalancer.server.port: 8080
      wud.tag.include: ^\d+\.\d+\.\d+$
      wud.watch: true
    networks:
      - all
networks:
  all:
    external: true
```

- All data for a stack lives in its folder (`~/docker/<name>/...`) as bind mounts. (Exception to discuss: agent app volumes — bind mounts under the gateway's data folder may be preferable to named volumes to follow this rule; the gateway creates the directory, agents never choose paths.)
- Secrets in a `.env` next to the compose file (gitignored).

## Lessons from the Coolify attempt (don't repeat)

- **Hostname collisions on `all`:** generic service names like `redis`/`postgres` resolve to _other stacks'_ containers on the shared network. Always use unique container names for internal dependencies, and keep agent apps off `all` entirely.
- **`host.docker.internal:host-gateway`** resolves to an IPv6 address first here and containers have no IPv6 route → use `192.168.0.53` if host access is ever needed (the gateway shouldn't need it).
- **fail2ban** is active on SSH and bans container IPs after failed logins. Another reason not to use SSH at all.
- `sshd_config` has `PermitRootLogin no` — keep it that way.

## Open decisions (ask me, don't assume)

- Language/stack for the gateway (my suggestion: TypeScript or Go; must have a good Docker SDK and MCP SDK).
- Transport for MCP (streamable HTTP behind Traefik vs. stdio via mcp-proxy).
- Whether git-based builds are needed in v1 or image-only is enough.
- Wildcard DNS/cert for `*.apps.sebmahr.de`.
- Whether agent apps may make outbound internet connections (default: yes) and whether they may talk to each other (default: no — consider a network per agent).
