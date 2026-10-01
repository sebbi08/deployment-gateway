import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockApi, quota, renderWithProviders } from "../test/utils";
import { Audit } from "./Audit";
import { Orphans } from "./Orphans";
import { Settings } from "./Settings";

const entry = {
  id: 1, time: "2026-10-01T10:00:00Z", actor_type: "agent", actor: "token:1", workspace: "alice", action: "deploy",
  params: { app: "shop" }, result: "security_violation", error: "rejected", severity: "critical", duration_ms: 12, client_ip: "203.0.113.1",
};

test("audit severity filter is sent to the API", async () => {
  const calls = mockApi({ "GET /audit": { body: { entries: [entry], next_before: 0 } } });
  renderWithProviders(<Audit />);
  expect(await screen.findByText("security_violation")).toBeInTheDocument();
  await userEvent.click(screen.getByText("critical"));
  await waitFor(() => expect(calls.at(-1)?.search).toBe("?severity=critical"));
});

test("settings are saved", async () => {
  const calls = mockApi({
    "GET /settings": { body: { default_quota: quota, audit_retention_days: 90 } },
    "PUT /settings": (b) => ({ body: b }),
  });
  renderWithProviders(<Settings />);
  const retention = await screen.findByLabelText(/retention/i);
  await userEvent.clear(retention);
  await userEvent.type(retention, "30");
  await userEvent.click(screen.getByRole("button", { name: "Save settings" }));
  expect(await screen.findByText("Settings saved")).toBeInTheDocument();
  expect(calls.find((c) => c.method === "PUT")?.body).toEqual({ default_quota: quota, audit_retention_days: 30 });
});

test("orphans can be removed", async () => {
  let gone = false;
  const calls = mockApi({
    "GET /orphans": () => ({ body: gone ? [] : [{ id: "ghost-x-web", name: "ghost-x-web", workspace: "ghost", app: "x", state: "running", status: "Up" }] }),
    "DELETE /orphans/ghost-x-web": () => {
      gone = true;
      return { status: 204 };
    },
  });
  renderWithProviders(<Orphans />);
  await userEvent.click(await screen.findByRole("button", { name: "Remove" }));
  expect(await screen.findByText("No orphaned containers.")).toBeInTheDocument();
  expect(calls.some((c) => c.method === "DELETE")).toBe(true);
});
