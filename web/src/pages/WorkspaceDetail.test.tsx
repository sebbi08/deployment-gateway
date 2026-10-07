import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { mockApi, quota, renderWithProviders } from "../test/utils";
import type { WorkspaceDetail as Detail } from "../types";
import { WorkspaceDetail } from "./WorkspaceDetail";

const detail: Detail = {
  name: "alice",
  quota,
  usage: { apps: 1, memory_mb: 256, cpus: 0.5 },
  ports: { start: 20000, end: 20099, bind_ip: "0.0.0.0" },
  folder: "/srv/workspaces/alice",
  templates: [{ id: 1, name: "public-web", host_pattern: "{{app}}.{{workspace}}.apps.example.com", granted: false }],
  tokens: [],
  apps: [
    {
      app: "shop",
      disk_bytes: 0,
      last_deploy_result: "ok",
      last_deploy_at: "2026-10-01T10:00:00Z",
      services: [{ service: "web", internal_host: "alice-shop-web", url: "https://shop.alice.apps.example.com", state: "running", ports: [] }],
    },
  ],
};

function renderDetail() {
  renderWithProviders(
    <Routes>
      <Route path="/workspaces/:name" element={<WorkspaceDetail />} />
    </Routes>,
    "/workspaces/alice",
  );
}

test("a new token is shown exactly once", async () => {
  mockApi({
    "GET /workspaces/alice": { body: detail },
    "POST /workspaces/alice/tokens": { status: 201, body: { token: "dgw_secretsecretsecret" } },
  });
  renderDetail();
  await userEvent.click(await screen.findByRole("tab", { name: "Tokens" }));
  await userEvent.type(screen.getByLabelText(/Token label/), "claude");
  await userEvent.click(screen.getByRole("button", { name: "Create token" }));
  expect(await screen.findByText("dgw_secretsecretsecret")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "I have stored it" }));
  expect(screen.queryByText("dgw_secretsecretsecret")).not.toBeInTheDocument();
});

const me = { actor: "admin", proxy_mode: false, agent_url: "https://mcp.example.com", admin_url: "https://deploy-admin.example.com" };

test("the agent setup tab shows a copyable prompt with the real hosts", async () => {
  mockApi({ "GET /workspaces/alice": { body: detail }, "GET /me": { body: me } });
  renderDetail();
  await userEvent.click(await screen.findByRole("tab", { name: "Agent setup" }));
  const prompt = await screen.findByTestId("agent-prompt");
  await waitFor(() => expect(prompt).toHaveTextContent("https://mcp.example.com/install"));
  expect(prompt).toHaveTextContent("https://mcp.example.com/mcp");
  expect(prompt).toHaveTextContent("Authorization: Bearer <TOKEN>");
  expect(prompt).toHaveTextContent("https://deploy-admin.example.com/workspaces/alice");
  expect(screen.getByRole("button", { name: "Copy prompt" })).toBeInTheDocument();
  expect(screen.queryByText("MCP host unknown")).not.toBeInTheDocument();
});

test("the agent setup tab warns when MCP_HOST is not configured", async () => {
  mockApi({ "GET /workspaces/alice": { body: detail }, "GET /me": { body: { ...me, agent_url: "", admin_url: "" } } });
  renderDetail();
  await userEvent.click(await screen.findByRole("tab", { name: "Agent setup" }));
  expect(await screen.findByText("MCP host unknown")).toBeInTheDocument();
  expect(screen.getByTestId("agent-prompt")).toHaveTextContent("https://<MCP_HOST>/mcp");
  expect(screen.getByTestId("agent-prompt")).toHaveTextContent(`${window.location.origin}/workspaces/alice`);
});

test("the new-token modal offers the prompt with the token filled in", async () => {
  mockApi({
    "GET /workspaces/alice": { body: detail },
    "GET /me": { body: me },
    "POST /workspaces/alice/tokens": { status: 201, body: { token: "dgw_secretsecretsecret" } },
  });
  renderDetail();
  await userEvent.click(await screen.findByRole("tab", { name: "Tokens" }));
  await userEvent.type(screen.getByLabelText(/Token label/), "claude");
  await userEvent.click(screen.getByRole("button", { name: "Create token" }));
  const prompt = await screen.findByTestId("agent-prompt");
  await waitFor(() => expect(prompt).toHaveTextContent("https://mcp.example.com/mcp"));
  expect(prompt).toHaveTextContent("Authorization: Bearer dgw_secretsecretsecret");
  expect(prompt).not.toHaveTextContent("<TOKEN>");
});

test("the apps tab explains that stopping does not keep an app down", async () => {
  mockApi({ "GET /workspaces/alice": { body: detail } });
  renderDetail();
  expect(await screen.findByText(/the agent can start it again/i)).toBeInTheDocument();
  expect(screen.getByText(/revoke the workspace's tokens/i)).toBeInTheDocument();
});

test("deleting an app requires typing its name", async () => {
  const calls = mockApi({
    "GET /workspaces/alice": { body: detail },
    "DELETE /workspaces/alice/apps/shop": { status: 204 },
  });
  renderDetail();
  expect(await screen.findByText("https://shop.alice.apps.example.com")).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Delete" }));
  const confirmButton = await screen.findByRole("button", { name: "Delete app" });
  expect(confirmButton).toBeDisabled();
  await userEvent.type(screen.getByLabelText("Type shop to confirm"), "shop");
  expect(confirmButton).toBeEnabled();
  await userEvent.click(confirmButton);
  expect(await screen.findByText("App deleted")).toBeInTheDocument();
  expect(calls.find((c) => c.method === "DELETE")?.body).toEqual({ confirm: "shop" });
});

test("grants are saved as template ids", async () => {
  const calls = mockApi({ "GET /workspaces/alice": { body: detail }, "PUT /workspaces/alice/grants": { status: 204 } });
  renderDetail();
  await userEvent.click(await screen.findByRole("tab", { name: "Templates" }));
  await userEvent.click(screen.getByLabelText(/public-web/));
  await userEvent.click(screen.getByRole("button", { name: "Save grants" }));
  expect(await screen.findByText("Grants saved")).toBeInTheDocument();
  expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ template_ids: [1] });
});

// Creating a token refetches the workspace; if that refetch fails (e.g. the
// Docker daemon is restarting), the one-time token must stay visible.
test("a failed refetch keeps the new token visible", async () => {
  let loads = 0;
  mockApi({
    "GET /workspaces/alice": () => (loads++ === 0 ? { body: detail } : { status: 500, body: { code: "internal", message: "docker down" } }),
    "POST /workspaces/alice/tokens": { status: 201, body: { token: "dgw_onlyshownonce" } },
  });
  renderDetail();
  await userEvent.click(await screen.findByRole("tab", { name: "Tokens" }));
  await userEvent.type(screen.getByLabelText(/Token label/), "claude");
  await userEvent.click(screen.getByRole("button", { name: "Create token" }));
  expect(await screen.findByText("dgw_onlyshownonce")).toBeInTheDocument();
  await screen.findByText(/docker down/);
  expect(screen.getByText("dgw_onlyshownonce")).toBeInTheDocument();
});

test("a double click on Revoke sends one request", async () => {
  const withToken: Detail = { ...detail, tokens: [{ id: 3, label: "ci", prefix: "dgw_abcd", created_at: "2026-10-01T10:00:00Z", last_used_at: null, revoked_at: null }] };
  const calls = mockApi({
    "GET /workspaces/alice": { body: withToken },
    "POST /workspaces/alice/tokens/3/revoke": { status: 204 },
  });
  renderDetail();
  await userEvent.click(await screen.findByRole("tab", { name: "Tokens" }));
  await userEvent.dblClick(await screen.findByRole("button", { name: "Revoke" }));
  await waitFor(() => expect(calls.filter((c) => c.method === "POST" && c.path.endsWith("/revoke"))).toHaveLength(1));
  await new Promise((r) => setTimeout(r, 100));
  expect(calls.filter((c) => c.method === "POST" && c.path.endsWith("/revoke"))).toHaveLength(1);
});
