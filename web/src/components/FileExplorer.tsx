import { Alert, Badge, Box, Button, Code, Grid, Group, Loader, Paper, ScrollArea, Stack, Text, UnstyledButton } from "@mantine/core";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { api } from "../api";
import type { FileEntry } from "../types";
import { ErrorAlert } from "./ErrorAlert";

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

const join = (dir: string, name: string) => (dir ? `${dir}/${name}` : name);
const indent = (depth: number) => depth * 16 + 4;

interface TreeProps {
  workspace: string;
  path: string;
  depth: number;
  selected: string | null;
  onSelect: (path: string) => void;
}

function DirContents({ workspace, path, depth, selected, onSelect }: TreeProps) {
  const q = useQuery({ queryKey: ["files", workspace, "list", path], queryFn: () => api.listFiles(workspace, path) });
  if (q.isPending) return <Loader size="xs" ml={indent(depth)} my={4} />;
  if (q.isError) return <ErrorAlert error={q.error} />;
  if (q.data.entries.length === 0 && q.data.more === 0) {
    return (
      <Text size="xs" c="dimmed" pl={indent(depth)} py={2}>
        empty
      </Text>
    );
  }
  return (
    <>
      {q.data.entries.map((e) => (
        <TreeEntry key={e.name} entry={e} workspace={workspace} path={join(path, e.name)} depth={depth} selected={selected} onSelect={onSelect} />
      ))}
      {q.data.more > 0 && (
        <Text size="xs" c="dimmed" pl={indent(depth)} py={2}>
          +{q.data.more} more not shown
        </Text>
      )}
    </>
  );
}

const rowStyle = (depth: number, active = false) => ({
  display: "flex",
  width: "100%",
  gap: 6,
  alignItems: "baseline",
  paddingLeft: indent(depth),
  paddingTop: 2,
  paddingBottom: 2,
  borderRadius: 4,
  background: active ? "var(--mantine-primary-color-light)" : undefined,
});

function Meta({ children }: { children: ReactNode }) {
  return (
    <Text span size="xs" c="dimmed" style={{ whiteSpace: "nowrap" }}>
      {children}
    </Text>
  );
}

function TreeEntry({ entry, workspace, path, depth, selected, onSelect }: TreeProps & { entry: FileEntry }) {
  const [open, setOpen] = useState(false);
  const name = (
    <Text span size="sm" ff="monospace" style={{ wordBreak: "break-all" }}>
      {entry.name}
    </Text>
  );
  if (entry.type === "dir") {
    return (
      <>
        <UnstyledButton style={rowStyle(depth)} onClick={() => setOpen((o) => !o)} aria-expanded={open}>
          <Text span size="sm" aria-hidden w={12}>
            {open ? "▾" : "▸"}
          </Text>
          {name}
        </UnstyledButton>
        {open && <DirContents workspace={workspace} path={path} depth={depth + 1} selected={selected} onSelect={onSelect} />}
      </>
    );
  }
  if (entry.type === "file") {
    return (
      <UnstyledButton style={rowStyle(depth, selected === path)} onClick={() => onSelect(path)}>
        <Text span size="sm" aria-hidden w={12} />
        {name}
        <Meta>{formatBytes(entry.size)}</Meta>
      </UnstyledButton>
    );
  }
  // Symlinks and special files are listed but never opened.
  return (
    <Box style={rowStyle(depth)}>
      <Text span size="sm" aria-hidden w={12} />
      {name}
      {entry.type === "symlink" ? (
        <>
          <Meta>→</Meta>
          <Text span size="xs" c="dimmed" ff="monospace" style={{ wordBreak: "break-all" }}>
            {entry.target}
          </Text>
          <Badge size="xs" variant="light" color="gray">
            not followed
          </Badge>
        </>
      ) : (
        <Badge size="xs" variant="light" color="gray">
          {entry.type}
        </Badge>
      )}
    </Box>
  );
}

function FileViewer({ workspace, path }: { workspace: string; path: string }) {
  const q = useQuery({ queryKey: ["files", workspace, "content", path], queryFn: () => api.fileContent(workspace, path) });
  return (
    <Stack gap="xs">
      <Group justify="space-between" wrap="nowrap">
        <Text size="sm" ff="monospace" fw={600} data-testid="file-viewer-path" style={{ wordBreak: "break-all" }}>
          {path}
        </Text>
        {q.data && (
          <Meta>
            {formatBytes(q.data.size)} · modified {new Date(q.data.mod_time).toLocaleString()}
          </Meta>
        )}
      </Group>
      {q.isPending && <Loader size="sm" />}
      {q.isError && <ErrorAlert error={q.error} />}
      {q.data?.binary && <Text c="dimmed">Binary file, {formatBytes(q.data.size)}. Content is not shown.</Text>}
      {q.data && !q.data.binary && (
        <>
          {q.data.truncated && (
            <Alert color="yellow" p="xs">
              Showing the first 512 KB of {formatBytes(q.data.size)}.
            </Alert>
          )}
          <ScrollArea.Autosize mah={600} type="auto">
            <Code block style={{ whiteSpace: "pre" }}>
              {q.data.text}
            </Code>
          </ScrollArea.Autosize>
        </>
      )}
    </Stack>
  );
}

/** Read-only view of a workspace folder: compose files and app data. */
export function FileExplorer({ workspace }: { workspace: string }) {
  const [selected, setSelected] = useState<string | null>(null);
  const queryClient = useQueryClient();
  return (
    <Stack>
      <Group justify="space-between">
        <Text size="sm" c="dimmed">
          Read-only. Symlinks are shown but never followed. Text files are shown up to 512 KB.
        </Text>
        <Button size="xs" variant="default" onClick={() => queryClient.invalidateQueries({ queryKey: ["files", workspace] })}>
          Refresh
        </Button>
      </Group>
      <Grid>
        <Grid.Col span={{ base: 12, md: 4 }}>
          <Paper withBorder p="xs">
            <ScrollArea.Autosize mah={640} type="auto">
              <DirContents workspace={workspace} path="" depth={0} selected={selected} onSelect={setSelected} />
            </ScrollArea.Autosize>
          </Paper>
        </Grid.Col>
        <Grid.Col span={{ base: 12, md: 8 }}>
          <Paper withBorder p="sm">
            {selected ? <FileViewer workspace={workspace} path={selected} /> : <Text c="dimmed">Select a file to view it.</Text>}
          </Paper>
        </Grid.Col>
      </Grid>
    </Stack>
  );
}
