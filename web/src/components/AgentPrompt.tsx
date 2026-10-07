import { Alert, Button, Code, CopyButton, Group, Stack, Text } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { api } from "../api";
import { TOKEN_PLACEHOLDER, agentPrompt } from "../agentPrompt";

/** A copyable prompt that tells an agent how to install the MCP server for this workspace. */
export function AgentPrompt({ workspace, token }: { workspace: string; token?: string }) {
  const me = useQuery({ queryKey: ["me"], queryFn: api.me, retry: false });
  const agentURL = me.data?.agent_url ?? "";
  const adminURL = me.data?.admin_url || window.location.origin;
  const prompt = agentPrompt({ agentURL, adminURL, workspace, token });
  return (
    <Stack gap="xs">
      {me.isSuccess && !agentURL && (
        <Alert color="yellow" title="MCP host unknown">
          The gateway has no MCP_HOST configured, so the prompt contains a placeholder. Set MCP_HOST (and ADMIN_HOST) in the
          gateway's environment, e.g. MCP_HOST=mcp.example.com, and restart it.
        </Alert>
      )}
      <Code block style={{ whiteSpace: "pre-wrap" }} data-testid="agent-prompt">
        {prompt}
      </Code>
      <Group justify="space-between" align="center">
        <Text size="sm" c="dimmed">
          {token
            ? "Contains the token: paste it only into the agent that should use it."
            : `Replace ${TOKEN_PLACEHOLDER} with a token of this workspace (create one in the Tokens tab).`}
        </Text>
        <CopyButton value={prompt}>
          {({ copied, copy }) => (
            <Button variant="light" onClick={copy}>
              {copied ? "Copied" : "Copy prompt"}
            </Button>
          )}
        </CopyButton>
      </Group>
    </Stack>
  );
}
