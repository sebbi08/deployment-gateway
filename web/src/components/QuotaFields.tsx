import { NumberInput, SimpleGrid } from "@mantine/core";
import type { Quota } from "../types";

const fields: { key: keyof Quota; label: string; decimals?: boolean }[] = [
  { key: "max_apps", label: "Max apps" },
  { key: "max_services_per_app", label: "Max services per app" },
  { key: "max_total_memory_mb", label: "Total memory (MB)" },
  { key: "max_total_cpus", label: "Total CPUs", decimals: true },
  { key: "default_memory_mb", label: "Default memory per service (MB)" },
  { key: "max_memory_mb", label: "Max memory per service (MB)" },
  { key: "default_cpus", label: "Default CPUs per service", decimals: true },
  { key: "max_cpus", label: "Max CPUs per service", decimals: true },
  { key: "default_pids", label: "Default pids per service" },
  { key: "max_pids", label: "Max pids per service" },
];

export function QuotaFields({ value, onChange }: { value: Quota; onChange: (q: Quota) => void }) {
  return (
    <SimpleGrid cols={{ base: 1, sm: 2 }}>
      {fields.map((f) => (
        <NumberInput
          key={f.key}
          label={f.label}
          value={value[f.key]}
          min={0}
          step={f.decimals ? 0.1 : 1}
          decimalScale={f.decimals ? 2 : 0}
          onChange={(v) => onChange({ ...value, [f.key]: typeof v === "number" ? v : Number(v) || 0 })}
        />
      ))}
    </SimpleGrid>
  );
}
