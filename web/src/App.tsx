import { Center, Loader, Modal, Text } from "@mantine/core";
import { MutationCache, QueryCache, QueryClient, useQuery } from "@tanstack/react-query";
import { Route, Routes } from "react-router";
import { ApiError, api } from "./api";
import { ErrorAlert } from "./components/ErrorAlert";
import { Layout } from "./components/Layout";
import { Audit } from "./pages/Audit";
import { Dashboard } from "./pages/Dashboard";
import { Login, LoginForm } from "./pages/Login";
import { Orphans } from "./pages/Orphans";
import { Settings } from "./pages/Settings";
import { TemplateEditor } from "./pages/TemplateEditor";
import { Templates } from "./pages/Templates";
import { WorkspaceDetail } from "./pages/WorkspaceDetail";
import { Workspaces } from "./pages/Workspaces";

/** A session that expires while the app is open sends the user back to the login form. */
export function createQueryClient(opts: { retry?: boolean } = {}): QueryClient {
  const client: QueryClient = new QueryClient({
    defaultOptions: { queries: { retry: opts.retry === false ? false : 1 }, mutations: { retry: false } },
    queryCache: new QueryCache({
      onError: (error, query) => {
        if (query.queryKey[0] !== "me" && isLoginRequired(error)) {
          client.invalidateQueries({ queryKey: ["me"] });
        }
      },
    }),
    // A save that hits an expired session asks for a login too.
    mutationCache: new MutationCache({
      onError: (error) => {
        if (isLoginRequired(error)) {
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
  // The session expired while pages were open: log in again over them, so
  // unsaved drafts survive.
  const relogin = me.isError && isLoginRequired(me.error) && me.data !== undefined;
  if (me.isError && !relogin) {
    if (isLoginRequired(me.error)) {
      return <Login />;
    }
    return (
      <Center h="100vh" p="xl">
        <ErrorAlert error={me.error} />
      </Center>
    );
  }
  return (
    <>
      <Modal opened={relogin} onClose={() => {}} withCloseButton={false} closeOnClickOutside={false} closeOnEscape={false} centered>
        <LoginForm />
      </Modal>
      <Layout me={me.data!}>
      <Routes>
        <Route path="/" element={<Dashboard />} />
        <Route path="/workspaces" element={<Workspaces />} />
        <Route path="/workspaces/:name" element={<WorkspaceDetail />} />
        <Route path="/templates" element={<Templates />} />
        <Route path="/templates/new" element={<TemplateEditor />} />
        <Route path="/templates/:id" element={<TemplateEditor />} />
        <Route path="/audit" element={<Audit />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="/orphans" element={<Orphans />} />
        <Route path="*" element={<Text>Page not found.</Text>} />
      </Routes>
      </Layout>
    </>
  );
}

function isLoginRequired(error: unknown): boolean {
  return error instanceof ApiError && error.code === "login_required";
}
