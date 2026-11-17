# Workspace secrets — Design

Date: 2026-11-17
Status: Draft for review
Extends: `2026-10-01-deploy-gateway-design.md` (referred to as "main spec", sections as §n)

## 1. Intent

Agents often deploy apps that need credentials (API keys, database passwords for external services, webhook secrets). Today the only way to pass them is `environment` in the stack, so the value has to travel through the agent: it ends up in the chat transcript, the LLM provider's logs and the agent's context.

This feature lets an agent **declare** that a service needs a secret, and lets the operator **enter and later change** the value in the admin UI. The value never travels to or through the agent.

### Threat scope (decided)

The goal is to keep secret values **out of the agent's context**: no gateway response, tool output, error message or audit entry an agent can see ever contains a value. It is explicitly **not** a goal to protect values against a malicious or prompt-injected agent: the agent controls image and command, so it can always deploy a container that prints its environment to `logs` or sends it out over the internet. Binding secrets to approved images was considered and rejected as out of scope.

## 2. Goals, non-goals, anti-goals

Goals:
- An agent references secrets by name per service; it never supplies or receives values.
- Secrets are stored per **workspace** and can be used by several apps of that workspace; changing a value once updates every app that uses it.
- The operator sets, replaces and deletes values in the admin UI; secrets an agent asked for but that have no value yet are visible there as "requested".
- Changing a value is applied to running services automatically, without the agent redeploying.
- Values never appear in compose files (`docker-compose.yml`, `.prev.yml`, `.next.yml`), the file explorer, the audit log or any agent-facing output.

Non-goals:
- Protection against a malicious agent (see §1).
- Encryption at rest. Values are stored in plaintext in SQLite under `DATA_DIR`; the env files Docker reads must be plaintext on disk anyway, and the container's environment is visible via `docker inspect` on the host.
- Multi-line values (e.g. PEM keys). Operators can base64-encode them; the app decodes.
- Secrets shared across workspaces.
- Agent-supplied descriptions/hints for requested secrets.
- Reading back a stored value in the UI.

Anti-goals (would make this a failure even if it works):
- Any path by which a value reaches an agent through the gateway (responses, errors, compose output, audit params, `whoami`, `list`).
- Any new way for agent input to influence container configuration beyond environment variables (no agent-chosen paths, no new compose keys from agents).
- Values readable by the `FILE_OWNER` group or shown in the admin file explorer.

## 3. Approaches considered

1. **Per-app secrets declared in the stack** — `secrets: [KEY]` per service, values entered per app. Simplest, but the same key used by three apps must be entered three times and changed three times.
2. **Workspace secret store (chosen)** — the operator manages named secrets per workspace; agents map env var → secret name. One value serves many apps; changing it updates all of them. Costs one more level of indirection (agents must know names; deleting must check usage).
3. **Values rendered into the compose file** — rejected: values would sit in every compose file and its backups and in the file explorer, and changing a value would require the agent's stack, which the gateway does not store.

The operator picked approach 2 because the same credential is often used by several apps.

## 4. Agent-facing changes

### 4.1 Stack format (§7)

New optional per-service field `secrets`: a map from environment variable name to secret name.

```json
"web": {
  "image": "ghcr.io/me/shop:1.4.2",
  "environment": { "STRIPE_MODE": "live" },
  "secrets": { "STRIPE_API_KEY": "stripe-prod", "SMTP_PASSWORD": "mail" }
}
```

| Rule | |
|---|---|
| Keys | same regex as `environment` keys (`^[A-Za-z_][A-Za-z0-9_]{0,63}$`) |
| Values (secret names) | `^[a-z][a-z0-9_-]{0,47}$` |
| Collision | a key must not also appear in the same service's `environment` |
| Count | `environment` + `secrets` entries together ≤ 100 per service |

Unknown-field rejection is unchanged; the jsonschema description must tell agents that values are never supplied here and that the operator sets them in the admin UI.

### 4.2 Missing or unset secrets

`validate` and `deploy` resolve every referenced name against the workspace's secrets:

- Name unknown → a **requested** secret is created (no value, `requested_at` = now, `requested_by` = app name), unless the workspace already holds 100 secrets (set + requested); then the reference fails with "too many secrets in this workspace".
- Name exists but has no value (requested) → reference fails.

Failures are returned as ordinary `validation_failed` field errors, all at once, e.g. `services.web.secrets.STRIPE_API_KEY: secret "stripe-prod" has no value yet; ask the operator to set it in the deploy-gateway admin UI (workspace "alice", tab Secrets)`. A deploy with unresolved secrets changes nothing else (same guarantee as any validation failure). Creating the requested row is the only side effect and happens on `validate` too, so an agent can register its needs before the first deploy.

### 4.3 Read tools

- `whoami` gains `secrets: [{name, state: "set" | "requested"}]` and a guidance line: never put credentials in `environment`; reference a secret name in `secrets` and ask the operator to set it.
- `validate`/`deploy` results list, per service, the secret env keys in use (names only).
- No tool ever returns a value. There is no agent tool to create, set or delete secrets.

### 4.4 Masking agent-facing output (best effort)

Every text the gateway returns to an agent that may contain container or compose output — `logs`, compose output and failure logs in `deploy` errors — has each secret value of the workspace that is at least 6 bytes long replaced by `***` before it is sanitized/truncated. This catches apps that print their configuration at startup; it is not a security boundary (encodings defeat it), consistent with §1. The audit log keeps full compose output only where it already does today and gets the same masking.

## 5. Storage (§15)

```
workspace_secrets(id, workspace_id FK ON DELETE CASCADE, name, value TEXT NULL,
                  created_at, updated_at, requested_at NULL, requested_by TEXT NOT NULL DEFAULT '',
                  UNIQUE(workspace_id, name))
app_secret_refs(app_id FK ON DELETE CASCADE, service, env_key, secret_id FK ON DELETE RESTRICT,
                stale INTEGER NOT NULL DEFAULT 0,
                PRIMARY KEY(app_id, service, env_key))
apps: + secret_apply_state TEXT NOT NULL DEFAULT '', secret_apply_at INTEGER NULL, secret_apply_error TEXT NOT NULL DEFAULT ''
```

- `value IS NULL` ⇔ requested. Once set, a value can be replaced but not unset.
- `app_secret_refs` is the source of truth for "which apps use which secret". It is replaced for an app in the same transaction that commits a deploy (the existing reservation/port-claim commit, §11), so it always describes the current `docker-compose.yml`. A failed deploy before commit leaves it untouched; app delete removes it via cascade.
- `stale = 1` means the env file holds a newer value than the running container (see §7).
- Schema changes follow the existing `schema.sql` (`CREATE TABLE IF NOT EXISTS`) approach; the new `apps` columns need an idempotent `ALTER TABLE ... ADD COLUMN` migration for existing databases.

## 6. Files and rendering (§6, §9)

### 6.1 Env files

```
<WORKSPACES_ROOT>/<ws>/<app>/
├── docker-compose.yml
└── secrets/                 # root:root 0700, created by the gateway, never mounted into containers
    └── <svc>.env            # root:root 0600; one line KEY=value per secret of that service
```

- `secrets/` and its files are excluded from `FILE_OWNER` group ownership.
- Values are single-line: ≤ 8 KB, no NUL, CR or LF (enforced when the operator saves a value).
- File content is generated by one function shared by deploy and apply (§7) from `(env_key → value)`, sorted by key.

### 6.2 Compose rendering

A service with secrets gets exactly one env file reference in long syntax:

```yaml
env_file:
  - path: <WORKSPACES_ROOT>/<ws>/<app>/secrets/<svc>.env
    required: true
    format: raw
```

`format: raw` disables interpolation and quote processing so `$`, quotes and `#` in values are taken literally. The path is computed by the gateway from validated names; agents never influence it. Values never appear in the YAML.

The builder must confirm with the pinned compose binary (integration test using `docker compose config` and a real container) that: raw values round-trip byte-exactly (including `$`, quotes, `#`, `=`, leading/trailing spaces); and a change of env file content alone makes `up` recreate the service (compose resolves `env_file` into the service config hash). If the latter does not hold, add a gateway-owned revision mechanism and update the verifier accordingly.

### 6.3 Deploy flow (§11)

- **Before pull:** `docker-compose.next.yml` may reference env files that do not exist yet (new service). Create missing ones **empty** so compose can load the file; never modify existing ones before commit, so a deploy that fails before commit leaves the running deployment's env files untouched.
- **At commit:** in the commit transaction, re-read the referenced values (they may have changed since validation) and replace `app_secret_refs` for the app. If a referenced secret was deleted meanwhile (cannot happen while refs exist, but a first-time reference can race), fail like a validation error.
- **At rotate:** write the env files of all services with secrets from the committed values (write to a temp file in `secrets/`, fsync, rename), clear `stale` for the app, and remove env files of services that no longer have secrets.
- Everything after (up, runtime check, health) is unchanged.

## 7. Applying a changed value

When the operator saves a new value for a secret that has refs:

1. The value is stored and `stale = 1` is set on all its refs (one transaction). The admin request returns immediately.
2. A background job processes affected apps **one at a time**. Per app it waits for the app lock (bounded by `DEPLOY_TIMEOUT`; on timeout the app stays stale) and then:
   - **Skip** the app (refs stay stale, reason "applied on the next successful deploy") if its `last_deploy_result` is not `ok`. After a failed, interrupted or rejected deploy the current `docker-compose.yml` is not what runs, and an apply must never finish a failed deploy on the agent's behalf.
   - **Re-verify** the current `docker-compose.yml` with the verifier against the workspace's *current* policy (granted templates, quotas, port range). If it fails, skip (refs stay stale, reason "configuration no longer allowed by current policy; the agent must redeploy"). A recreate must not produce a container that current policy would reject.
   - Rewrite the env files of the app's services that use the secret (from `app_secret_refs` + current values).
   - Select those services whose container state is `running` or `restarting` (a crash loop, often caused by the very credential being fixed). `exited`, `created`, `paused` or missing containers are not started.
   - Mark the app `secret_apply_state = 'running'` (see below), then run `up -d --pull never --no-deps --wait --wait-timeout <DEPLOY_TIMEOUT> <service…>` on the current `docker-compose.yml` for exactly the selected services (new runner method; fixed argv, same global semaphore/workspace queue).
   - Run the runtime check (layer 3) and the running/healthy check for those services, with the same failure handling as a deploy (security violation → remove containers, `critical` audit). The plan facts these checks need (labels, ports, healthcheck presence) are derived from the current compose file and the app's port claims, because the stack is not stored.
   - On success clear `stale` for the recreated services' refs. Record the outcome in the app's own **apply status** (`secret_apply_state`, `secret_apply_at`, `secret_apply_error`), **not** in `last_deploy_*`, so `.prev.yml` rotation and deploy recovery are unaffected. Audit `secret_apply` (actor `system`, params: secret name, app, services, skip reason — never the value).
3. Services that are not selected keep stale refs; the next deploy recreates them with the new value (compose detects the changed env file, §6.2). Note: the agent's `restart` tool restarts the existing container and does **not** pick up the new value; the UI says so.
4. Crash safety: at startup, apps with `secret_apply_state = 'running'` get the same plan-independent runtime checks as interrupted deploys (`recover.go`), violating containers are removed, and the state becomes `interrupted` (refs stay stale).
5. Apply jobs are not persisted otherwise. After a gateway restart, stale refs remain visible and the operator can re-run the apply (§8 endpoint). No automatic retry.
6. Agent visibility: `list` shows per app the env keys whose secret has a newer value not yet applied (`stale_secrets`, keys only) and, if the last apply failed, its sanitized and masked error. This tells an agent that a failure stems from a secret change, not from its own code.

A newer value saved while a job for the same secret is still running queues another pass; jobs for one workspace never run concurrently with each other.

## 8. Admin API and UI (§14)

### 8.1 API

| Method & path | Purpose |
|---|---|
| `GET workspaces/{name}/secrets` | list: name, state (set/requested), created/updated/requested time, requested_by, usage `[{app, service, env_key, stale}]`. Never returns values |
| `POST workspaces/{name}/secrets` | `{name, value}`: create a set secret (operator may pre-create) |
| `PUT workspaces/{name}/secrets/{secret}` | `{value}`: set a requested secret or replace a value; triggers apply (§7) if refs exist |
| `POST workspaces/{name}/secrets/{secret}/apply` | re-run the apply for stale refs |
| `DELETE workspaces/{name}/secrets/{secret}` | delete; refused with `conflict` while refs exist (lists the apps) |

- Value validation per §6.1; name per §4.1.
- All mutations audited with `actor_type=admin`; params contain the secret name only. The generic `redact` must also mask a `value` field defensively, with a test.
- Workspace detail (`GET workspaces/{name}`) and the dashboard get the count of requested secrets.

### 8.2 UI

New tab **Secrets** on the workspace detail page, plus a dashboard banner "N requested secrets" linking to the workspace (like the orphan banner).

```
Secrets                                                     [+ New secret]
┌──────────────┬────────────┬──────────────────────────────┬──────────────┐
│ Name         │ Status     │ Used by                      │              │
├──────────────┼────────────┼──────────────────────────────┼──────────────┤
│ stripe-prod  │ set        │ shop/web STRIPE_API_KEY      │ Replace  Del │
│              │ updated 2h │ admin/web STRIPE_KEY ⚠ stale │ Re-apply     │
│ mail         │ requested  │ — (asked by shop, 5 min ago) │ Set     Del  │
└──────────────┴────────────┴──────────────────────────────┴──────────────┘
```

- Set/Replace opens a modal with a password-type input (no reveal, no prefill); on save a notification says which apps will be updated.
- "stale" shows a tooltip: not yet applied (stopped service, failed apply or gateway restart); for stopped services it says the value takes effect on the next deploy, not on `restart`.
- Delete is disabled while in use, with the list of apps.

### 8.3 File explorer

The explorer lists `secrets/` and its entries, but refuses to return the content of any file below an app's `secrets/` directory (`forbidden`-style error, UI shows "secret file — not shown").

## 9. Check layers (§10)

- **Input allowlist:** §4.1 rules.
- **Verifier:** `env_file` joins the allowed service keys. It must be a list with exactly one entry `{path, required: true, format: raw}`, where `path` equals `<WORKSPACES_ROOT>/<ws>/<app>/secrets/<svc>.env` for that service's own short name (lexical equality plus `EvalSymlinks` of the existing parent staying inside the app folder, like volumes). Short syntax, other keys, other paths → violation. Unit tests with hand-written malicious compose files.
- **Runtime inspect:** unchanged (environment content is not security-relevant).

## 10. Workspace and app lifecycle

- App delete: folder removal deletes `secrets/`; refs cascade. Secrets themselves stay in the workspace.
- Workspace delete: secrets cascade.
- Requested secrets can be deleted at any time; an agent re-requests by validating/deploying again.

## 11. Testing

- Stack validation: key/name regexes, environment collision, combined count, unknown/requested names producing field errors and requested rows, workspace cap.
- Render golden file with `env_file`; verifier corpus for bad `env_file` shapes/paths.
- Service: deploy writes env files only at rotate; failure before commit leaves existing env files byte-identical; refs replaced on commit; apply recreates only running affected services, leaves stopped ones stale, honours the app lock; masking in `logs` and deploy errors; no value in any audit row (scan all audit params in tests for the test value).
- Admin API: CRUD, value validation, delete refused while in use, apply endpoint, values never in responses.
- Integration (real Docker): raw-format round trip and recreate-on-change (§6.2); end-to-end set → deploy → change value → container sees the new value.
- UI: Vitest for the Secrets tab (set modal is write-only, delete disabled while in use, stale indicator), dashboard banner.

## 12. Documentation

- Amend the main spec (§6, §7, §9, §10, §11, §12, §14, §15) to reference this feature.
- Install page, `whoami` guidance and the MCP tool descriptions explain the `secrets` field and that values are set by the operator.

## 13. Calls made by Claude (operator may override)

- Apply on change is automatic and runs in the background, one app at a time; stopped services are not started.
- Unknown names create a "requested" secret; cap of 100 secrets per workspace.
- Values are write-only in the UI; plaintext at rest.
- Secret names `^[a-z][a-z0-9_-]{0,47}$`; values single-line ≤ 8 KB.
- Missing secrets are reported as `validation_failed`, not a new error code.
- Best-effort masking of values in agent-facing logs/output.
- Apply status is stored separately from `last_deploy_*` so deploy rotation and recovery semantics stay unchanged.
- Apply skips apps whose last deploy is not `ok`, and apps whose current compose file fails re-verification against current policy; both stay stale with a visible reason.
- Apply recreates services in state `running` or `restarting`; everything else waits for the next deploy.
- `list` shows agents which secret keys are stale and the last apply error (keys only, masked).

## 14. Left to the builder

Exact Go types and package placement (suggestion: name rules in `names`, env file content in `render`, store queries in `store/secrets.go`, apply job in `service`), how plan facts for the apply checks are derived from the compose file, UI component structure, and the revision fallback in §6.2 if compose does not recreate on env file changes.
