import { screen, within } from "@testing-library/react";
import { mockApi, quota, renderWithProviders } from "../test/utils";
import { Dashboard } from "./Dashboard";

const row = (name: string, requested: number) => ({
  name, quota, usage: { apps: 0, memory_mb: 0, cpus: 0 }, ports: { start: 0, end: 0, bind_ip: "" }, requested_secrets: requested,
});

test("the banner links to the first workspace with requested secrets", async () => {
  mockApi({ "GET /dashboard": { body: { workspaces: [row("alice", 0), row("bob", 2)], critical: [], orphans: 0, requested_secrets: 2 } } });
  renderWithProviders(<Dashboard />);
  expect(await screen.findByText(/2 requested secrets waiting for a value/)).toBeInTheDocument();
  const banner = screen.getByText(/waiting for a value/).closest(".mantine-Alert-root") as HTMLElement;
  expect(within(banner).getByRole("link", { name: "bob" })).toHaveAttribute("href", "/workspaces/bob");
});

test("no banner without requested secrets", async () => {
  mockApi({ "GET /dashboard": { body: { workspaces: [row("alice", 0)], critical: [], orphans: 0, requested_secrets: 0 } } });
  renderWithProviders(<Dashboard />);
  await screen.findByText("Recent critical events");
  expect(screen.queryByText(/waiting for a value/)).not.toBeInTheDocument();
});
