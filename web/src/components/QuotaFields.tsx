import { NumberInput, SimpleGrid } from "@mantine/core";
import { useState } from "react";
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

const completeNumber = /^\d+(\.\d+)?$/;

export function QuotaFields({ value, onChange }: { value: Quota; onChange: (q: Quota) => void }) {
  // NumberInput reports intermediate text such as "1." as a string. Keep it
  // as typed while the field is edited and pass on only complete numbers;
  // coercing "1." to 1 made the next keystrokes turn 1.25 into 125.
  const [drafts, setDrafts] = useState<Partial<Record<keyof Quota, string | number>>>({});
  return (
    <SimpleGrid cols={{ base: 1, sm: 2 }}>
      {fields.map((f) => (
        <NumberInput
          key={f.key}
          label={f.label}
          value={drafts[f.key] ?? value[f.key]}
          min={0}
          step={f.decimals ? 0.1 : 1}
          decimalScale={f.decimals ? 2 : 0}
          onChange={(v) => {
            setDrafts((d) => ({ ...d, [f.key]: v }));
            if (typeof v === "number") {
              onChange({ ...value, [f.key]: v });
            } else if (completeNumber.test(v)) {
              onChange({ ...value, [f.key]: Number(v) });
            }
          }}
          onBlur={() =>
            setDrafts((d) => {
              const rest = { ...d };
              delete rest[f.key];
              return rest;
            })
          }
        />
      ))}
    </SimpleGrid>
  );
}
