import { ActionIcon, AppShell, Burger, Button, Group, NavLink, Text, Title, useComputedColorScheme, useMantineColorScheme } from "@mantine/core";
import { useDisclosure } from "@mantine/hooks";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { Link, useLocation } from "react-router";
import { api } from "../api";
import type { Me } from "../types";

const links = [
  { to: "/", label: "Dashboard" },
  { to: "/workspaces", label: "Workspaces" },
  { to: "/templates", label: "Templates" },
  { to: "/audit", label: "Audit log" },
  { to: "/settings", label: "Settings" },
  { to: "/orphans", label: "Orphans" },
];

export function Layout({ me, children }: { me: Me; children: ReactNode }) {
  const location = useLocation();
  // Below the sm breakpoint the navbar is an overlay; it starts closed.
  const [navOpen, nav] = useDisclosure(false);
  const queryClient = useQueryClient();
  const { setColorScheme } = useMantineColorScheme();
  const scheme = useComputedColorScheme("light");
  const logout = useMutation({ mutationFn: api.logout, onSuccess: () => {
      queryClient.removeQueries({ predicate: (q) => q.queryKey[0] !== "me" });
      return queryClient.resetQueries({ queryKey: ["me"] });
    },
  });

  return (
    <AppShell header={{ height: 56 }} navbar={{ width: 220, breakpoint: "sm", collapsed: { mobile: !navOpen } }} padding="lg">
      <AppShell.Header>
        <Group h="100%" px="md" justify="space-between" wrap="nowrap">
          <Group gap="sm" wrap="nowrap">
            <Burger opened={navOpen} onClick={nav.toggle} hiddenFrom="sm" size="sm" aria-label="Toggle navigation" aria-expanded={navOpen} />
            <Title order={4}>deploy-gateway</Title>
          </Group>
          <Group gap="sm" wrap="nowrap">
            <Text size="sm" c="dimmed" visibleFrom="sm">
              {me.actor}
            </Text>
            <ActionIcon
              variant="default"
              aria-label="Toggle color scheme"
              onClick={() => setColorScheme(scheme === "dark" ? "light" : "dark")}
            >
              {scheme === "dark" ? "☀" : "☾"}
            </ActionIcon>
            {!me.proxy_mode && (
              <Button variant="default" size="xs" onClick={() => logout.mutate()} loading={logout.isPending}>
                Log out
              </Button>
            )}
          </Group>
        </Group>
      </AppShell.Header>
      <AppShell.Navbar p="xs">
        {links.map((l) => (
          <NavLink
            key={l.to}
            component={Link}
            to={l.to}
            label={l.label}
            active={l.to === "/" ? location.pathname === "/" : location.pathname.startsWith(l.to)}
            onClick={nav.close}
          />
        ))}
      </AppShell.Navbar>
      <AppShell.Main>{children}</AppShell.Main>
    </AppShell>
  );
}
