// Mirrors the admin JSON API in internal/admin (Go).

export interface FieldError {
  field: string;
  message: string;
}

export interface Me {
  actor: string;
  proxy_mode: boolean;
  /** Public base URL of the agent endpoint (MCP_HOST); empty when not configured. */
  agent_url: string;
  /** Public base URL of the admin UI (ADMIN_HOST); empty when not configured. */
  admin_url: string;
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
