import { screen } from "@testing-library/react";
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
