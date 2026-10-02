# deploy-gateway

A small self-hosted gateway that lets AI agents deploy containerized apps to a Docker host **without** SSH, Docker socket or arbitrary compose files. Agents talk to an MCP server (streamable HTTP) or the matching JSON API; the gateway validates a strict stack description, renders a hardened `docker-compose.yml` into the agent's workspace folder, verifies it independently, runs it, and re-inspects the running containers.

Design: `docs/superpowers/specs/2026-10-01-deploy-gateway-design.md`.

## How it is secured

- Agents never send compose files, labels, volume paths, capabilities or networks. They send a JSON stack (image, command, env, exposure template, ports in their range, one volume path per service, healthcheck, resources).
- Every container runs with `cap_drop: [ALL]`, `no-new-privileges`, read-only root fs (opt-out per service), `/tmp` tmpfs, memory/CPU/pids limits, no swap.
- Volumes are bind mounts under `<WORKSPACES_ROOT>/<workspace>/<app>/data/<service>` only.
- Three checks: input allowlist → post-render verifier → runtime inspect (containers are removed on any violation and a critical audit event is written).
- Images are pulled before any container is created and their `LABEL`s are inspected: Docker merges image labels into container labels, so an image carrying a key under `traefik.`, `deploy-gateway.`, `com.docker.compose.` or the first segment of any stored template's label keys (e.g. `wud.` for a template label `wud.watch`) is rejected with `validation_failed` (`services.<svc>.image`). The runtime inspect additionally rejects any container label in those namespaces (except `com.docker.compose.*`, which compose sets itself) that the gateway did not render.
- Tokens are per workspace, stored as SHA-256, revocable; every call is audited with env values redacted.

## Setup on the host

1. Build and push the image (or build on the host):
   ```bash
   docker build -t ghcr.io/sebmahr/deploy-gateway:0.1.0 --build-arg VERSION=0.1.0 .
   ```
2. Create the stack folder and the workspaces folder:
   ```bash
   mkdir -p ~/docker/deploy-gateway/{data,workspaces}
   cp deploy/docker-compose.yml deploy/.env.example ~/docker/deploy-gateway/
   mv ~/docker/deploy-gateway/.env.example ~/docker/deploy-gateway/.env
   ```
3. Generate the admin password hash and paste it into `.env` (single quotes!):
   ```bash
   docker run --rm -i ghcr.io/sebmahr/deploy-gateway:0.1.0 hash-password
   ```
4. Registry access: `docker login <registry>` on the host. `~/.docker/config.json` must contain inline `auths` (no `credsStore`), because it is mounted into the gateway.
5. Start: `cd ~/docker/deploy-gateway && docker compose up -d && docker logs deploy-gateway`.

### Operator checklist (manual)

- Create the shared network for agent apps and attach Traefik to it: `docker network create apps`, then add `apps` to Traefik's networks.
- DNS: a wildcard record for the apps domain (e.g. `*.apps.sebmahr.de`) pointing to the host, and a wildcard certificate via the DNS challenge (or per-host certificates).
- Published ports bypass `ufw`. Choose each workspace's bind IP (`0.0.0.0`, the LAN IP, or `127.0.0.1`) and port range deliberately.
- The startup mount check finds the gateway's own container by hostname, and startup fails if it runs in a container it cannot find. Either keep the default hostname or set `SELF_CONTAINER_ID` to the container name/ID (e.g. `SELF_CONTAINER_ID=deploy-gateway`, matching `container_name`).

## Configuration notes

Configuration is read from environment variables (see `deploy/.env.example`): `ADMIN_PASSWORD_HASH`, `ADMIN_TRUSTED_PROXIES` / `ADMIN_USER_HEADER`, `TRUSTED_PROXIES`, `DEPLOY_TIMEOUT`, `DEPLOY_CONCURRENCY`, `WORKSPACES_ROOT`, `DATA_DIR`. Optional: `SELF_CONTAINER_ID` (name or ID of the gateway's own container, e.g. `deploy-gateway`; needed only when the container has a custom `hostname:`, because the startup mount check otherwise finds itself by hostname and fails closed if it cannot).

## Admin UI

Open the admin host (e.g. `https://deploy-admin.sebmahr.de`).

1. **Templates** → create an exposure template, for example:
   - Name `public-web`, host pattern `{{app}}.{{workspace}}.apps.sebmahr.de` (or `{{app}}-{{workspace}}.apps.sebmahr.de` for a single-level wildcard cert), network `apps`
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
claude mcp add --transport http deploy https://deploy-mcp.sebmahr.de/mcp \
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
