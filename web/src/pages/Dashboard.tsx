import { Alert, Anchor, Card, Loader, Progress, SimpleGrid, Stack, Text, Title } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { api } from "../api";
import { AuditTable } from "../components/AuditTable";
import { ErrorAlert } from "../components/ErrorAlert";
import type { WorkspaceRow } from "../types";

const pct = (used: number, max: number) => (max > 0 ? Math.min(100, (used / max) * 100) : 0);

function Meter({ label, used, max, unit = "" }: { label: string; used: number; max: number; unit?: string }) {
  return (
    <div>
      <Text size="sm">
        {label}: {used}
        {unit} / {max}
        {unit}
      </Text>
      <Progress value={pct(used, max)} color={pct(used, max) > 90 ? "red" : "indigo"} size="sm" />
    </div>
  );
}

function WorkspaceCard({ w }: { w: WorkspaceRow }) {
  return (
    <Card withBorder>
      <Stack gap="xs">
        <Anchor component={Link} to={`/workspaces/${w.name}`} fw={600}>
          {w.name}
        </Anchor>
        <Meter label="Apps" used={w.usage.apps} max={w.quota.max_apps} />
        <Meter label="Memory" used={w.usage.memory_mb} max={w.quota.max_total_memory_mb} unit=" MB" />
        <Meter label="CPUs" used={Number(w.usage.cpus.toFixed(2))} max={w.quota.max_total_cpus} />
        <Text size="xs" c="dimmed">
          Ports: {w.ports.start > 0 ? `${w.ports.bind_ip}:${w.ports.start}-${w.ports.end}` : "publishing disabled"}
        </Text>
      </Stack>
    </Card>
  );
}

export function Dashboard() {
  const q = useQuery({ queryKey: ["dashboard"], queryFn: api.dashboard });
  if (q.isPending) return <Loader />;
  if (q.isError) return <ErrorAlert error={q.error} />;
  const d = q.data;
  return (
    <Stack>
      <Title order={2}>Dashboard</Title>
      {d.orphans > 0 && (
        <Alert color="orange" title="Orphaned containers">
          {d.orphans} container(s) carry gateway labels but belong to no known app.{" "}
          <Anchor component={Link} to="/orphans">
            Review them
          </Anchor>
        </Alert>
      )}
      {d.workspaces.length === 0 ? (
        <Text c="dimmed">
          No workspaces yet.{" "}
          <Anchor component={Link} to="/workspaces">
            Create one
          </Anchor>
        </Text>
      ) : (
        <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }}>
          {d.workspaces.map((w) => (
            <WorkspaceCard key={w.name} w={w} />
          ))}
        </SimpleGrid>
      )}
      <Title order={3}>Recent critical events</Title>
      <AuditTable entries={d.critical} />
    </Stack>
  );
}
