export const TOKEN_PLACEHOLDER = "<TOKEN>";
export const AGENT_URL_PLACEHOLDER = "https://<MCP_HOST>";

export interface PromptInput {
  /** Public base URL of the agent endpoint, e.g. https://mcp.example.com. */
  agentURL: string;
  /** Public base URL of the admin UI. */
  adminURL: string;
  workspace: string;
  /** The real token, if it is known (right after creating it); otherwise a placeholder is used. */
  token?: string;
}

/** The text an admin hands to an agent so it can install and use the MCP server. */
export function agentPrompt({ agentURL, adminURL, workspace, token }: PromptInput): string {
  const base = (agentURL || AGENT_URL_PLACEHOLDER).replace(/\/+$/, "");
  const admin = adminURL.replace(/\/+$/, "");
  const mcp = `${base}/mcp`;
  const tok = token || TOKEN_PLACEHOLDER;
  return `Connect to the deploy-gateway MCP server so you can deploy apps into workspace "${workspace}".

Setup guide (read it first): ${base}/install
MCP endpoint (streamable HTTP): ${mcp}
Auth header: Authorization: Bearer ${tok}

Steps:
1. Read the setup guide and add the MCP server, named "deploy-gateway", to the MCP client you are running in. For Claude Code:
   claude mcp add --transport http deploy-gateway ${mcp} --header "Authorization: Bearer ${tok}"
2. Keep the token secret: do not commit it, do not write it into files inside a repository, and do not print it in logs, issues or pull requests.
3. Verify the connection by calling the whoami tool; it must report workspace "${workspace}". If the tools only become available after a restart of the client, tell me.
4. Before deploying, follow the workflow from the guide: whoami -> list_templates -> validate -> deploy.

If the token is rejected (HTTP 401), ask me for a new one; tokens are managed in the admin UI at ${admin}/workspaces/${encodeURIComponent(workspace)}.
`;
}
