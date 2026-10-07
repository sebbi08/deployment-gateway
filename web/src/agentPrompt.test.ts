import { agentPrompt } from "./agentPrompt";

test("the prompt uses the real MCP and admin URLs", () => {
  const p = agentPrompt({ agentURL: "https://mcp.example.com/", adminURL: "https://deploy-admin.example.com", workspace: "alice" });
  expect(p).toContain("Setup guide (read it first): https://mcp.example.com/install");
  expect(p).toContain("MCP endpoint (streamable HTTP): https://mcp.example.com/mcp");
  expect(p).toContain(
    'claude mcp add --transport http deploy-gateway https://mcp.example.com/mcp --header "Authorization: Bearer <TOKEN>"',
  );
  expect(p).toContain('it must report workspace "alice"');
  expect(p).toContain("https://deploy-admin.example.com/workspaces/alice");
});

test("a known token replaces the placeholder", () => {
  const p = agentPrompt({ agentURL: "https://mcp.example.com", adminURL: "https://a.example.com", workspace: "alice", token: "dgw_abc" });
  expect(p).toContain("Authorization: Bearer dgw_abc");
  expect(p).not.toContain("<TOKEN>");
});

test("a missing MCP host becomes a visible placeholder", () => {
  const p = agentPrompt({ agentURL: "", adminURL: "https://a.example.com", workspace: "alice" });
  expect(p).toContain("https://<MCP_HOST>/mcp");
});
