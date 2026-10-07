import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockApi, renderWithProviders } from "../test/utils";
import { Layout } from "./Layout";

// On narrow screens the navbar used to cover all content with no way to
// close it; a burger toggles it and following a link closes it.
test("the burger toggles the navigation and a link closes it", async () => {
  mockApi({});
  renderWithProviders(
    <Layout me={{ actor: "admin", proxy_mode: false, agent_url: "", admin_url: "" }}>
      <p>content</p>
    </Layout>,
  );
  const burger = screen.getByRole("button", { name: "Toggle navigation" });
  expect(burger).toHaveAttribute("aria-expanded", "false");
  await userEvent.click(burger);
  expect(burger).toHaveAttribute("aria-expanded", "true");
  await userEvent.click(screen.getByRole("link", { name: "Workspaces" }));
  expect(burger).toHaveAttribute("aria-expanded", "false");
});
