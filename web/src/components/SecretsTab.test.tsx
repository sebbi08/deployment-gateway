import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockApi, renderWithProviders } from "../test/utils";
import type { SecretView } from "../types";
import { SecretsTab } from "./SecretsTab";

const secrets: SecretView[] = [
  { name: "mail", state: "requested", created_at: "2026-11-01T10:00:00Z", updated_at: "2026-11-01T10:00:00Z", requested_at: "2026-11-01T10:00:00Z", requested_by: "agent-1", used_by: [] },
  {
    name: "stripe", state: "set", created_at: "2026-11-01T10:00:00Z", updated_at: "2026-11-02T10:00:00Z", requested_by: "agent-1",
    used_by: [
      { app: "shop", service: "web", env_key: "STRIPE", stale: true, apply_state: "failed", apply_error: "boom" },
      { app: "admin", service: "web", env_key: "STRIPE", stale: false, apply_state: "applied", apply_error: "" },
    ],
  },
  { name: "unused", state: "set", created_at: "2026-11-01T10:00:00Z", updated_at: "2026-11-02T10:00:00Z", used_by: [] },
];

function setup(extra: Record<string, any> = {}) {
  const calls = mockApi({ "GET /workspaces/alice/secrets": { body: secrets }, ...extra });
  renderWithProviders(<SecretsTab workspace="alice" />);
  return calls;
}

const row = async (name: string) => (await screen.findByText(name)).closest("tr") as HTMLElement;

test("a requested secret shows a Set button and who asked", async () => {
  setup();
  const r = await row("mail");
  expect(within(r).getByRole("button", { name: "Set" })).toBeInTheDocument();
  expect(within(r).getByText(/asked by agent-1/)).toBeInTheDocument();
  const s = await row("stripe");
  expect(within(s).queryByText(/asked by/)).not.toBeInTheDocument();
  expect(within(s).getByRole("button", { name: "Replace" })).toBeInTheDocument();
});

test("the set modal is an empty password field and saving shows the updated apps", async () => {
  const calls = setup({ "PUT /workspaces/alice/secrets/mail": { body: { apps: ["shop", "admin"] } } });
  await userEvent.click(within(await row("mail")).getByRole("button", { name: "Set" }));
  const input = await screen.findByLabelText(/^Value/);
  expect(input).toHaveAttribute("type", "password");
  expect(input).toHaveValue("");
  await userEvent.type(input, "hunter2");
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(await screen.findByText("Updating: shop, admin")).toBeInTheDocument();
  const put = calls.find((c) => c.method === "PUT");
  expect(put?.path).toBe("/workspaces/alice/secrets/mail");
  expect(put?.body).toEqual({ value: "hunter2" });
});

test("saving a secret nobody uses says Saved", async () => {
  setup({ "PUT /workspaces/alice/secrets/mail": { body: { apps: [] } } });
  await userEvent.click(within(await row("mail")).getByRole("button", { name: "Set" }));
  await userEvent.type(await screen.findByLabelText(/^Value/), "x");
  await userEvent.click(screen.getByRole("button", { name: "Save" }));
  expect(await screen.findByText("Saved")).toBeInTheDocument();
});

test("delete is disabled while apps use the secret", async () => {
  setup();
  expect(within(await row("stripe")).getByRole("button", { name: "Delete" })).toBeDisabled();
  expect(within(await row("unused")).getByRole("button", { name: "Delete" })).toBeEnabled();
});

test("a stale use shows a badge with the explanation and a Re-apply button", async () => {
  const calls = setup({ "POST /workspaces/alice/secrets/stripe/apply": { body: { apps: ["shop"] } } });
  const s = await row("stripe");
  const badge = within(s).getByText("stale");
  await userEvent.hover(badge);
  expect(
    await screen.findByText(
      "New value not applied yet: the service was stopped, the update failed or the gateway restarted. Stopped services get it on the next deploy (restart keeps the old value).",
    ),
  ).toBeInTheDocument();
  await userEvent.click(within(s).getByRole("button", { name: "Re-apply" }));
  await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.path === "/workspaces/alice/secrets/stripe/apply")).toBe(true));
  expect(within(await row("unused")).queryByRole("button", { name: "Re-apply" })).not.toBeInTheDocument();
});
