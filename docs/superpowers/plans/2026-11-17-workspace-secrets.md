# Workspace Secrets Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Agents reference workspace secrets by name per service; the operator sets and changes values in the admin UI; values reach containers through gateway-written env files and never reach agents.

**Architecture:** New store tables (`workspace_secrets`, `app_secret_refs`) plus apply-status columns on `apps`. The stack gains `services.<svc>.secrets` (env key → secret name). Render emits one `env_file` entry (`format: raw`) per service with secrets; values are written to `<app>/secrets/<svc>.env` (0600) at the deploy's rotate step. A background job in `service` re-applies changed values to running services. Admin API + React tab manage secrets.

**Tech Stack:** Go 1.27, SQLite (`modernc.org/sqlite`), compose v5 binary, React + Mantine + TanStack Query, Vitest.

**Spec:** `docs/superpowers/specs/2026-11-17-workspace-secrets-design.md` (extends `docs/superpowers/specs/2026-10-01-deploy-gateway-design.md`). Read both.

## Global Constraints

- Secret values never appear in: any agent-facing response or error, audit `params_json`, compose files, file explorer content, admin API responses.
- Secret name regex: `^[a-z][a-z0-9_-]{0,47}$`. Env key regex: `^[A-Za-z_][A-Za-z0-9_]{0,63}$` (existing `envKeyRe`).
- Secret value: 1..8192 bytes, no NUL, CR or LF.
- Max 100 secrets (set + requested) per workspace (`store.MaxSecretsPerWorkspace = 100`).
- `environment` + `secrets` entries together ≤ 100 per service; a key may not appear in both.
- Env file: `<WORKSPACES_ROOT>/<ws>/<app>/secrets/<svc>.env`, dir `secrets/` mode 0700, files 0600, owner root:root, never chowned to `FILE_OWNER`, never mounted into containers. Content: `KEY=value\n` lines sorted by key.
- Rendered compose entry, exactly: `env_file: [{path: <abs env file>, required: true, format: raw}]`.
- Masking: secret values of ≥ 6 bytes are replaced by `***` in agent-facing text, before truncation.
- Apply recreates only containers in state `running` or `restarting`; skips apps whose `last_deploy_result != "ok"` or whose current compose file fails `verify.Verify` against current policy.
- All admin secret mutations hold the workspace mutex (`s.wsMutex(ws.Name)`).
- Commits end with the attribution lines from the session's system reminder.

## Review Focus

1. **A value that contains another secret's value or is a substring of logs** — masking must replace longer values first so `abcdef123` is not left as `***123`. Test in Task 6.
2. **Operator replaces a value while that app's deploy is in progress** — apply must wait for the app lock and run after the deploy, not be lost. Test in Task 7.
3. **Agent redeploys the same stack after a stopped service went stale** — the deploy must rewrite env files and clear `stale`. Test in Task 6.
4. **Workspace deleted while an apply job is queued** — the job must exit cleanly, not recreate or error loudly. Test in Task 7.
5. **Requested secret deleted by the operator, agent validates again** — it is re-requested, not an internal error. Test in Task 6.

---

### Task 1: Store — secrets tables, refs and apply status

**Files:**
- Modify: `internal/store/schema.sql`
- Modify: `internal/store/store.go` (call `migrate` after schema in `Open`)
- Modify: `internal/store/apps.go` (`App` fields, `scanApp`, `SaveDeployment` signature)
- Create: `internal/store/secrets.go`
- Test: `internal/store/secrets_test.go`, update callers in `internal/store/apps_test.go`

**Interfaces:**
- Produces:
  ```go
  const MaxSecretsPerWorkspace = 100
  type Secret struct {
      ID, WorkspaceID int64; Name string; Set bool
      CreatedAt, UpdatedAt time.Time; RequestedAt *time.Time; RequestedBy string
  }
  type SecretRef struct { // one env var of one service bound to a secret
      AppID int64; App, Service, EnvKey, Secret string; Stale bool
  }
  type SecretRefInput struct{ Service, EnvKey, Secret string }
  // App gains: SecretApplyState string; SecretApplyAt *time.Time; SecretApplyError string
  func (s *Store) ListSecrets(ctx, wsID int64) ([]Secret, error)                 // sorted by name, never values
  func (s *Store) SecretValues(ctx, wsID int64) (map[string]string, error)       // name -> value, set secrets only
  func (s *Store) RequestSecrets(ctx, wsID int64, names []string, app string) error // inserts missing names as requested, stops silently at MaxSecretsPerWorkspace
  func (s *Store) CreateSecret(ctx, wsID int64, name, value string) error        // ErrExists if name exists (set or requested)
  func (s *Store) SetSecretValue(ctx, wsID int64, name, value string) ([]SecretRef, error) // ErrNotFound; sets value, updated_at, requested_at=NULL; marks its refs stale=1; returns those refs
  func (s *Store) DeleteSecret(ctx, wsID int64, name string) error              // ErrNotFound; ErrInUse while refs exist
  func (s *Store) ListSecretRefs(ctx, wsID int64) ([]SecretRef, error)          // sorted app, service, env_key
  func (s *Store) AppSecretRefs(ctx, appID int64) ([]SecretRef, error)
  func (s *Store) ClearStale(ctx, appID int64, services []string) error
  func (s *Store) SetSecretApply(ctx, appID int64, state, errMsg string) error  // also sets secret_apply_at=now
  func (s *Store) AppsWithApplyState(ctx, state string) ([]AppRef, error)
  func (s *Store) SaveDeployment(ctx, wsID int64, name string, mem int64, cpus float64,
      hosts map[string]string, claims []PortClaim, refs []SecretRefInput) (App, error)
  ```
  `SaveDeployment` replaces the app's refs (all with `stale=0`) in its existing transaction; a ref to a missing or unset secret returns `ErrNotFound` and rolls back.

- [ ] **Step 1: Write failing tests** in `secrets_test.go`:
  - `TestRequestAndSetSecret`: `RequestSecrets(ws, ["stripe-prod"], "shop")` → `ListSecrets` has one entry, `Set=false`, `RequestedBy=="shop"`, `RequestedAt!=nil`; `SecretValues` empty. `SetSecretValue("stripe-prod","sk_live_x")` → `Set=true`, `RequestedAt==nil`; `SecretValues["stripe-prod"]=="sk_live_x"`.
  - `TestRequestSecretsCap`: create 100 secrets, `RequestSecrets(["extra"])` returns nil error and `ListSecrets` still has 100.
  - `TestSaveDeploymentRefs`: with secret set, `SaveDeployment(..., []SecretRefInput{{"web","STRIPE_API_KEY","stripe-prod"}})` → `AppSecretRefs` has it, `Stale=false`; `SetSecretValue` returns that ref and marks it `Stale=true`; second `SaveDeployment` with same refs resets `Stale=false`; `SaveDeployment` with a ref to a requested secret returns `ErrNotFound` and leaves existing refs unchanged.
  - `TestDeleteSecretInUse`: `DeleteSecret` returns `ErrInUse` while a ref exists; after `DeleteApp` it succeeds.
  - `TestSecretApplyState`: `SetSecretApply(app, "running", "")` → `AppsWithApplyState("running")` returns the app; `GetApp` shows the fields.
  - `TestMigrateAddsApplyColumns`: create a DB with the old `apps` table (no apply columns, via raw SQL), `Open` it, `SetSecretApply` works.
- [ ] **Step 2: Run** `go test ./internal/store/ -run 'Secret|Migrate'` — expect compile failure.
- [ ] **Step 3: Implement.** Tables exactly as spec §5 (`value TEXT NULL`, `UNIQUE(workspace_id,name)`, refs `PRIMARY KEY(app_id, service, env_key)`, `secret_id ... ON DELETE RESTRICT`, `app_id ... ON DELETE CASCADE`). `migrate(db)` reads `PRAGMA table_info(apps)` and adds each missing column (`secret_apply_state TEXT NOT NULL DEFAULT ''`, `secret_apply_at INTEGER`, `secret_apply_error TEXT NOT NULL DEFAULT ''`). Update all `SaveDeployment` callers in the repo to pass `nil` for now.
- [ ] **Step 4: Run** `go test ./internal/store/ && go build ./...` — PASS.
- [ ] **Step 5: Commit** `feat(store): workspace secrets, secret refs and apply status`.

### Task 2: Stack — `secrets` field and validation

**Files:**
- Modify: `internal/names/names.go` (add `ValidSecret`)
- Modify: `internal/stack/types.go`, `internal/stack/validate.go`
- Test: `internal/stack/validate_test.go`, `internal/names/names_test.go`

**Interfaces:**
- Produces:
  ```go
  func names.ValidSecret(s string) bool // ^[a-z][a-z0-9_-]{0,47}$
  // stack.Service gains:
  Secrets map[string]string `json:"secrets,omitempty" jsonschema:"environment variables whose values are workspace secrets: env var name -> secret name. Never put credentials in environment; the operator sets secret values in the admin UI and you never see them"`
  func stack.SecretNames(s *Stack) []string // sorted, deduplicated secret names over all services
  ```

- [ ] **Step 1: Write failing table cases** in `validate_test.go` (`TestValidateSecrets`): valid `{"STRIPE_API_KEY":"stripe-prod"}` → no errors; key `"1BAD"` → field `services.web.secrets.1BAD`; name `"Stripe"` → message containing `^[a-z][a-z0-9_-]{0,47}$`; same key in `environment` and `secrets` → field `services.web.secrets.FOO`, message `also set in environment`; 60 env + 41 secrets → field `services.web`, message `at most 100 environment variables and secrets`. `TestSecretNames`: two services sharing a name → one entry, sorted.
- [ ] **Step 2: Run** `go test ./internal/stack/ ./internal/names/` — FAIL.
- [ ] **Step 3: Implement** `validateSecrets(errs *Errors, f string, svc Service)` in `validate.go`, called next to `validateEnv`.
- [ ] **Step 4: Run** the same command — PASS. Also `go test ./internal/render/ -run Fuzz` (stack decoder change must not break the fuzz seed corpus).
- [ ] **Step 5: Commit** `feat(stack): services.<svc>.secrets maps env vars to workspace secrets`.

### Task 3: Render — `env_file` entry and env file content

**Files:**
- Modify: `internal/render/render.go`, `internal/render/compose.go`
- Create: `internal/render/envfile.go`
- Test: `internal/render/render_test.go`, `internal/render/envfile_test.go`, golden `internal/render/testdata/basic.golden.yml` (or a new `secrets.golden.yml`)

**Interfaces:**
- Consumes: `stack.Service.Secrets` (Task 2).
- Produces:
  ```go
  // render.Input gains: SecretsDir string // <root>/<ws>/<app>/secrets
  func render.EnvFilePath(secretsDir, svc string) string // filepath.Join(secretsDir, svc+".env")
  func render.EnvFile(values map[string]string) []byte  // "K=v\n" sorted by key; nil map -> empty slice
  ```
  `composeService` gains `EnvFile []composeEnvFile \`yaml:"env_file,omitempty"\`` with `composeEnvFile{Path string; Required bool; Format string}` (yaml keys `path`, `required`, `format`).

- [ ] **Step 1: Write failing tests:** `TestRenderEnvFile` — a stack with `web.secrets` renders `env_file` with exactly one entry `{path: <SecretsDir>/web.env, required: true, format: raw}` and no secret key in `environment`; a service without secrets has no `env_file`. `TestEnvFileContent` — `EnvFile({"B":"x y","A":"p$$\"#"})` equals `"A=p$$\"#\nB=x y\n"`. Golden file covers a service with secrets (run with `-update` once, review the diff).
- [ ] **Step 2: Run** `go test ./internal/render/` — FAIL.
- [ ] **Step 3: Implement.** The path is computed only from `in.SecretsDir` and the validated service name.
- [ ] **Step 4: Run** `go test ./internal/render/ -run 'Render|EnvFile|Golden'` — PASS. (The fuzz target needs Task 4's verifier rule; it runs there.)
- [ ] **Step 5: Commit** `feat(render): env_file entry for services with secrets`.

### Task 4: Verifier — allow only the gateway's own `env_file`

**Files:**
- Modify: `internal/verify/verify.go`
- Test: `internal/verify/verify_test.go`
- Modify: `internal/service/plan.go` (pass `SecretsDir` into `render.Input` and `verify.Policy`; add `func (s *Service) secretsDir(ws, app string) string` in `service.go`)

**Interfaces:**
- Produces: `verify.Policy` gains `SecretsDir string`. `env_file` added to `allowedServiceKeys`; new `checkEnvFile(key, svc string, raw any, p Policy, v *Violations)`.

- [ ] **Step 1: Write failing tests** (hand-written YAML, independent of render), each must produce a violation: short syntax `env_file: x.env`; two entries; path of another service (`.../secrets/db.env` on `web`); path outside `SecretsDir`; `format` missing or not `raw`; `required: false`; extra key `foo`; path whose parent `secrets` is a symlink (create in `t.TempDir()`). One passing case: exact entry.
- [ ] **Step 2: Run** `go test ./internal/verify/` — FAIL.
- [ ] **Step 3: Implement** using `filepath.Join(p.SecretsDir, svc+".env")` equality and the existing `checkNoSymlink`.
- [ ] **Step 4: Run** `go test ./internal/verify/ ./internal/render/ ./internal/service/` and `go test ./internal/render/ -run=^$ -fuzz=Fuzz -fuzztime=30s` — PASS.
- [ ] **Step 5: Commit** `feat(verify): accept only the gateway-rendered env_file`.

### Task 5: Runner — recreate selected services

**Files:**
- Modify: `internal/runner/runner.go`, `internal/service/service.go` (`Compose` interface), `internal/service/servicetest/fakes.go`
- Test: `internal/runner/runner_test.go`

**Interfaces:**
- Produces:
  ```go
  // Recreate runs: up -d --pull never --no-deps --wait --wait-timeout <secs> <services...>
  func (r *Runner) Recreate(ctx context.Context, file, project string, services []string) (string, error)
  ```
  Added to `service.Compose`. `FakeCompose.Recreate` records `"recreate <project> <svc1,svc2>"`, honours `UpErr`, and calls a new hook `OnRecreate func(file, project string, services []string) error` (the fake must recreate only those containers, re-reading env from the env files is not needed — tests assert on calls and container IDs).

- [ ] **Step 1: Write failing test** `TestRecreateArgv` using the existing fake-binary pattern in `runner_test.go`: argv after `-f file -p project` equals `up -d --pull never --no-deps --wait --wait-timeout 300 a-b-web a-b-worker` (compose service keys `<ws>-<app>-<svc>`); empty `services` returns an error without running compose.
- [ ] **Step 2: Run** `go test ./internal/runner/` — FAIL.
- [ ] **Step 3: Implement**; same `run` path (semaphore, workspace queue, scrubbed env).
- [ ] **Step 4: Run** `go test ./internal/runner/ ./internal/service/...` — PASS.
- [ ] **Step 5: Commit** `feat(runner): recreate selected services without pulling`.

### Task 6: Service — deploy with secrets, env files, masking

**Files:**
- Create: `internal/service/secrets.go` (env file writing, masking, secret resolution helpers)
- Modify: `internal/service/plan.go`, `internal/service/deploy.go`, `internal/service/read.go`, `internal/service/lifecycle.go`, `internal/service/types.go`
- Test: `internal/service/secrets_test.go`

**Interfaces:**
- Consumes: Tasks 1–5.
- Produces (unexported unless noted):
  ```go
  func (s *Service) requestSecrets(ctx context.Context, ws store.Workspace, app string, st *stack.Stack) error // calls Store.RequestSecrets for stack.SecretNames; run before plan in Validate and stage
  // plan: after stack.Validate, loads Store.ListSecrets + SecretValues; per reference adds field errors:
  //   requested -> `secret %q has no value yet; ask the operator to set it in the deploy-gateway admin UI (workspace %q, tab Secrets)`
  //   missing   -> `secret %q does not exist and the workspace already holds 100 secrets; ask the operator to remove unused ones`
  // planned gains: secretValues map[string]map[string]string // svc -> env key -> value
  //                refs []store.SecretRefInput
  func (s *Service) writeEnvFiles(ws, app string, pl *planned) error  // writes secrets/<svc>.env via temp+fsync+rename 0600; removes .env files of services without secrets
  func (s *Service) ensureEnvFiles(ws, app string, st *stack.Stack) error // mkdir secrets/ 0700; create missing <svc>.env empty (0600); never touches existing files
  func (s *Service) masker(ctx context.Context, wsID int64) func(string) string // replaces values >= 6 bytes with ***, longest first
  // ServicePlan and ServiceStatus gain: SecretEnv []string `json:"secret_env,omitempty"` (sorted env keys)
  ```
  Flow changes: `Validate` and `stage` call `requestSecrets` before planning (outside `plan`, which stays read-only). `prepareDirs` calls `ensureEnvFiles`. `prepare` passes `pl.refs` to `SaveDeployment` and calls `writeEnvFiles` after `RecordDeploy(..., "pending")` and before renaming next → current. Every agent-facing `sanitize(...)` input and every `Error.detail` built from compose output, plus `Logs` output and `failureLogs`, goes through the masker first.

- [ ] **Step 1: Write failing tests** (use the existing harness and fakes):
  - `TestValidateRequestsUnknownSecret`: validate with `secrets: {"K":"stripe-prod"}` → `validation_failed`, field `services.web.secrets.K`, message contains `has no value yet`; `ListSecrets` shows `stripe-prod` requested by the app; no app record.
  - `TestDeployWritesEnvFile`: secret set to `"sk_live_123456"` → deploy ok; `secrets/web.env` is `K=sk_live_123456\n` with mode 0600 and dir 0700; `docker-compose.yml` does not contain `sk_live_123456`; `AppSecretRefs` has the ref with `Stale=false`.
  - `TestDeployFailureBeforeCommitKeepsEnvFile`: deploy ok, change value in DB with `Store.SetSecretValue`, next deploy fails at pull (`PullErr`) → `web.env` byte-identical to before.
  - `TestRedeployClearsStale` (Review Focus 3): mark ref stale via `SetSecretValue`, redeploy same stack → env file has new value, `Stale=false`.
  - `TestDeployRemovesEnvFileOfServiceWithoutSecrets`.
  - `TestMaskingInLogsAndErrors` (Review Focus 1): secrets `a="abcdef123"`, `b="abcdef"`; fake logs `"x abcdef123 y abcdef z"` → `Logs` returns `"x *** y *** z"`; `UpErr` with `UpOut` containing the value → error message contains `***` and not the value; audit rows (`Store.ListAudit`) contain no value.
  - `TestRerequestAfterDelete` (Review Focus 5): requested secret deleted via `Store.DeleteSecret`, validate again → requested again, `validation_failed`.
  - `TestEnvFilesNotOwnedByFileOwner`: with `FileOwner` set, `Chown` is never called on `secrets/` or its files (record calls).
  - `TestSecretValueNeverInAgentResults`: JSON-marshal the results of `validate`, `deploy`, `list`, `whoami` and assert none contains the value.
- [ ] **Step 2: Run** `go test ./internal/service/ -run 'Secret|EnvFile|Masking|Stale|Rerequest'` — FAIL.
- [ ] **Step 3: Implement** as listed under Interfaces.
- [ ] **Step 4: Run** `go test ./internal/service/...` — PASS (existing tests unchanged).
- [ ] **Step 5: Commit** `feat(service): deploy apps with workspace secrets via env files`.

### Task 7: Service — apply changed values, recovery, agent visibility

**Files:**
- Create: `internal/service/secretapply.go`
- Modify: `internal/service/recover.go`, `internal/service/read.go`, `internal/service/types.go`, `internal/service/service.go` (job queue field, `Close`/shutdown hook if the service has one; otherwise a `context` passed to `StartSecretApplier`)
- Modify: `cmd/deploy-gateway/main.go` (start the applier)
- Test: `internal/service/secretapply_test.go`

**Interfaces:**
- Consumes: `Store.SetSecretValue` refs, `Store.ClearStale`, `Store.SetSecretApply`, `Compose.Recreate`, `verify.Verify`.
- Produces:
  ```go
  func (s *Service) StartSecretApplier(ctx context.Context) // starts one worker goroutine per workspace on demand; stops with ctx
  func (s *Service) enqueueApply(ws string, appIDs []int64)  // dedupes; a pass already running for an app re-runs once afterwards
  func (s *Service) applySecrets(ctx context.Context, ws store.Workspace, app store.App) error
  // AppView gains: StaleSecrets []string `json:"stale_secrets,omitempty"` ("<svc>.<ENV_KEY>")
  //                SecretApplyError string `json:"secret_apply_error,omitempty"` (masked, sanitized)
  ```
  `applySecrets` steps, in order (spec §7): reload workspace (gone → return nil); wait for `Compose.TryLock(lockKey)` polling every 500 ms up to `DEPLOY_TIMEOUT` (timeout → `SetSecretApply(app,"skipped","app busy")`); skip with `SetSecretApply(app,"skipped", reason)` if `last_deploy_result != "ok"` (reason `applied on the next successful deploy`) or `verify.Verify(current compose, policy rebuilt from current workspace + grants)` fails (reason `configuration no longer allowed by current policy; the agent must redeploy`); rewrite env files of affected services from `AppSecretRefs` + `SecretValues`; select services whose container state is `running` or `restarting`; none → done (refs stay stale); `SetSecretApply(app,"running","")`; `Compose.Recreate`; runtime check (labels from the compose file's `labels`, ports from `ListPortClaims`) and running/healthy check (healthcheck presence from the compose file); violation → same removal path as deploy + `critical` audit; success → `ClearStale(app, services)` + `SetSecretApply(app,"ok","")`; failure → `SetSecretApply(app,"failed", masked msg)`. Audit `secret_apply` with actor_type `system`, params `{secret?, app, services, skipped?}`.
  The policy/plan facts helper: `func (s *Service) currentPlanFacts(ctx, ws store.Workspace, app string) (*planned, error)` parsing `docker-compose.yml` (labels per service, healthcheck presence) — `pl.stack` is nil here, so the health check must take the healthcheck set as a parameter rather than reading `pl.stack`.
  `RecoverInterrupted` additionally handles `AppsWithApplyState("running")`: same `preExistingViolations` handling, then `SetSecretApply(app,"interrupted","gateway stopped during apply")`.

- [ ] **Step 1: Write failing tests:**
  - `TestApplyRecreatesRunningServices`: two services use the secret, `db` stopped (fake state `exited`), `web` running; `SetSecretValue` + `enqueueApply` → fake records exactly `recreate dg-alice-shop alice-shop-web`; `web` ref not stale, `db` ref stale; app `SecretApplyState=="ok"`; `last_deploy_result` unchanged.
  - `TestApplyRecreatesRestarting`: state `restarting` is recreated.
  - `TestApplySkipsFailedDeploy`: `last_deploy_result="failed"` → no recreate, state `skipped`, error text `applied on the next successful deploy`.
  - `TestApplySkipsWhenPolicyChanged`: revoke the template grant after deploy → no recreate, state `skipped`, reason mentions current policy.
  - `TestApplyWaitsForAppLock` (Review Focus 2): hold `TryLock` for 1 s in the test, apply completes after release with `recreate` recorded.
  - `TestApplyWorkspaceDeleted` (Review Focus 4): delete workspace before the job runs → no compose calls, no error log at error level.
  - `TestApplyRuntimeViolation`: fake `OnRecreate` creates a privileged container → containers removed, critical audit row with action `secret_apply`.
  - `TestRecoverInterruptedApply`: `SetSecretApply(app,"running","")` then `RecoverInterrupted` → state `interrupted`.
  - `TestListShowsStaleSecrets`: `List` returns `StaleSecrets: ["db.K"]` and `SecretApplyError` without the value.
- [ ] **Step 2: Run** `go test ./internal/service/ -run 'Apply|Recover|Stale'` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./internal/service/...` — PASS.
- [ ] **Step 5: Commit** `feat(service): apply changed secret values to running services`.

### Task 8: Admin service methods and API

**Files:**
- Modify: `internal/service/admin.go` (secret methods), `internal/service/track.go` (`redact` masks top-level `value`)
- Create: `internal/admin/secrets.go`
- Modify: `internal/admin/server.go` (register `secretRoutes`), `internal/admin/workspaces.go` (dashboard + detail counts), `internal/admin/dto.go`, `internal/admin/files.go`
- Test: `internal/admin/secrets_test.go`, `internal/service/admin_test.go`, `internal/admin/files_test.go`, `internal/service/audit_test.go`

**Interfaces:**
- Produces:
  ```go
  type SecretView struct { // service package; JSON for admin API
      Name string `json:"name"`; State string `json:"state"` // "set" | "requested"
      CreatedAt time.Time `json:"created_at"`; UpdatedAt time.Time `json:"updated_at"`
      RequestedAt *time.Time `json:"requested_at,omitempty"`; RequestedBy string `json:"requested_by,omitempty"`
      UsedBy []SecretUse `json:"used_by"`
  }
  type SecretUse struct { App, Service, EnvKey string; Stale bool; ApplyState, ApplyError string } // json snake_case
  func (s *Service) AdminSecrets(ctx, w store.Workspace) ([]SecretView, error)
  func (s *Service) CreateSecret(ctx, actor string, w store.Workspace, name, value string) error
  func (s *Service) SetSecret(ctx, actor string, w store.Workspace, name, value string) (affectedApps []string, err error) // enqueues apply
  func (s *Service) ReapplySecret(ctx, actor string, w store.Workspace, name string) (affectedApps []string, err error) // enqueues apps with stale refs of that secret
  func (s *Service) DeleteSecret(ctx, actor string, w store.Workspace, name string) error // CodeConflict listing apps while in use
  ```
  Routes (all under `/api/admin/workspaces/{name}/secrets`): `GET ""` → `[]SecretView`; `POST ""` `{name,value}` → 201; `PUT "/{secret}"` `{value}` → 200 `{"apps": [...]}`; `POST "/{secret}/apply"` → 200 `{"apps": [...]}`; `DELETE "/{secret}"` → 204. Invalid name/value → 422 `validation_failed` with field `name`/`value`. Audit actions `secret_create`, `secret_set`, `secret_apply_request`, `secret_delete` with params `{"name": ...}` only. `workspaceRow` gains `RequestedSecrets int \`json:"requested_secrets"\``; dashboard JSON gains `requested_secrets` (total). `fileContent` refuses paths whose split parts have length ≥ 3 and `parts[1] == "secrets"` with 422 and message `secret files are not shown`.

- [ ] **Step 1: Write failing tests:** CRUD round trip; no response body (GET list, PUT, POST) contains the value (`strings.Contains` on raw body); value with `\n`, empty value, 8193 bytes → 422; name `Bad` → 422; delete in use → 409 with app name in message; PUT on requested secret → state `set`; PUT returns affected apps; audit rows for all four actions have no value (scan `params_json` and `error`); `redact(map[string]any{"name":"x","value":"secret"})` yields `"value":"***"`; file content of `shop/secrets/web.env` → 422, listing of `shop/secrets` still works; dashboard and workspace row show `requested_secrets`.
- [ ] **Step 2: Run** `go test ./internal/admin/ ./internal/service/` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./...` — PASS.
- [ ] **Step 5: Commit** `feat(admin): manage workspace secrets`.

### Task 9: Agent-facing docs — whoami, MCP, install page

**Files:**
- Modify: `internal/service/read.go`, `internal/service/types.go`, `internal/mcpserver/mcpserver.go`, `internal/installpage/install.html.tmpl`, `web/src/agentPrompt.ts`
- Test: `internal/service/read` tests (existing file for whoami), `internal/mcpserver/mcpserver_test.go`, `internal/installpage/installpage_test.go`, `web/src/agentPrompt.test.ts`

**Interfaces:**
- Produces: `WhoamiResult.Secrets []SecretState \`json:"secrets"\`` with `SecretState{Name string \`json:"name"\`; State string \`json:"state"\`}` (never values). New guidance line, exact copy:
  `Never put credentials in environment. Reference a workspace secret instead: "secrets": {"STRIPE_API_KEY": "stripe-prod"}. validate/deploy register unknown names as requested; ask the operator to set the value in the admin UI. You never see secret values; redeploy is not needed after the operator changes one.`
  MCP `instructions` and the install page get one sentence each with the same meaning; the install page example adds `"secrets": {"STRIPE_API_KEY": "stripe-prod"}` to the web service.

- [ ] **Step 1: Write failing tests:** whoami lists `{name, state}` for set and requested secrets and the guidance contains `Never put credentials in environment`; MCP tools/list output for `deploy` input schema contains `secrets`; install page contains `"secrets"`; agent prompt test contains `secrets`.
- [ ] **Step 2: Run** `go test ./internal/... && (cd web && npx vitest run src/agentPrompt.test.ts)` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same — PASS.
- [ ] **Step 5: Commit** `docs(agent): explain workspace secrets to agents`.

### Task 10: Admin UI — Secrets tab, dashboard banner, file explorer

**Files:**
- Modify: `web/src/types.ts`, `web/src/api.ts`, `web/src/pages/WorkspaceDetail.tsx`, `web/src/pages/Dashboard.tsx`, `web/src/components/FileExplorer.tsx`
- Create: `web/src/components/SecretsTab.tsx`
- Test: `web/src/components/SecretsTab.test.tsx`, `web/src/pages/WorkspaceDetail.test.tsx`

**Interfaces:**
- Consumes: Task 8 routes and JSON.
- Produces: `api.secrets(ws)`, `api.createSecret(ws, name, value)`, `api.setSecret(ws, name, value) -> {apps: string[]}`, `api.reapplySecret(ws, name) -> {apps: string[]}`, `api.deleteSecret(ws, name)`; types `SecretView`, `SecretUse`; `WorkspaceRow.requested_secrets`, `Dashboard.requested_secrets`.

UI per spec §8.2: tab label `Secrets` (with `(N requested)` when `requested_secrets > 0`), placed after `Apps`; table columns Name, Status, Used by, actions; Set/Replace modal with Mantine `PasswordInput` (no visibility toggle: `visibilityToggleIcon={() => null}` or a plain `TextInput type="password"`), never prefilled; after save a notification `Updating: shop, admin` (or `Saved` when no app uses it); `stale` badge with tooltip text exactly: `New value not applied yet: the service was stopped, the update failed or the gateway restarted. Stopped services get it on the next deploy (restart keeps the old value).`; Delete disabled with tooltip listing apps while `used_by` is non-empty; `Re-apply` button when any use is stale. Dashboard banner `N requested secret(s) waiting for a value` linking to the first workspace with requests. File explorer shows `Secret file — not shown` for the 422 from Task 8.

- [ ] **Step 1: Write failing tests:** requested secret shows Set button; set modal input is type password and empty on open; submitting calls `PUT /workspaces/alice/secrets/mail` with `{value}` and shows notification with app names; Delete disabled when `used_by` non-empty; stale badge renders with the exact tooltip text on hover; dashboard banner appears when `requested_secrets > 0`.
- [ ] **Step 2: Run** `cd web && npx vitest run` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `cd web && npx vitest run && npx tsc --noEmit && npm run build` — PASS.
- [ ] **Step 5: Commit** `feat(web): secrets tab and requested-secrets banner`.

### Task 11: Integration test and main spec amendments

**Files:**
- Modify: `internal/service/integration_test.go`
- Modify: `docs/superpowers/specs/2026-10-01-deploy-gateway-design.md` (§6 layout adds `secrets/`, §7 `secrets` field, §9 `env_file`, §10 verifier rule + `env_file` no longer only forbidden-by-omission, §11 env file steps, §12 whoami/list fields, §14 Secrets tab + endpoints, §15 tables/columns), `README.md` (one paragraph on secrets)

- [ ] **Step 1: Write the integration test** `TestIntegrationSecrets` (`//go:build integration`, real Docker + `COMPOSE_BIN`): create secret `"pa$$ss \"q\" #x = y"`, deploy `busybox:1.37` running `sleep 300` with `secrets: {"A":"s"}`; `docker exec` (via Docker SDK exec or `docker` CLI in the test) prints `$A` equal to the value byte-exactly; `SetSecret` to `"new"`, wait for apply state `ok` (poll ≤ 60 s), container ID changed and `$A == "new"`; `Logs` of a service echoing `$A` returns `***`.
- [ ] **Step 2: Run** `COMPOSE_BIN=$(command -v docker-compose || echo /usr/libexec/docker/cli-plugins/docker-compose) go test -tags integration ./internal/service/ -run TestIntegrationSecrets -v` — PASS (it pins the behaviour verified in the spec §6.2).
- [ ] **Step 3: Amend the main spec and README** as listed.
- [ ] **Step 4: Run** `go vet ./... && go test ./... && (cd web && npx vitest run)` — PASS.
- [ ] **Step 5: Commit** `test: integration coverage for secrets; docs: amend main spec`.
