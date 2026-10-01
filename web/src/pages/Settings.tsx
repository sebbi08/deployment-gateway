import { Button, Card, Group, Loader, NumberInput, Stack, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { api } from "../api";
import { ErrorAlert } from "../components/ErrorAlert";
import { QuotaFields } from "../components/QuotaFields";
import type { Settings as SettingsT } from "../types";

export function Settings() {
  const queryClient = useQueryClient();
  const q = useQuery({ queryKey: ["settings"], queryFn: api.settings });
  const [draft, setDraft] = useState<SettingsT | null>(null);
  useEffect(() => {
    if (q.data && draft === null) setDraft(q.data);
  }, [q.data, draft]);
  const save = useMutation({
    mutationFn: () => api.saveSettings(draft!),
    onSuccess: (s) => {
      queryClient.setQueryData(["settings"], s);
      notifications.show({ color: "green", message: "Settings saved" });
    },
  });
  if (q.isError) return <ErrorAlert error={q.error} />;
  if (draft === null) return <Loader />;
  return (
    <Stack>
      <Title order={2}>Settings</Title>
      <ErrorAlert error={save.error} />
      <Card withBorder>
        <Title order={4} mb="sm">
          Default quota for new workspaces
        </Title>
        <QuotaFields value={draft.default_quota} onChange={(qq) => setDraft({ ...draft, default_quota: qq })} />
      </Card>
      <Card withBorder>
        <NumberInput
          label="Audit log retention (days)"
          min={1}
          max={3650}
          value={draft.audit_retention_days}
          onChange={(v) => setDraft({ ...draft, audit_retention_days: Number(v) || 0 })}
          maw={300}
        />
      </Card>
      <Group>
        <Button onClick={() => save.mutate()} loading={save.isPending}>
          Save settings
        </Button>
      </Group>
    </Stack>
  );
}
