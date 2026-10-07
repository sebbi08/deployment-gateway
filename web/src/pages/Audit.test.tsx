import { screen } from "@testing-library/react";
import { Profiler } from "react";
import { mockApi, renderWithProviders } from "../test/utils";
import { Audit } from "./Audit";

// The debounced filter used to get a new object on every render, so the
// page re-rendered every 300 ms for as long as it was open.
test("the audit page settles after loading", async () => {
  mockApi({ "GET /audit": { body: { entries: [] } } });
  let renders = 0;
  renderWithProviders(
    <Profiler id="audit" onRender={() => renders++}>
      <Audit />
    </Profiler>,
  );
  await screen.findByText("Audit log");
  await new Promise((r) => setTimeout(r, 500));
  const settled = renders;
  await new Promise((r) => setTimeout(r, 1000));
  expect(renders - settled).toBeLessThanOrEqual(1);
});
