import { Badge, Button, Group, Modal, Stack, Table, Text, TextInput, Tooltip } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api";
import type { SecretUse, SecretView } from "../types";
import { ErrorAlert } from "./ErrorAlert";

const STALE_TIP =
  "New value not applied yet: the service was stopped, the update failed or the gateway restarted. Stopped services get it on the next deploy (restart keeps the old value).";

function ago(t: string | undefined): string {
  if (!t) return "";
  const s = Math.max(0, (Date.now() - new Date(t).getTime()) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)} min ago`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ago`;
  return `${Math.floor(s / 86400)} d ago`;
}

const APPLY_STATE: Record<string, string> = {
  running: "Updating",
  skipped: "Update skipped",
  failed: "Update failed",
  interrupted: "Update interrupted",
};

/** Why a stale use still has the old value, from the app's last apply status. */
function applyStatus(u: SecretUse): string {
  const label = APPLY_STATE[u.apply_state] ?? "";
  if (label && u.apply_error) return `${label}: ${u.apply_error}`;
  return label || u.apply_error;
}

/** While a use is stale a background update may be running: poll until it clears. */
function staleRefetch(data: SecretView[] | undefined): number | false {
  return data?.some((s) => s.used_by.some((u) => u.stale)) ? 5000 : false;
}

type Dialog = { kind: "new" } | { kind: "set"; secret: SecretView } | { kind: "delete"; secret: SecretView };

export function SecretsTab({ workspace }: { workspace: string }) {
  const queryClient = useQueryClient();
  const q = useQuery({
    queryKey: ["secrets", workspace],
    queryFn: () => api.secrets(workspace),
    refetchInterval: (query) => staleRefetch(query.state.data),
  });
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [name, setName] = useState("");
  const [value, setValue] = useState("");

  const refresh = () => {
    queryClient.invalidateQueries({ queryKey: ["secrets", workspace] });
    queryClient.invalidateQueries({ queryKey: ["workspace", workspace] });
    queryClient.invalidateQueries({ queryKey: ["workspaces"] });
    queryClient.invalidateQueries({ queryKey: ["dashboard"] });
  };
  const close = () => {
    setDialog(null);
    setName("");
    setValue("");
    create.reset();
    set.reset();
    del.reset();
  };
  const announce = (apps: string[]) =>
    notifications.show({ color: "green", message: apps.length > 0 ? `Updating: ${apps.join(", ")}` : "Saved" });

  const create = useMutation({
    mutationFn: () => api.createSecret(workspace, name, value),
    onSuccess: () => {
      notifications.show({ color: "green", message: "Saved" });
      close();
      refresh();
    },
  });
  const set = useMutation({
    mutationFn: (secret: string) => api.setSecret(workspace, secret, value),
    onSuccess: (res) => {
      announce(res.apps);
      close();
      refresh();
    },
  });
  const reapply = useMutation({
    mutationFn: (secret: string) => api.reapplySecret(workspace, secret),
    onSuccess: (res) => {
      announce(res.apps);
      refresh();
    },
  });
  const del = useMutation({
    mutationFn: (secret: string) => api.deleteSecret(workspace, secret),
    onSuccess: () => {
      notifications.show({ color: "green", message: "Secret deleted" });
      close();
      refresh();
    },
  });

  const secrets = q.data ?? [];
  return (
    <Stack>
      <Group justify="space-between">
        <Text size="sm" c="dimmed">
          Values are write-only: they are never shown again, not even to admins.
        </Text>
        <Button size="xs" onClick={() => setDialog({ kind: "new" })}>
          + New secret
        </Button>
      </Group>
      <ErrorAlert error={q.error ?? reapply.error} />
      {q.isSuccess && secrets.length === 0 && <Text c="dimmed">No secrets yet.</Text>}
      {secrets.length > 0 && (
        <Table striped>
          <Table.Thead>
            <Table.Tr>
              <Table.Th>Name</Table.Th>
              <Table.Th>Status</Table.Th>
              <Table.Th>Used by</Table.Th>
              <Table.Th />
            </Table.Tr>
          </Table.Thead>
          <Table.Tbody>
            {secrets.map((s) => {
              const requested = s.state === "requested";
              const apps = [...new Set(s.used_by.map((u) => u.app))];
              const anyStale = s.used_by.some((u) => u.stale);
              return (
                <Table.Tr key={s.name}>
                  <Table.Td>
                    <Text ff="monospace" size="sm">
                      {s.name}
                    </Text>
                  </Table.Td>
                  <Table.Td>
                    <Badge color={requested ? "orange" : "green"} variant="light">
                      {s.state}
                    </Badge>
                    {!requested && (
                      <Text size="xs" c="dimmed">
                        updated {ago(s.updated_at)}
                      </Text>
                    )}
                  </Table.Td>
                  <Table.Td>
                    {s.used_by.length === 0 ? (
                      <Text size="sm" c="dimmed">
                        —{requested && s.requested_by ? ` (asked by ${s.requested_by}, ${ago(s.requested_at)})` : ""}
                      </Text>
                    ) : (
                      <Stack gap={2}>
                        {s.used_by.map((u) => {
                          const status = u.stale ? applyStatus(u) : "";
                          return (
                            <div key={`${u.app}/${u.service}/${u.env_key}`}>
                              <Group gap="xs">
                                <Text size="sm" ff="monospace">
                                  {u.app}/{u.service} {u.env_key}
                                </Text>
                                {u.stale && (
                                  <Tooltip label={STALE_TIP} multiline w={320} withArrow>
                                    <Badge color="yellow" variant="light">
                                      stale
                                    </Badge>
                                  </Tooltip>
                                )}
                              </Group>
                              {status && (
                                <Text size="xs" c="dimmed" lineClamp={3} title={status} style={{ whiteSpace: "pre-wrap" }}>
                                  {status}
                                </Text>
                              )}
                            </div>
                          );
                        })}
                      </Stack>
                    )}
                  </Table.Td>
                  <Table.Td>
                    <Group gap="xs" wrap="nowrap">
                      <Button size="xs" variant="default" onClick={() => setDialog({ kind: "set", secret: s })}>
                        {requested ? "Set" : "Replace"}
                      </Button>
                      {anyStale && (
                        <Button
                          size="xs"
                          variant="default"
                          loading={reapply.isPending && reapply.variables === s.name}
                          onClick={() => reapply.mutate(s.name)}
                        >
                          Re-apply
                        </Button>
                      )}
                      <Tooltip label={`In use by: ${apps.join(", ")}`} disabled={apps.length === 0} withArrow>
                        <span>
                          <Button
                            size="xs"
                            color="red"
                            variant="light"
                            disabled={apps.length > 0}
                            onClick={() => setDialog({ kind: "delete", secret: s })}
                          >
                            Delete
                          </Button>
                        </span>
                      </Tooltip>
                    </Group>
                  </Table.Td>
                </Table.Tr>
              );
            })}
          </Table.Tbody>
        </Table>
      )}

      <Modal opened={dialog?.kind === "new"} onClose={close} title="New secret">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <Stack>
            <ErrorAlert error={create.error} />
            <TextInput label="Name" value={name} onChange={(e) => setName(e.currentTarget.value)} autoComplete="off" required />
            <TextInput
              type="password"
              label="Value"
              value={value}
              onChange={(e) => setValue(e.currentTarget.value)}
              autoComplete="new-password"
              required
            />
            <Button type="submit" loading={create.isPending}>
              Save
            </Button>
          </Stack>
        </form>
      </Modal>

      <Modal
        opened={dialog?.kind === "set"}
        onClose={close}
        title={dialog?.kind === "set" ? `${dialog.secret.state === "requested" ? "Set" : "Replace"} secret ${dialog.secret.name}` : ""}
      >
        {dialog?.kind === "set" && (
          <form
            onSubmit={(e) => {
              e.preventDefault();
              set.mutate(dialog.secret.name);
            }}
          >
            <Stack>
              <ErrorAlert error={set.error} />
              <TextInput
              type="password"
                label="Value"
                value={value}
                onChange={(e) => setValue(e.currentTarget.value)}
                autoComplete="new-password"
                data-autofocus
                required
              />
              <Button type="submit" loading={set.isPending}>
                Save
              </Button>
            </Stack>
          </form>
        )}
      </Modal>

      <Modal opened={dialog?.kind === "delete"} onClose={close} title={dialog?.kind === "delete" ? `Delete secret ${dialog.secret.name}` : ""}>
        {dialog?.kind === "delete" && (
          <Stack>
            <Text size="sm">Removes the secret. An agent can request it again by deploying a stack that references it.</Text>
            <ErrorAlert error={del.error} />
            <Button color="red" loading={del.isPending} onClick={() => del.mutate(dialog.secret.name)}>
              Delete secret
            </Button>
          </Stack>
        )}
      </Modal>
    </Stack>
  );
}
