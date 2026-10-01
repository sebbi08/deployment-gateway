import { Badge, Button, Loader, Stack, Table, Text, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "../api";
import { ErrorAlert } from "../components/ErrorAlert";

export function Orphans() {
  const queryClient = useQueryClient();
  const q = useQuery({ queryKey: ["orphans"], queryFn: api.orphans });
  const remove = useMutation({
    mutationFn: api.removeOrphan,
    onSuccess: () => {
      notifications.show({ color: "green", message: "Container removed" });
      queryClient.invalidateQueries({ queryKey: ["orphans"] });
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
    },
  });
  return (
    <Stack>
      <Title order={2}>Orphaned containers</Title>
      <Text size="sm" c="dimmed">
        Containers carrying deploy-gateway labels whose workspace or app no longer exists.
      </Text>
      <ErrorAlert error={q.error ?? remove.error} />
      {q.isPending && <Loader />}
      {q.data && q.data.length === 0 && <Text c="dimmed">No orphaned containers.</Text>}
      {q.data && q.data.length > 0 && (
        <Table striped>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Workspace</Table.Th>
              <Table.Th>App</Table.Th>
              <Table.Th>State</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {q.data.map((o) => (
              <Table.Tr key={o.id}>
                <Table.Td>{o.name}</Table.Td>
                <Table.Td>{o.workspace}</Table.Td>
                <Table.Td>{o.app}</Table.Td>
                <Table.Td>
                  <Badge variant="light">{o.state}</Badge>
                </Table.Td>
                <Table.Td>
                  <Button size="xs" color="red" variant="light" loading={remove.isPending && remove.variables === o.id} onClick={() => remove.mutate(o.id)}>
                    Remove
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
