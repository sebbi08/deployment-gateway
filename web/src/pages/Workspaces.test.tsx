import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { mockApi, quota, renderWithProviders } from "../test/utils";
import { Workspaces } from "./Workspaces";

test("create workspace with defaults from settings", async () => {
  const calls = mockApi({
    "GET /workspaces": { body: [] },
    "GET /settings": { body: { default_quota: quota, audit_retention_days: 90 } },
    "POST /workspaces": (b) =>
      b.name === "Bad-Name"
        ? { status: 422, body: { code: "validation_failed", message: "workspace name must match ^[a-z][a-z0-9]{1,15}$" } }
        : { status: 201, body: { name: b.name, quota: b.quota, usage: { apps: 0, memory_mb: 0, cpus: 0 }, ports: b.ports } },
  });
  renderWithProviders(
    <Routes>
      <Route path="/workspaces" element={<Workspaces />} />
      <Route path="/workspaces/:name" element={<div>detail page</div>} />
    </Routes>,
    "/workspaces",
  );
  await userEvent.click(await screen.findByRole("button", { name: "New workspace" }));
  const name = await screen.findByLabelText(/^Name/);
  await userEvent.type(name, "Bad-Name");
  await userEvent.click(screen.getByRole("button", { name: "Create workspace" }));
  expect(await screen.findByText(/workspace name must match/)).toBeInTheDocument();

  await userEvent.clear(name);
  await userEvent.type(name, "alice");
  const maxApps = screen.getByLabelText("Max apps");
  await userEvent.clear(maxApps);
  await userEvent.type(maxApps, "3");
  await userEvent.click(screen.getByRole("button", { name: "Create workspace" }));
  expect(await screen.findByText("detail page")).toBeInTheDocument();
  const post = calls.filter((c) => c.method === "POST").at(-1)!;
  expect(post.body).toEqual({ name: "alice", quota: { ...quota, max_apps: 3 }, ports: { start: 0, end: 0, bind_ip: "0.0.0.0" } });
});
