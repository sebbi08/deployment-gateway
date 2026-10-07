import {
  Alert, Anchor, Badge, Button, Checkbox, Code, CopyButton, Group, Loader, Modal, Stack, Table, Tabs, Text, TextInput, Title,
} from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { api } from "../api";
import { ConfirmDelete } from "../components/ConfirmDelete";
import { ErrorAlert } from "../components/ErrorAlert";
import { PortFields } from "../components/PortFields";
import { QuotaFields } from "../components/QuotaFields";
import type { AppView, WorkspaceDetail as Detail } from "../types";

const stateColor = (s: string) => (s === "running" ? "green" : s === "missing" ? "gray" : "orange");
const fmt = (t: string | null | undefined) => (t ? new Date(t).toLocaleString() : "never");

function useInvalidate(name: string) {
  const queryClient = useQueryClient();
  return () => {
    queryClient.invalidateQueries({ queryKey: ["workspace", name] });
    queryClient.invalidateQueries({ queryKey: ["workspaces"] });
    queryClient.invalidateQueries({ queryKey: ["dashboard"] });
  };
}

function AppsTab({ ws }: { ws: Detail }) {
  const invalidate = useInvalidate(ws.name);
  const [deleting, setDeleting] = useState<AppView | null>(null);
  const stop = useMutation({ mutationFn: (app: string) => api.stopApp(ws.name, app), onSuccess: invalidate });
  const del = useMutation({
    mutationFn: ({ app, confirm }: { app: string; confirm: string }) => api.deleteApp(ws.name, app, confirm),
    onSuccess: () => {
      setDeleting(null);
      notifications.show({ color: "green", message: "App deleted" });
      invalidate();
    },
  });
  if (ws.apps.length === 0) return <Text c="dimmed">No apps deployed.</Text>;
  return (
    <Stack>
      <ErrorAlert error={stop.error} />
      <Table striped verticalSpacing="sm">
        <Table.Thead>
          <Table.Tr>
            <Table.Th>App</Table.Th>
            <Table.Th>Services</Table.Th>
            <Table.Th>Disk</Table.Th>
            <Table.Th>Last deploy</Table.Th>
            <Table.Th />
          </Table.Tr>
        </Table.Thead>
        <Table.Tbody>
          {ws.apps.map((a) => (
            <Table.Tr key={a.app}>
              <Table.Td fw={600}>{a.app}</Table.Td>
              <Table.Td>
                <Stack gap={4}>
                  {a.services.map((s) => (
                    <Group key={s.service} gap="xs">
                      <Code>{s.internal_host}</Code>
                      <Badge color={stateColor(s.state)} variant="light">
                        {s.state}
                        {s.health ? ` · ${s.health}` : ""}
                      </Badge>
                      {s.url && (
                        <Anchor href={s.url} target="_blank" size="sm">
                          {s.url}
                        </Anchor>
                      )}
                      {s.ports.map((p) => (
                        <Text key={`${p.protocol}${p.host_port}`} size="xs" c="dimmed">
                          {p.host_port}/{p.protocol} → {p.container_port}
                        </Text>
                      ))}
                    </Group>
                  ))}
                </Stack>
              </Table.Td>
              <Table.Td>{Math.round(a.disk_bytes / 1048576)} MB</Table.Td>
              <Table.Td>
                {a.last_deploy_at ? new Date(a.last_deploy_at).toLocaleString() : "-"}{" "}
                <Badge color={a.last_deploy_result === "ok" ? "green" : "red"} variant="light">
                  {a.last_deploy_result || "-"}
                </Badge>
              </Table.Td>
              <Table.Td>
                <Group gap="xs" wrap="nowrap">
                  <Button size="xs" variant="default" loading={stop.isPending && stop.variables === a.app} onClick={() => stop.mutate(a.app)}>
                    Stop
                  </Button>
                  <Button size="xs" color="red" variant="light" onClick={() => setDeleting(a)}>
                    Delete
                  </Button>
                </Group>
              </Table.Td>
            </Table.Tr>
          ))}
        </Table.Tbody>
      </Table>
      <Text size="xs" c="dimmed">
        Stop is temporary: the agent can start it again with restart or a new deploy. To keep an app down, revoke the
        workspace's tokens or delete the app.
      </Text>
      <Modal opened={deleting !== null} onClose={() => setDeleting(null)} title={`Delete app ${deleting?.app ?? ""}`}>
        {deleting && (
          <Stack>
            <Text size="sm">Stops and removes all containers and deletes the app's data folder. This cannot be undone.</Text>
            <ErrorAlert error={del.error} />
            <ConfirmDelete
              name={deleting.app}
              label="Delete app"
              loading={del.isPending}
              onConfirm={(confirm) => del.mutate({ app: deleting.app, confirm })}
            />
          </Stack>
        )}
      </Modal>
    </Stack>
  );
}

function TokensTab({ ws }: { ws: Detail }) {
  const invalidate = useInvalidate(ws.name);
  const [label, setLabel] = useState("");
  const [newToken, setNewToken] = useState<string | null>(null);
  const create = useMutation({
    mutationFn: () => api.createToken(ws.name, label),
    onSuccess: (res) => {
      setNewToken(res.token);
      setLabel("");
      invalidate();
    },
  });
  const revoke = useMutation({ mutationFn: (id: number) => api.revokeToken(ws.name, id), onSuccess: invalidate });
  return (
    <Stack>
      <ErrorAlert error={create.error ?? revoke.error} />
      {ws.tokens.length > 0 && (
        <Table striped>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Label</Table.Th>
              <Table.Th>Prefix</Table.Th>
              <Table.Th>Created</Table.Th>
              <Table.Th>Last used</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {ws.tokens.map((t) => (
              <Table.Tr key={t.id}>
                <Table.Td>{t.label}</Table.Td>
                <Table.Td>
                  <Code>{t.prefix}…</Code>
                </Table.Td>
                <Table.Td>{fmt(t.created_at)}</Table.Td>
                <Table.Td>{fmt(t.last_used_at)}</Table.Td>
                <Table.Td>
                  {t.revoked_at ? (
                    <Badge color="gray">revoked</Badge>
                  ) : (
                    <Button size="xs" color="red" variant="light" onClick={() => revoke.mutate(t.id)}>
                      Revoke
                    </Button>
                  )}
                </Table.Td>
              </Table.Tr>
            ))}
          </Table.Tbody>
        </Table>
      )}
      <form
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate();
        }}
      >
        <Group align="end">
          <TextInput label="Token label" placeholder="e.g. claude-laptop" value={label} onChange={(e) => setLabel(e.currentTarget.value)} required />
          <Button type="submit" loading={create.isPending}>
            Create token
          </Button>
        </Group>
      </form>
      <Modal opened={newToken !== null} onClose={() => setNewToken(null)} title="New token" size="lg" closeOnClickOutside={false}>
        <Stack>
          <Alert color="yellow">Copy this token now. It is shown only once and stored only as a hash.</Alert>
          <Code block>{newToken}</Code>
          <Group justify="space-between">
            <CopyButton value={newToken ?? ""}>
              {({ copied, copy }) => (
                <Button variant="light" onClick={copy}>
                  {copied ? "Copied" : "Copy token"}
                </Button>
              )}
            </CopyButton>
            <Button onClick={() => setNewToken(null)}>I have stored it</Button>
          </Group>
          <Text size="sm" c="dimmed">
            Agents connect to https://&lt;agent-host&gt;/mcp with the header Authorization: Bearer &lt;token&gt;.
          </Text>
        </Stack>
      </Modal>
    </Stack>
  );
}

function GrantsTab({ ws }: { ws: Detail }) {
  const invalidate = useInvalidate(ws.name);
  const [selected, setSelected] = useState<string[]>(ws.templates.filter((t) => t.granted).map((t) => String(t.id)));
  const save = useMutation({
    mutationFn: () => api.setGrants(ws.name, selected.map(Number)),
    onSuccess: () => {
      notifications.show({ color: "green", message: "Grants saved" });
      invalidate();
    },
  });
  if (ws.templates.length === 0) {
    return (
      <Text c="dimmed">
        No templates defined yet.{" "}
        <Anchor component={Link} to="/templates/new">
          Create one
        </Anchor>
      </Text>
    );
  }
  return (
    <Stack>
      <ErrorAlert error={save.error} />
      <Checkbox.Group label="Templates this workspace may use" value={selected} onChange={setSelected}>
        <Stack gap="xs" mt="xs">
          {ws.templates.map((t) => (
            <Checkbox key={t.id} value={String(t.id)} label={`${t.name}${t.host_pattern ? ` (${t.host_pattern})` : ""}`} />
          ))}
        </Stack>
      </Checkbox.Group>
      <Group>
        <Button onClick={() => save.mutate()} loading={save.isPending}>
          Save grants
        </Button>
      </Group>
    </Stack>
  );
}

function QuotaTab({ ws }: { ws: Detail }) {
  const invalidate = useInvalidate(ws.name);
  const [quota, setQuota] = useState(ws.quota);
  const [ports, setPorts] = useState(ws.ports);
  const save = useMutation({
    mutationFn: () => api.updateWorkspace(ws.name, { quota, ports }),
    onSuccess: () => {
      notifications.show({ color: "green", message: "Quota and ports saved" });
      invalidate();
    },
  });
  return (
    <Stack>
      <ErrorAlert error={save.error} />
      <QuotaFields value={quota} onChange={setQuota} />
      <PortFields value={ports} onChange={setPorts} />
      <Group>
        <Button onClick={() => save.mutate()} loading={save.isPending}>
          Save
        </Button>
      </Group>
    </Stack>
  );
}

function DangerTab({ ws }: { ws: Detail }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const del = useMutation({
    mutationFn: (confirm: string) => api.deleteWorkspace(ws.name, confirm),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["workspaces"] });
      queryClient.invalidateQueries({ queryKey: ["dashboard"] });
      notifications.show({ color: "green", message: `Workspace ${ws.name} deleted` });
      navigate("/workspaces");
    },
  });
  return (
    <Stack>
      <Text>Deletes all apps of this workspace including their data, the workspace folder and all tokens.</Text>
      <ErrorAlert error={del.error} />
      <ConfirmDelete name={ws.name} label="Delete workspace" loading={del.isPending} onConfirm={(c) => del.mutate(c)} />
    </Stack>
  );
}

export function WorkspaceDetail() {
  const { name = "" } = useParams();
  const q = useQuery({ queryKey: ["workspace", name], queryFn: () => api.workspace(name) });
  if (q.isPending) return <Loader />;
  // Replace the page only when nothing was loaded yet: a failed background
  // refetch must not unmount the tabs (and with them e.g. the token modal).
  if (q.isError && !q.data) return <ErrorAlert error={q.error} />;
  const ws = q.data;
  return (
    <Stack>
      {q.isError && <ErrorAlert error={q.error} />}
      <div>
        <Title order={2}>Workspace {ws.name}</Title>
        <Text size="sm" c="dimmed">
          Folder <Code>{ws.folder}</Code>
        </Text>
      </div>
      <Tabs defaultValue="apps" keepMounted={false}>
        <Tabs.List>
          <Tabs.Tab value="apps">Apps ({ws.apps.length})</Tabs.Tab>
          <Tabs.Tab value="tokens">Tokens</Tabs.Tab>
          <Tabs.Tab value="templates">Templates</Tabs.Tab>
          <Tabs.Tab value="quota">Quota &amp; ports</Tabs.Tab>
          <Tabs.Tab value="danger" color="red">
            Danger zone
          </Tabs.Tab>
        </Tabs.List>
        <Tabs.Panel value="apps" pt="md">
          <AppsTab ws={ws} />
        </Tabs.Panel>
        <Tabs.Panel value="tokens" pt="md">
          <TokensTab ws={ws} />
        </Tabs.Panel>
        <Tabs.Panel value="templates" pt="md">
          <GrantsTab ws={ws} />
        </Tabs.Panel>
        <Tabs.Panel value="quota" pt="md">
          <QuotaTab ws={ws} />
        </Tabs.Panel>
        <Tabs.Panel value="danger" pt="md">
          <DangerTab ws={ws} />
        </Tabs.Panel>
      </Tabs>
    </Stack>
  );
}
