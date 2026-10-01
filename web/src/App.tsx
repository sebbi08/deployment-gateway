import { Center, Loader, Text } from "@mantine/core";
import { QueryCache, QueryClient, useQuery } from "@tanstack/react-query";
import { Route, Routes } from "react-router";
import { ApiError, api } from "./api";
import { ErrorAlert } from "./components/ErrorAlert";
import { Layout } from "./components/Layout";
import { Dashboard } from "./pages/Dashboard";
import { Login } from "./pages/Login";

/** A session that expires while the app is open sends the user back to the login form. */
export function createQueryClient(opts: { retry?: boolean } = {}): QueryClient {
  const client: QueryClient = new QueryClient({
    defaultOptions: { queries: { retry: opts.retry === false ? false : 1 }, mutations: { retry: false } },
    queryCache: new QueryCache({
      onError: (error, query) => {
        if (query.queryKey[0] !== "me" && error instanceof ApiError && error.code === "login_required") {
          client.invalidateQueries({ queryKey: ["me"] });
        }
      },
    }),
  });
  return client;
}

export function App() {
  const me = useQuery({ queryKey: ["me"], queryFn: api.me, retry: false });
  if (me.isPending) {
    return (
      <Center h="100vh">
        <Loader />
      </Center>
    );
  }
  if (me.isError) {
    if (me.error instanceof ApiError && me.error.code === "login_required") {
      return <Login />;
    }
    return (
      <Center h="100vh" p="xl">
        <ErrorAlert error={me.error} />
      </Center>
    );
  }
  return (
    <Layout me={me.data}>
      <Routes>
        <Route path="/" element={<Dashboard />} />
        <Route path="*" element={<Text>Page not found.</Text>} />
      </Routes>
    </Layout>
  );
}
