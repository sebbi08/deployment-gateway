import { ApiError, api, errorMessage } from "./api";
import { mockApi } from "./test/utils";

test("ApiError carries code, fields and detail", async () => {
  mockApi({
    "POST /workspaces": {
      status: 422,
      body: { code: "validation_failed", message: "stack validation failed", errors: [{ field: "name", message: "too short" }], detail: "x" },
    },
  });
  const err = await api.createWorkspace({ name: "a", quota: {} as never, ports: { start: 0, end: 0, bind_ip: "0.0.0.0" } }).catch((e) => e);
  expect(err).toBeInstanceOf(ApiError);
  expect(err.status).toBe(422);
  expect(err.code).toBe("validation_failed");
  expect(errorMessage(err)).toBe("stack validation failed\nname: too short\nx");
});

test("204 resolves to undefined and audit builds a query string", async () => {
  const calls = mockApi({ "POST /logout": { status: 204 }, "GET /audit": { body: { entries: [], next_before: 0 } } });
  await expect(api.logout()).resolves.toBeUndefined();
  await api.audit({ workspace: "alice", severity: "", before: 0 });
  expect(calls[1].search).toBe("?workspace=alice");
});
