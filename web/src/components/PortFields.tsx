import { Group, NumberInput, Stack, Text, TextInput } from "@mantine/core";
import type { PortSettings } from "../types";

export function PortFields({ value, onChange }: { value: PortSettings; onChange: (p: PortSettings) => void }) {
  return (
    <Stack gap={4}>
      <Group grow align="start">
        <NumberInput
          allowDecimal={false}
          label="Published ports from"
          value={value.start}
          min={0}
          max={65535}
          onChange={(v) => onChange({ ...value, start: Number(v) || 0 })}
        />
        <NumberInput allowDecimal={false} label="to" value={value.end} min={0} max={65535} onChange={(v) => onChange({ ...value, end: Number(v) || 0 })} />
        <TextInput label="Bind IP" value={value.bind_ip} onChange={(e) => onChange({ ...value, bind_ip: e.currentTarget.value })} />
      </Group>
      <Text size="xs" c="dimmed">
        0 to 0 disables publishing. Published ports bypass the host firewall (ufw); choose the bind IP deliberately.
      </Text>
    </Stack>
  );
}
