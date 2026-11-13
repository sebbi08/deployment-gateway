import { MantineProvider, type MantineThemeOverride } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import type { ReactElement } from "react";
import { MemoryRouter } from "react-router";
import { vi } from "vitest";
import { createQueryClient } from "../App";
import { theme } from "../theme";
import type { Quota } from "../types";

export function renderWithProviders(ui: ReactElement, route = "/", providerTheme: MantineThemeOverride = theme) {
  const queryClient = createQueryClient({ retry: false });
  return render(
    <MantineProvider theme={providerTheme} env="test">
      <Notifications />
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={[route]}>{ui}</MemoryRouter>
      </QueryClientProvider>
    </MantineProvider>,
  );
}

export interface Reply {
  status?: number;
  body?: unknown;
}
export type Handler = (body: any, url: URL) => Reply;
export interface Call {
  method: string;
  path: string;
  search: string;
  body: any;
}

/** Stubs fetch for /api/admin/*. Routes are keyed "METHOD /path" (path without /api/admin, without query). */
export function mockApi(routes: Record<string, Handler | Reply>): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), "http://localhost");
      const method = (init?.method ?? "GET").toUpperCase();
      const path = url.pathname.replace(/^\/api\/admin/, "");
      const body = init?.body ? JSON.parse(String(init.body)) : undefined;
      calls.push({ method, path, search: url.search, body });
      const route = routes[`${method} ${path}`];
      if (!route) {
        return new Response(JSON.stringify({ code: "not_found", message: `unmocked ${method} ${path}` }), { status: 404 });
      }
      const reply = typeof route === "function" ? route(body, url) : route;
      const status = reply.status ?? 200;
      return new Response(status === 204 ? null : JSON.stringify(reply.body ?? {}), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return calls;
}

export const quota: Quota = {
  max_apps: 5, max_services_per_app: 4, max_total_memory_mb: 2048, max_total_cpus: 2,
  default_memory_mb: 256, max_memory_mb: 1024, default_cpus: 0.5, max_cpus: 1, default_pids: 256, max_pids: 1024,
};
