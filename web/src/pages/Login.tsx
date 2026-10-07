import { Button, Center, Paper, PasswordInput, Stack, Title } from "@mantine/core";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api";
import { ErrorAlert } from "../components/ErrorAlert";

/** Full-page login (no session yet). */
export function Login() {
  return (
    <Center h="100vh">
      <Paper withBorder shadow="sm" p="xl" w={380}>
        <LoginForm />
      </Paper>
    </Center>
  );
}

export function LoginForm() {
  const queryClient = useQueryClient();
  const [password, setPassword] = useState("");
  const login = useMutation({
    mutationFn: () => api.login(password),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["me"] }),
  });
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        login.mutate();
      }}
    >
      <Stack>
        <Title order={3}>deploy-gateway</Title>
        <ErrorAlert error={login.error} />
        <PasswordInput label="Admin password" value={password} onChange={(e) => setPassword(e.currentTarget.value)} required autoFocus />
        <Button type="submit" loading={login.isPending}>
          Log in
        </Button>
      </Stack>
    </form>
  );
}
