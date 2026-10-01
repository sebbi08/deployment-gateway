import { Alert } from "@mantine/core";
import { errorMessage } from "../api";

export function ErrorAlert({ error }: { error: unknown }) {
  if (!error) return null;
  return (
    <Alert color="red" title="Error" style={{ whiteSpace: "pre-wrap" }}>
      {errorMessage(error)}
    </Alert>
  );
}
