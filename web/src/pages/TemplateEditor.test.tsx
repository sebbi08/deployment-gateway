import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Route, Routes } from "react-router";
import { mockApi, renderWithProviders } from "../test/utils";
import { TemplateEditor, parseLabels } from "./TemplateEditor";

test("parseLabels splits at the first = and reports bad lines", () => {
  expect(parseLabels("a=b=c\n\n x = y ")).toEqual({ labels: { a: "b=c", x: "y" }, errors: [] });
  expect(parseLabels("novalue").errors).toEqual(["Labels line 1: expected key=value"]);
});

test("live preview shows errors and hosts, then saves", async () => {
  const calls = mockApi({
    "POST /templates/preview": (b) =>
      b.template.host_pattern.includes("{{workspace}}")
        ? { body: { valid: true, errors: [], host: "shop.alice.apps.example.com", labels: [{ key: "traefik.enable", value: "true" }] } }
        : { body: { valid: false, errors: ["host_pattern must contain {{workspace}} and {{app}}"], host: "", labels: [] } },
    "POST /templates": { status: 201, body: { id: 7 } },
  });
  renderWithProviders(
    <Routes>
      <Route path="/templates/new" element={<TemplateEditor />} />
      <Route path="/templates" element={<div>template list</div>} />
    </Routes>,
    "/templates/new",
  );
  await userEvent.type(await screen.findByLabelText(/^Name/), "public-web");
  const host = screen.getByLabelText(/^Host pattern/);
  await userEvent.click(host);
  await userEvent.paste("{{app}}.apps.example.com");
  expect(await screen.findByText("host_pattern must contain {{workspace}} and {{app}}", {}, { timeout: 3000 })).toBeInTheDocument();

  await userEvent.clear(host);
  await userEvent.click(host);
  await userEvent.paste("{{app}}.{{workspace}}.apps.example.com");
  await userEvent.click(screen.getByLabelText(/^Networks/));
  await userEvent.paste("apps");
  await userEvent.click(screen.getByLabelText(/^Labels/));
  await userEvent.paste("traefik.enable=true");
  expect(await screen.findByText("shop.alice.apps.example.com", {}, { timeout: 3000 })).toBeInTheDocument();

  await userEvent.click(screen.getByRole("button", { name: "Save template" }));
  expect(await screen.findByText("template list")).toBeInTheDocument();
  expect(calls.find((c) => c.method === "POST" && c.path === "/templates")?.body).toEqual({
    name: "public-web",
    description: "",
    host_pattern: "{{app}}.{{workspace}}.apps.example.com",
    networks: ["apps"],
    labels: { "traefik.enable": "true" },
  });
});

test("editing loads the stored template and saves changes via PUT", async () => {
  const stored = { id: 7, name: "public-web", description: "old", host_pattern: "{{app}}.{{workspace}}.apps.example.com", networks: ["apps"], labels: { "traefik.enable": "true" } };
  const calls = mockApi({
    "GET /templates/7": { body: stored },
    "POST /templates/preview": { body: { valid: true, errors: [], host: "shop.alice.apps.example.com", labels: [] } },
    "PUT /templates/7": (b) => ({ body: { id: 7, ...b } }),
  });
  renderWithProviders(
    <Routes>
      <Route path="/templates/:id" element={<TemplateEditor />} />
      <Route path="/templates" element={<div>template list</div>} />
    </Routes>,
    "/templates/7",
  );
  expect(await screen.findByLabelText(/^Name/)).toHaveValue("public-web");
  const desc = screen.getByLabelText(/^Description/);
  await userEvent.clear(desc);
  await userEvent.type(desc, "new");
  await userEvent.click(screen.getByRole("button", { name: "Save template" }));
  expect(await screen.findByText("template list")).toBeInTheDocument();
  expect(calls.find((c) => c.method === "PUT" && c.path === "/templates/7")?.body).toEqual({
    name: "public-web",
    description: "new",
    host_pattern: "{{app}}.{{workspace}}.apps.example.com",
    networks: ["apps"],
    labels: { "traefik.enable": "true" },
  });
});

test("parseLabels reports duplicate keys instead of dropping one", () => {
  const { errors } = parseLabels("traefik.http.routers.{{id}}.middlewares=auth@docker\ntraefik.http.routers.{{id}}.middlewares = compress@docker");
  expect(errors).toEqual(['Labels line 2: duplicate key "traefik.http.routers.{{id}}.middlewares" (also on line 1)']);
});
