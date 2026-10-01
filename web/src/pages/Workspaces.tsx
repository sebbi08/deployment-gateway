import { Anchor, Button, Group, Loader, Modal, Stack, Table, Text, TextInput, Title } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router";
import { api } from "../api";
import { ErrorAlert } from "../components/ErrorAlert";
import { PortFields } from "../components/PortFields";
import { QuotaFields } from "../components/QuotaFields";
import type { PortSettings, Quota } from "../types";

function CreateWorkspaceForm({ onDone }: { onDone: () => void }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const settings = useQuery({ queryKey: ["settings"], queryFn: api.settings });
  const [name, setName] = useState("");
  const [quota, setQuota] = useState<Quota | null>(null);
  const [ports, setPorts] = useState<PortSettings>({ start: 0, end: 0, bind_ip: "0.0.0.0" });
  useEffect(() => {
    if (settings.data && quota === null) setQuota(settings.data.default_quota);
  }, [settings.data, quota]);
  const create = useMutation({
    mutationFn: () => api.createWorkspace({ name, quota: quota!, ports }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["workspaces"] });
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notifications.show({ color: "green", message: `Workspace ${name} created` });
      onDone();
      navigate(`/workspaces/${name}`);
    },
  });
  if (settings.isError) return <ErrorAlert error={settings.error} />;
  if (quota === null) return <Loader />;
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate();
      }}
    >
      <Stack>
        <ErrorAlert error={create.error} />
        <TextInput
          label="Name"
          description="Lowercase letters and digits, 2-16 characters, no hyphens"
          value={name}
          onChange={(e) => setName(e.currentTarget.value)}
          required
        />
        <QuotaFields value={quota} onChange={setQuota} />
        <PortFields value={ports} onChange={setPorts} />
        <Group justify="flex-end">
          <Button type="submit" loading={create.isPending}>
            Create workspace
          </Button>
        </Group>
      </Stack>
    </form>
  );
}

export function Workspaces() {
  const q = useQuery({ queryKey: ["workspaces"], queryFn: api.workspaces });
  const [opened, { open, close }] = useDisclosure(false);
  return (
    <Stack>
      <Group justify="space-between">
        <Title order={2}>Workspaces</Title>
        <Button onClick={open}>New workspace</Button>
      </Group>
      <Modal opened={opened} onClose={close} title="New workspace" size="lg">
        <CreateWorkspaceForm onDone={close} />
      </Modal>
      {q.isPending && <Loader />}
      <ErrorAlert error={q.error} />
      {q.data && q.data.length === 0 && <Text c="dimmed">No workspaces yet.</Text>}
      {q.data && q.data.length > 0 && (
        <Table striped highlightOnHover>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Workspace</Table.Th>
              <Table.Th>Apps</Table.Th>
              <Table.Th>Memory (MB)</Table.Th>
              <Table.Th>CPUs</Table.Th>
              <Table.Th>Published ports</Table.Th>
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {q.data.map((w) => (
              <Table.Tr key={w.name}>
                <Table.Td>
                  <Anchor component={Link} to={`/workspaces/${w.name}`}>
                    {w.name}
                  </Anchor>
                </Table.Td>
                <Table.Td>
                  {w.usage.apps} / {w.quota.max_apps}
                </Table.Td>
                <Table.Td>
                  {w.usage.memory_mb} / {w.quota.max_total_memory_mb}
                </Table.Td>
                <Table.Td>
                  {w.usage.cpus.toFixed(2)} / {w.quota.max_total_cpus}
                </Table.Td>
                <Table.Td>{w.ports.start > 0 ? `${w.ports.bind_ip}:${w.ports.start}-${w.ports.end}` : "disabled"}</Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
    </Stack>
  );
}
