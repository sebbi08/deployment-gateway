import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "./App";
import { mockApi, quota, renderWithProviders } from "./test/utils";

const dashboard = {
  workspaces: [{ name: "alice", quota, usage: { apps: 1, memory_mb: 256, cpus: 0.5 }, ports: { start: 20000, end: 20099, bind_ip: "0.0.0.0" } }],
  critical: [],
  orphans: 2,
};

test("login flow", async () => {
  let loggedIn = false;
  const calls = mockApi({
    "GET /me": () =>
      loggedIn ? { body: { actor: "admin", proxy_mode: false } } : { status: 401, body: { code: "login_required", message: "login required" } },
    "POST /login": (b) => {
      loggedIn = b.password === "correct horse battery";
      return loggedIn ? { status: 204 } : { status: 401, body: { code: "login_required", message: "wrong password" } };
    },
    "GET /dashboard": { body: dashboard },
  });
  renderWithProviders(<App />);
  const pw = await screen.findByLabelText(/admin password/i);
  await userEvent.type(pw, "nope");
  await userEvent.click(screen.getByRole("button", { name: "Log in" }));
  expect(await screen.findByText("wrong password")).toBeInTheDocument();
  await userEvent.clear(pw);
  await userEvent.type(pw, "correct horse battery");
  await userEvent.click(screen.getByRole("button", { name: "Log in" }));
  expect(await screen.findByRole("link", { name: "alice" })).toBeInTheDocument();
  expect(screen.getByText(/2 container\(s\)/)).toBeInTheDocument();
  expect(calls.filter((c) => c.path === "/login").at(-1)?.body).toEqual({ password: "correct horse battery" });
});

test("proxy-mode auth errors are shown instead of a login form", async () => {
  mockApi({ "GET /me": { status: 401, body: { code: "unauthorized", message: "identity header missing" } } });
  renderWithProviders(<App />);
  expect(await screen.findByText("identity header missing")).toBeInTheDocument();
  expect(screen.queryByLabelText(/admin password/i)).not.toBeInTheDocument();
});

test("logout returns to the login form", async () => {
  let loggedIn = true;
  mockApi({
    "GET /me": () => (loggedIn ? { body: { actor: "admin", proxy_mode: false } } : { status: 401, body: { code: "login_required", message: "login required" } }),
    "POST /logout": () => {
      loggedIn = false;
      return { status: 204 };
    },
    "GET /dashboard": { body: dashboard },
  });
  renderWithProviders(<App />);
  await userEvent.click(await screen.findByRole("button", { name: "Log out" }));
  expect(await screen.findByLabelText(/admin password/i)).toBeInTheDocument();
});

// A session that expires while editing (gateway restart, idle timeout) must
// not throw away the draft: login appears over the page, and a failed save
// triggers it too.
test("an expired session keeps unsaved drafts", async () => {
  let loggedIn = true;
  const expired = { status: 401, body: { code: "login_required", message: "login required" } };
  mockApi({
    "GET /me": () => (loggedIn ? { body: { actor: "admin", proxy_mode: false } } : expired),
    "POST /templates/preview": () => (loggedIn ? { body: { valid: true, errors: [], host: "", labels: [] } } : expired),
    "POST /templates": () => expired,
    "POST /login": () => {
      loggedIn = true;
      return { status: 204 };
    },
  });
  renderWithProviders(<App />, "/templates/new");
  await userEvent.type(await screen.findByLabelText(/^Name/), "keepme");
  loggedIn = false;
  await userEvent.click(screen.getByRole("button", { name: "Save template" }));
  const pw = await screen.findByLabelText(/admin password/i);
  expect(screen.getByLabelText(/^Name/)).toHaveValue("keepme");
  await userEvent.type(pw, "correct horse battery");
  await userEvent.click(screen.getByRole("button", { name: "Log in" }));
  await waitFor(() => expect(screen.queryByLabelText(/admin password/i)).not.toBeInTheDocument());
  expect(screen.getByLabelText(/^Name/)).toHaveValue("keepme");
});
