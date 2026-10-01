import { Button, Center, Paper, PasswordInput, Stack, Title } from "@mantine/core";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api";
import { ErrorAlert } from "../components/ErrorAlert";

export function Login() {
  const queryClient = useQueryClient();
  const [password, setPassword] = useState("");
  const login = useMutation({
    mutationFn: () => api.login(password),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["me"] }),
  });
  return (
    <Center h="100vh">
      <Paper withBorder shadow="sm" p="xl" w={380}>
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
      </Paper>
    </Center>
  );
}
