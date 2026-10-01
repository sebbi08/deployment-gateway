import { Button, Group, TextInput } from "@mantine/core";
import { useState } from "react";

/** A destructive action that is only enabled after typing the exact name. */
export function ConfirmDelete({ name, label, loading, onConfirm }: {
  name: string;
  label: string;
  loading?: boolean;
  onConfirm: (confirm: string) => void;
}) {
  const [typed, setTyped] = useState("");
  return (
    <Group align="end">
      <TextInput label={`Type ${name} to confirm`} value={typed} onChange={(e) => setTyped(e.currentTarget.value)} />
      <Button color="red" disabled={typed !== name} loading={loading} onClick={() => onConfirm(typed)}>
        {label}
      </Button>
    </Group>
  );
}
