import { Alert, Badge, Button, Card, Code, Grid, Group, List, Loader, Stack, Table, Text, TextInput, Textarea, Title } from "@mantine/core";
import { useDebouncedValue } from "@mantine/hooks";
import { notifications } from "@mantine/notifications";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { api } from "../api";
import { ErrorAlert } from "../components/ErrorAlert";
import type { Template } from "../types";

interface Draft {
  name: string;
  description: string;
  host_pattern: string;
  networks: string;
  labels: string;
}

const emptyDraft: Draft = { name: "", description: "", host_pattern: "", networks: "", labels: "" };

/** Parses one key=value per line; the first = separates key and value. */
export function parseLabels(text: string): { labels: Record<string, string>; errors: string[] } {
  const labels: Record<string, string> = {};
  const errors: string[] = [];
  text.split("\n").forEach((raw, i) => {
    const line = raw.trim();
    if (line === "") return;
    const eq = line.indexOf("=");
    if (eq <= 0) {
      errors.push(`Labels line ${i + 1}: expected key=value`);
      return;
    }
    labels[line.slice(0, eq).trim()] = line.slice(eq + 1).trim();
  });
  return { labels, errors };
}

function toTemplate(d: Draft): Template {
  return {
    name: d.name.trim(),
    description: d.description.trim(),
    host_pattern: d.host_pattern.trim(),
    networks: d.networks.split(",").map((n) => n.trim()).filter(Boolean),
    labels: parseLabels(d.labels).labels,
  };
}

function fromTemplate(t: Template): Draft {
  return {
    name: t.name,
    description: t.description,
    host_pattern: t.host_pattern,
    networks: t.networks.join(", "),
    labels: Object.entries(t.labels)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
  };
}

const placeholders = "{{workspace}} {{app}} {{service}} {{id}} {{host}} {{port}}";

export function TemplateEditor() {
  const { id: idParam } = useParams();
  const id = idParam ? Number(idParam) : 0;
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const existing = useQuery({ queryKey: ["template", id], queryFn: () => api.template(id), enabled: id > 0 });
  const [draft, setDraft] = useState<Draft>(emptyDraft);
  const [loaded, setLoaded] = useState(id === 0);
  useEffect(() => {
    if (existing.data && !loaded) {
      setDraft(fromTemplate(existing.data));
      setLoaded(true);
    }
  }, [existing.data, loaded]);

  const [debounced] = useDebouncedValue(draft, 400);
  const labelErrors = parseLabels(draft.labels).errors;
  const preview = useQuery({
    queryKey: ["template-preview", id, debounced],
    queryFn: () => api.previewTemplate(id, toTemplate(debounced)),
    enabled: loaded && (debounced.name !== "" || debounced.host_pattern !== ""),
    placeholderData: keepPreviousData,
  });

  const save = useMutation({
    mutationFn: () => (id > 0 ? api.updateTemplate(id, toTemplate(draft)) : api.createTemplate(toTemplate(draft))),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["templates"] });
      queryClient.removeQueries({ queryKey: ["template", id] });
      notifications.show({ color: "green", message: `Template ${draft.name} saved` });
      navigate("/templates");
    },
  });

  if (id > 0 && existing.isPending) return <Loader />;
  if (existing.isError) return <ErrorAlert error={existing.error} />;
  const set = (k: keyof Draft) => (e: { currentTarget: { value: string } }) => setDraft({ ...draft, [k]: e.currentTarget.value });

  return (
    <Stack>
      <Title order={2}>{id > 0 ? `Template ${draft.name}` : "New template"}</Title>
      <Grid gap="lg">
        <Grid.Col span={{ base: 12, md: 7 }}>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              save.mutate();
            }}
          >
            <Stack>
              <ErrorAlert error={save.error} />
              <TextInput label="Name" value={draft.name} onChange={set("name")} required />
              <TextInput label="Description" value={draft.description} onChange={set("description")} />
              <TextInput
                label="Host pattern"
                description="Optional. Placeholders separated by - or ., must contain {{workspace}} and {{app}}; the domain suffix must not overlap other templates."
                placeholder="{{app}}.{{workspace}}.apps.example.com"
                value={draft.host_pattern}
                onChange={set("host_pattern")}
              />
              <TextInput label="Networks" description="Comma-separated; must exist in Docker" value={draft.networks} onChange={set("networks")} />
              <Textarea
                label="Labels"
                description={`One key=value per line. Placeholders: ${placeholders}`}
                value={draft.labels}
                onChange={set("labels")}
                autosize
                minRows={6}
                styles={{ input: { fontFamily: "var(--mantine-font-family-monospace)" } }}
                error={labelErrors.length > 0 ? labelErrors.join("; ") : undefined}
              />
              <Group>
                <Button type="submit" loading={save.isPending} disabled={labelErrors.length > 0}>
                  Save template
                </Button>
              </Group>
            </Stack>
          </form>
        </Grid.Col>
        <Grid.Col span={{ base: 12, md: 5 }}>
          <Card withBorder>
            <Stack gap="sm">
              <Group justify="space-between">
                <Title order={4}>Live preview</Title>
                {preview.isFetching && <Loader size="xs" />}
                {preview.data && (preview.data.valid ? <Badge color="green">valid</Badge> : <Badge color="red">invalid</Badge>)}
              </Group>
              <Text size="xs" c="dimmed">
                Sample: workspace alice, app shop, service web, port 8080
              </Text>
              <ErrorAlert error={preview.error} />
              {preview.data && preview.data.errors.length > 0 && (
                <Alert color="red" title="Validation errors">
                  <List size="sm">
                    {preview.data.errors.map((e) => (
                      <List.Item key={e}>{e}</List.Item>
                    ))}
                  </List>
                </Alert>
              )}
              {preview.data && (
                <>
                  <Text size="sm">
                    Host: {preview.data.host ? <Code>{preview.data.host}</Code> : <Text span c="dimmed">none</Text>}
                  </Text>
                  <Table withTableBorder fz="xs">
                    <Table.Tbody>
                      {preview.data.labels.map((l) => (
                        <Table.Tr key={l.key}>
                          <Table.Td>
                            <Code>{l.key}</Code>
                          </Table.Td>
                          <Table.Td>
                            <Code>{l.value}</Code>
                          </Table.Td>
                        </Table.Tr>
                      ))}
                    </Table.Tbody>
                  </Table>
                </>
              )}
              {!preview.data && !preview.isFetching && <Text c="dimmed">Start typing to see a preview.</Text>}
            </Stack>
          </Card>
        </Grid.Col>
      </Grid>
    </Stack>
  );
}
