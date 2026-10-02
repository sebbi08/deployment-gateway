# testapp — end-to-end check for deploy-gateway

A tiny HTTP service that exercises what the gateway enforces:

| Feature | How the test app shows it |
|---|---|
| Non-root, read-only root fs, no capabilities | Runs as `65532:65532` on distroless; only writes to `/data` and `/tmp` |
| Persistent volume owned by the service user | `GET /` increments a counter stored in `/data/visits`; it survives `restart` and redeploys |
| Env values passed literally | `GREETING` is echoed back unchanged (try a value containing `${HOME}` or `$5`) |
| Healthcheck without shell/curl | `/testapp -healthcheck` probes `/healthz` |
| Private registry pull | The image is published to GHCR as a private package |

Endpoints: `GET /` (JSON with greeting, hostname, version, visit counter) and `GET /healthz`.

## 1. Publish the image (private)

GitHub → **Actions → Test app image → Run workflow** with a version such as `0.1.0`. This pushes `ghcr.io/sebbi08/deployment-gateway-testapp:<version>`. GHCR creates the package as **private** — leave it that way to test private pulls.

## 2. Give the gateway host access to the private package

On the gateway host:

```bash
# Classic personal access token with scope read:packages
echo "$GHCR_TOKEN" | docker login ghcr.io -u <github-user> --password-stdin
grep -q credsStore ~/.docker/config.json && echo "remove credsStore: the gateway needs inline auths"
docker restart deploy-gateway
```

The restart matters: `docker login` replaces `config.json` atomically, and a single-file bind mount keeps pointing at the old file until the container is recreated/restarted.

## 3. Deploy through the gateway

```bash
GATEWAY=https://mcp.example.com      # your agent endpoint
TOKEN=dgw_...                        # the workspace token (never commit it)
api() { curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' "$GATEWAY/api/v1/$1" -d "${2:-{\}}"; echo; }

api whoami
api list_templates                    # pick the template name for "expose" below
```

Stack (replace `<template>` and the version):

```bash
STACK='{"app": "testapp", "stack": {"services": {"web": {
  "image": "ghcr.io/sebbi08/deployment-gateway-testapp:0.1.0",
  "user": "65532:65532",
  "environment": {"GREETING": "hello ${HOME} costs $5"},
  "expose": {"template": "<template>", "port": 8080},
  "volume": {"path": "/data"},
  "healthcheck": {"command": ["/testapp", "-healthcheck"], "interval": "10s"},
  "resources": {"memory": "64m", "cpus": 0.25}
}}}}'

api validate "$STACK"                 # dry run: hostnames, URLs, totals
api deploy "$STACK"                   # pulls the private image, starts, waits for healthy
api list
curl -s "https://<url-from-deploy-output>/"   # visits: 1, 2, ...
api logs '{"app": "testapp", "service": "web", "tail": 50}'
api restart '{"app": "testapp"}'
curl -s "https://<url-from-deploy-output>/"   # counter continues → volume persisted
api delete '{"app": "testapp", "confirm": "testapp"}'
```

Without an exposure template you can also publish a port inside the workspace's range instead of `expose`, e.g. `"ports": [{"container": 8080, "host": 20080}]`, and open `http://<bind-ip>:20080/`.

## Negative checks worth trying

- `"image": "ghcr.io/sebbi08/deployment-gateway-testapp:latest"` → `validation_failed` (unpinned tag).
- Add `"privileged": true` to the service → rejected as an unknown field.
- `"volume": {"path": "/proc/x"}` → `validation_failed`.
- Deploy before step 2 (no registry credentials) → `deploy_failed` with the pull error.
- Use a token of another workspace and call `logs` for `testapp` → `not_found`.

Via MCP (e.g. Claude Code): `claude mcp add --transport http deploy "$GATEWAY/mcp" --header "Authorization: Bearer $TOKEN"`, then ask the agent to deploy the stack above.
