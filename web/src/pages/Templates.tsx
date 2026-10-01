import { Anchor, Badge, Button, Code, Group, Loader, Stack, Table, Text, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router";
import { api } from "../api";
import { ErrorAlert } from "../components/ErrorAlert";

export function Templates() {
  const queryClient = useQueryClient();
  const q = useQuery({ queryKey: ["templates"], queryFn: api.templates });
  const del = useMutation({
    mutationFn: api.deleteTemplate,
    onSuccess: () => {
      notifications.show({ color: "green", message: "Template deleted" });
      queryClient.invalidateQueries({ queryKey: ["templates"] });
    },
  });
  return (
    <Stack>
      <Group justify="space-between">
        <Title order={2}>Exposure templates</Title>
        <Button component={Link} to="/templates/new">
          New template
        </Button>
      </Group>
      <Text size="sm" c="dimmed">
        Templates define how agent services are exposed (networks, reverse-proxy labels, host pattern). Grant them per workspace.
      </Text>
      <ErrorAlert error={q.error ?? del.error} />
      {q.isPending && <Loader />}
      {q.data && q.data.length === 0 && <Text c="dimmed">No templates yet.</Text>}
      {q.data && q.data.length > 0 && (
        <Table striped highlightOnHover>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Description</Table.Th>
              <Table.Th>Host pattern</Table.Th>
              <Table.Th>Networks</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {q.data.map((t) => (
              <Table.Tr key={t.id}>
                <Table.Td>
                  <Anchor component={Link} to={`/templates/${t.id}`}>
                    {t.name}
                  </Anchor>
                </Table.Td>
                <Table.Td>{t.description}</Table.Td>
                <Table.Td>{t.host_pattern ? <Code>{t.host_pattern}</Code> : <Text c="dimmed">none</Text>}</Table.Td>
                <Table.Td>
                  <Group gap={4}>
                    {t.networks.map((n) => (
                      <Badge key={n} variant="light">
                        {n}
                      </Badge>
                    ))}
                  </Group>
                </Table.Td>
                <Table.Td>
                  <Button size="xs" color="red" variant="light" loading={del.isPending && del.variables === t.id} onClick={() => del.mutate(t.id)}>
                    Delete
                  </Button>
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
    </Stack>
  );
}
