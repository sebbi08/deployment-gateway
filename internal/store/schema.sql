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
