import { Button, Group, Loader, SegmentedControl, Stack, TextInput, Title } from "@mantine/core";
import { useDebouncedValue } from "@mantine/hooks";
import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api";
import { AuditTable } from "../components/AuditTable";
import { ErrorAlert } from "../components/ErrorAlert";

export function Audit() {
  const [workspace, setWorkspace] = useState("");
  const [action, setAction] = useState("");
  const [result, setResult] = useState("");
  const [severity, setSeverity] = useState("any");
  const [cursors, setCursors] = useState<number[]>([]);
  const [text] = useDebouncedValue({ workspace, action, result }, 300);
  const before = cursors.at(-1) ?? 0;
  const filter = { ...text, severity: severity === "any" ? "" : severity, before };
  const q = useQuery({ queryKey: ["audit", filter], queryFn: () => api.audit(filter), placeholderData: keepPreviousData });
  const reset = () => setCursors([]);

  return (
    <Stack>
      <Title order={2}>Audit log</Title>
      <Group align="end">
        <TextInput label="Workspace" value={workspace} onChange={(e) => { setWorkspace(e.currentTarget.value); reset(); }} />
        <TextInput label="Action" value={action} onChange={(e) => { setAction(e.currentTarget.value); reset(); }} />
        <TextInput label="Result" value={result} onChange={(e) => { setResult(e.currentTarget.value); reset(); }} />
        <SegmentedControl
          data={["any", "info", "warn", "critical"]}
          value={severity}
          onChange={(v) => {
            setSeverity(v);
            reset();
          }}
        />
        {q.isFetching && <Loader size="sm" />}
      </Group>
      <ErrorAlert error={q.error} />
      {q.data && <AuditTable entries={q.data.entries} />}
      <Group>
        <Button variant="default" disabled={cursors.length === 0} onClick={() => setCursors(cursors.slice(0, -1))}>
          Newer
        </Button>
        <Button variant="default" disabled={!q.data || q.data.next_before === 0} onClick={() => q.data && setCursors([...cursors, q.data.next_before])}>
          Older
        </Button>
      </Group>
    </Stack>
  );
}
