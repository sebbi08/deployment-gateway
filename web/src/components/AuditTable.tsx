import { Badge, Code, Table, Text } from "@mantine/core";
import type { AuditEntry } from "../types";

const severityColor: Record<string, string> = { info: "gray", warn: "yellow", critical: "red" };

export function AuditTable({ entries }: { entries: AuditEntry[] }) {
  if (entries.length === 0) {
    return <Text c="dimmed">No entries.</Text>;
  }
  return (
    <Table.ScrollContainer minWidth={900}>
      <Table striped verticalSpacing="xs">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>Time</Table.Th>
            <Table.Th>Actor</Table.Th>
            <Table.Th>Workspace</Table.Th>
            <Table.Th>Action</Table.Th>
            <Table.Th>Result</Table.Th>
            <Table.Th>Error</Table.Th>
            <Table.Th>Params</Table.Th>
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {entries.map((e) => (
            <Table.Tr key={e.id} bg={e.severity === "critical" ? "var(--mantine-color-red-light)" : undefined}>
              <Table.Td>{new Date(e.time).toLocaleString()}</Table.Td>
              <Table.Td>
                {e.actor}
                {e.client_ip && (
                  <Text size="xs" c="dimmed">
                    {e.client_ip}
                  </Text>
                )}
              </Table.Td>
              <Table.Td>{e.workspace}</Table.Td>
              <Table.Td>{e.action}</Table.Td>
              <Table.Td>
                <Badge color={e.result === "ok" ? "green" : severityColor[e.severity] ?? "gray"} variant="light">
                  {e.result}
                </Badge>
              </Table.Td>
              <Table.Td style={{ whiteSpace: "pre-wrap" }}>{e.error}</Table.Td>
              <Table.Td>
                <Code style={{ wordBreak: "break-all" }}>{JSON.stringify(e.params)}</Code>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
    </Table.ScrollContainer>
  );
}
