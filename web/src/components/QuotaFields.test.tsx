import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { quota, renderWithProviders } from "../test/utils";
import type { Quota } from "../types";
import { QuotaFields } from "./QuotaFields";

let latest: Quota;

function Harness() {
  const [q, setQ] = useState<Quota>({ ...quota, max_total_cpus: 1.5 });
  latest = q;
  return <QuotaFields value={q} onChange={setQ} />;
}

test("editing a decimal by backspacing keeps the intermediate text", async () => {
  renderWithProviders(<Harness />);
  const input = screen.getByLabelText("Total CPUs");
  await userEvent.click(input);
  await userEvent.keyboard("{End}{Backspace}25");
  expect(input).toHaveValue("1.25");
  expect(latest.max_total_cpus).toBe(1.25);
});
