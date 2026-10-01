import { screen } from "@testing-library/react";
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
