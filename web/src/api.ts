import type {
  AuditFilter, AuditPage, Dashboard, FieldError, Me, Orphan, PortSettings, Preview, Quota,
  Settings, StoredTemplate, Template, WorkspaceDetail, WorkspaceRow,
} from "./types";

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly fields: FieldError[];
  readonly detail: string;

  constructor(status: number, code: string, message: string, fields: FieldError[] = [], detail = "") {
    super(message);
    this.status = status;
    this.code = code;
    this.fields = fields;
    this.detail = detail;
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`/api/admin${path}`, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) {
    return undefined as T;
  }
  const text = await res.text();
  let data: any = undefined;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    data = undefined;
  }
  if (!res.ok) {
    throw new ApiError(res.status, data?.code ?? "error", data?.message ?? (text || res.statusText), data?.errors ?? [], data?.detail ?? "");
  }
  return data as T;
}

const enc = encodeURIComponent;

export const api = {
  me: () => request<Me>("GET", "/me"),
  login: (password: string) => request<void>("POST", "/login", { password }),
  logout: () => request<void>("POST", "/logout"),
  dashboard: () => request<Dashboard>("GET", "/dashboard"),

  workspaces: () => request<WorkspaceRow[]>("GET", "/workspaces"),
  workspace: (name: string) => request<WorkspaceDetail>("GET", `/workspaces/${enc(name)}`),
  createWorkspace: (body: { name: string; quota: Quota; ports: PortSettings }) =>
    request<WorkspaceRow>("POST", "/workspaces", body),
  updateWorkspace: (name: string, body: { quota: Quota; ports: PortSettings }) =>
    request<WorkspaceDetail>("PUT", `/workspaces/${enc(name)}`, body),
  deleteWorkspace: (name: string, confirm: string) => request<void>("DELETE", `/workspaces/${enc(name)}`, { confirm }),
  setGrants: (name: string, templateIDs: number[]) =>
    request<void>("PUT", `/workspaces/${enc(name)}/grants`, { template_ids: templateIDs }),
  createToken: (name: string, label: string) => request<{ token: string }>("POST", `/workspaces/${enc(name)}/tokens`, { label }),
  revokeToken: (name: string, id: number) => request<void>("POST", `/workspaces/${enc(name)}/tokens/${id}/revoke`),
  stopApp: (name: string, app: string) => request<void>("POST", `/workspaces/${enc(name)}/apps/${enc(app)}/stop`),
  deleteApp: (name: string, app: string, confirm: string) =>
    request<void>("DELETE", `/workspaces/${enc(name)}/apps/${enc(app)}`, { confirm }),

  templates: () => request<StoredTemplate[]>("GET", "/templates"),
  template: (id: number) => request<StoredTemplate>("GET", `/templates/${id}`),
  createTemplate: (t: Template) => request<{ id: number }>("POST", "/templates", t),
  updateTemplate: (id: number, t: Template) => request<StoredTemplate>("PUT", `/templates/${id}`, t),
  deleteTemplate: (id: number) => request<void>("DELETE", `/templates/${id}`),
  previewTemplate: (id: number, template: Template) => request<Preview>("POST", "/templates/preview", { id, template }),

  audit: (f: AuditFilter) => {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(f)) {
      if (v !== undefined && v !== "" && v !== 0) q.set(k, String(v));
    }
    const qs = q.toString();
    return request<AuditPage>("GET", `/audit${qs ? `?${qs}` : ""}`);
  },
  settings: () => request<Settings>("GET", "/settings"),
  saveSettings: (s: Settings) => request<Settings>("PUT", "/settings", s),
  orphans: () => request<Orphan[]>("GET", "/orphans"),
  removeOrphan: (id: string) => request<void>("DELETE", `/orphans/${enc(id)}`),
};

/** Human-readable text for any error, including field errors and admin detail. */
export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) {
    const lines = [e.message];
    for (const f of e.fields) {
      if (f.message !== e.message) lines.push(`${f.field}: ${f.message}`);
    }
    if (e.detail) lines.push(e.detail);
    return lines.join("\n");
  }
  return e instanceof Error ? e.message : String(e);
}
