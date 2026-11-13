import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockApi, renderWithProviders } from "../test/utils";
import { theme } from "../theme";
import { Layout } from "./Layout";

// jsdom (since v31) applies the media queries Mantine generates for
// hiddenFrom/visibleFrom and the AppShell against its fixed 1024x768 viewport,
// which cannot be resized. To render on a "narrow screen" the breakpoints are
// moved above that width instead, so the viewport is below every breakpoint.
const narrowScreenTheme = {
  ...theme,
  breakpoints: { xs: "70em", sm: "80em", md: "90em", lg: "100em", xl: "110em" },
};

// On narrow screens the navbar used to cover all content with no way to
// close it; a burger toggles it and following a link closes it.
test("the burger toggles the navigation and a link closes it", async () => {
  mockApi({});
  renderWithProviders(
    <Layout me={{ actor: "admin", proxy_mode: false, agent_url: "", admin_url: "" }}>
      <p>content</p>
    </Layout>,
    "/",
    narrowScreenTheme,
  );
  const burger = screen.getByRole("button", { name: "Toggle navigation" });
  expect(burger).toHaveAttribute("aria-expanded", "false");
  await userEvent.click(burger);
  expect(burger).toHaveAttribute("aria-expanded", "true");
  await userEvent.click(screen.getByRole("link", { name: "Workspaces" }));
  expect(burger).toHaveAttribute("aria-expanded", "false");
});
