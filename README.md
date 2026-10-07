# deploy-gateway

A small self-hosted gateway that lets AI agents deploy containerized apps to a Docker host **without** SSH, Docker socket or arbitrary compose files. Agents talk to an MCP server (streamable HTTP) or the matching JSON API; the gateway validates a strict stack description, renders a hardened `docker-compose.yml` into the agent's workspace folder, verifies it independently, runs it, and re-inspects the running containers.

Design: `docs/superpowers/specs/2026-10-01-deploy-gateway-design.md`.

## How it is secured

- Agents never send compose files, labels, volume paths, capabilities or networks. They send a JSON stack (image, command, env, exposure template, ports in their range, one volume path per service, healthcheck, resources).
- Every container runs with `cap_drop: [ALL]`, `no-new-privileges`, read-only root fs (opt-out per service), `/tmp` tmpfs, memory/CPU/pids limits, no swap.
- Volumes are bind mounts under `<WORKSPACES_ROOT>/<workspace>/<app>/data/<service>` only. Paths an image declares with `VOLUME` (e.g. postgres) get anonymous Docker volumes in Docker's own storage, not in the workspace folder (so backups of the folder miss them); the gateway removes them when the app is deleted or a redeploy stops using them.
- Three checks: input allowlist → post-render verifier → runtime inspect (containers are removed on any violation and a critical audit event is written).
- Images are pulled before any container is created and their `LABEL`s are inspected: Docker merges image labels into container labels, so an image carrying a key under `traefik.`, `deploy-gateway.`, `com.docker.compose.` or the first segment of any stored template's label keys (e.g. `wud.` for a template label `wud.watch`) is rejected with `validation_failed` (`services.<svc>.image`). The runtime inspect additionally rejects any container label in those namespaces (except `com.docker.compose.*`, which compose sets itself) that the gateway did not render.
- Tokens are per workspace, stored as SHA-256, revocable; every call is audited with env values redacted.

## Setup on the host

1. Release the image: on GitHub, run **Actions → Release image → Run workflow** with a version such as `0.1.0`. The workflow runs the Go and frontend tests, pushes `ghcr.io/sebbi08/deployment-gateway:<version>` (amd64) and tags the commit `v<version>`; an existing version is never overwritten. The GHCR package is private by default — either make it public in the package settings or `docker login ghcr.io` on the host. To build locally instead:
   ```bash
   docker build -t ghcr.io/sebbi08/deployment-gateway:0.1.0 --build-arg VERSION=0.1.0 .
   ```
2. Create the stack folder (any absolute path; `/srv/deploy-gateway` is used as the example) and copy the example files:
   ```bash
   sudo mkdir -p /srv/deploy-gateway/{data,workspaces}
   cp deploy/docker-compose.yml /srv/deploy-gateway/
   cp deploy/.env.example /srv/deploy-gateway/.env
   ```
   Then edit `/srv/deploy-gateway/.env`: `GATEWAY_VERSION`, `GATEWAY_DIR` (the stack folder — the workspaces path must be identical on host and in the container, the compose file derives both from it), `DOCKER_CONFIG_FILE`, `MCP_HOST`, `ADMIN_HOST`, `PROXY_NETWORK`, `TRAEFIK_ENTRYPOINT` and `TRUSTED_PROXIES` (the IP of your reverse-proxy container; without it every agent and every admin login attempt is audited and rate-limited under the proxy's address, and the gateway logs a startup warning). Pin the proxy's IP (e.g. `ipv4_address` on the proxy network) so it survives restarts.
3. Generate the admin password hash and paste it into `.env` (single quotes!):
   ```bash
   docker run --rm -i ghcr.io/sebbi08/deployment-gateway:0.1.0 hash-password
   ```
4. Registry access: create a dedicated credentials file for agent pulls, e.g. `DOCKER_CONFIG=/srv/deploy-gateway/registry docker login <registry>` (writes `registry/config.json` with inline `auths`), and set `DOCKER_CONFIG_FILE` to it. **Every workspace can pull, run and read any image these credentials can access**, so use least-privilege pull tokens that see only agent images (e.g. a bot account), never root's `~/.docker/config.json` or credentials for your own stacks' private images. Pulls also run from the host's network: agents can name registries on loopback or your LAN.
5. Start: `cd /srv/deploy-gateway && docker compose up -d && docker logs deploy-gateway`.

### Operator checklist (manual)

- Create a dedicated network for agent apps and attach your reverse proxy to it: `docker network create apps`, then add `apps` to the proxy's networks. Keep agent apps off any network your own stacks share.
- Exposed apps of all workspaces share the template networks (e.g. `apps`): they can reach each other directly on every listening port, without going through Traefik. Middlewares on a router (forward-auth, IP allowlists, trusted identity headers) do not protect an app against other workspaces; apps must authenticate requests themselves.
- Every port your reverse proxy listens on is reachable from exposed apps over `apps`, including its API/dashboard and entrypoints you only publish on `127.0.0.1` or the LAN. Do not run Traefik with `api.insecure`; serve `api@internal` through an authenticated router, keep metrics/ping on manual routing, and do not use `ipAllowList` ranges that cover the `apps` subnet. Internal-only routers belong on a proxy that is not attached to `apps`.
- DNS: a wildcard record for the apps domain (e.g. `*.apps.example.com`) pointing to the host, and a wildcard certificate via the DNS challenge (or per-host certificates).
- Every app gets its own Docker network. The daemon's built-in address pools allow only about 31 networks host-wide (shared with your own stacks), so configure `default-address-pools` in `/etc/docker/daemon.json` with a range unused on your LAN/VPN, e.g. `{"default-address-pools":[{"base":"10.201.0.0/16","size":24}]}` (256 networks), and keep the sum of all workspaces' `max_apps` below it. The gateway warns at startup when the built-in pools are in use.
- Mount the file system holding `WORKSPACES_ROOT` with `nosuid,nodev` if you can: containers that run as root can create setuid files in their data folders. Root-owned data folders are created with mode 0700, so other host users cannot reach such files, but `nosuid` makes them harmless everywhere.
- Published ports bypass `ufw`. Choose each workspace's bind IP (`0.0.0.0`, the LAN IP, or `127.0.0.1`) and port range deliberately.
- The startup mount check finds the gateway's own container by hostname, and startup fails if it runs in a container it cannot find. Either keep the default hostname or set `SELF_CONTAINER_ID` to the container name/ID (e.g. `SELF_CONTAINER_ID=deploy-gateway`, matching `container_name`).

## Configuration notes

Configuration is read from environment variables (see `deploy/.env.example`): `ADMIN_PASSWORD_HASH`, `ADMIN_TRUSTED_PROXIES` / `ADMIN_USER_HEADER`, `TRUSTED_PROXIES`, `DEPLOY_TIMEOUT`, `DEPLOY_CONCURRENCY`, `WORKSPACES_ROOT`, `DATA_DIR`, `FILE_OWNER`, `EXTRA_RUNTIMES` (hardened runtimes such as `runsc` allowed besides runc; nvidia runtimes are refused). `FILE_OWNER=uid:gid` (numeric, optional) gives group `gid` read access to the workspace and app folders, `data/` and the generated compose files (re-applied on every deploy); they stay owned by root, because the gateway runs those files as root and a host user who could edit them would effectively be root. `data/<service>` folders keep the owner the container runs as (`user`, otherwise root). `ADMIN_TRUSTED_PROXIES` and `TRUSTED_PROXIES` take comma-separated IPs/CIDRs of the reverse proxy (e.g. the IP of your Traefik container); catch-all prefixes `0.0.0.0/0` and `::/0` are rejected at startup because they would let any client spoof its IP or admin user header. Optional: `SELF_CONTAINER_ID` (name or ID of the gateway's own container, e.g. `deploy-gateway`; needed only when the container has a custom `hostname:`, because the startup mount check otherwise finds itself by hostname and fails closed if it cannot).

## Admin UI

Open the admin host (`https://$ADMIN_HOST`, e.g. `https://deploy-admin.example.com`). Optionally protect it with a forward-auth middleware (Authelia, oauth2-proxy, …) via the commented label in `deploy/docker-compose.yml`. In proxy mode (empty `ADMIN_PASSWORD_HASH`) that middleware is required and must set or overwrite `ADMIN_USER_HEADER`, and `ADMIN_TRUSTED_PROXIES` must be the proxy's pinned single IP; the gateway warns at startup if it contains a range.

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
2. **Workspaces** → create a workspace (name, quota, published port range, bind IP); on its page grant templates (Templates tab) and create a token (Tokens tab, shown once with a copy button). Stopping an app in the Apps tab is temporary: the agent can restart or redeploy it. To keep an app down, revoke the workspace's tokens or delete the app.

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
          "environment": {"DATABASE_URL": "postgres://postgres:change-me@alice-shop-db:5432/shop"}, "depends_on": ["db"]},
  "db":  {"image": "postgres:16.4", "user": "999:999", "volume": {"path": "/var/lib/postgresql/data"},
          "environment": {"POSTGRES_PASSWORD": "change-me", "POSTGRES_DB": "shop"},
          "writable_rootfs": true, "healthcheck": {"command": ["pg_isready"]}}
}}}
```

Use a real secret instead of `change-me`; environment values are redacted in the audit log.

## Trying it out

`examples/testapp` is a small test service plus a manual workflow (**Actions → Test app image**) that publishes it as a private GHCR package; its README walks through private-registry access, deploy, restart, persistence and negative checks.

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
