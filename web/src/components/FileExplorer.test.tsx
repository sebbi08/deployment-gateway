import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { mockApi, renderWithProviders } from "../test/utils";
import type { FileContent, FileListing } from "../types";
import { FileExplorer } from "./FileExplorer";

const t = "2026-11-10T14:02:00Z";

const listings: Record<string, FileListing> = {
  "": { path: "", more: 0, entries: [{ name: "shop", type: "dir", size: 4096, mod_time: t }] },
  shop: {
    path: "shop",
    more: 0,
    entries: [
      { name: "data", type: "dir", size: 4096, mod_time: t },
      { name: "docker-compose.yml", type: "file", size: 1234, mod_time: t },
    ],
  },
  "shop/data": {
    path: "shop/data",
    more: 412,
    entries: [
      { name: "evil", type: "symlink", size: 11, mod_time: t, target: "/etc/shadow" },
      { name: "pipe", type: "fifo", size: 0, mod_time: t },
      { name: "db.bin", type: "file", size: 3_400_000, mod_time: t },
      { name: "big.log", type: "file", size: 9_000_000, mod_time: t },
    ],
  },
};

const contents: Record<string, FileContent> = {
  "shop/docker-compose.yml": {
    path: "shop/docker-compose.yml", size: 1234, mod_time: t, binary: false, truncated: false,
    text: "services:\n  web:\n    image: ghcr.io/alice/shop:1.4.2\n",
  },
  "shop/data/db.bin": { path: "shop/data/db.bin", size: 3_400_000, mod_time: t, binary: true, truncated: false, text: "" },
  "shop/data/big.log": { path: "shop/data/big.log", size: 9_000_000, mod_time: t, binary: false, truncated: true, text: "line 1\n" },
};

function setup() {
  const calls = mockApi({
    "GET /workspaces/alice/files": (_b, url) => ({ body: listings[url.searchParams.get("path") ?? ""] }),
    "GET /workspaces/alice/files/content": (_b, url) => ({ body: contents[url.searchParams.get("path") ?? ""] }),
  });
  renderWithProviders(<FileExplorer workspace="alice" />);
  return calls;
}

async function openShopData() {
  await userEvent.click(await screen.findByRole("button", { name: /shop/ }));
  await userEvent.click(await screen.findByRole("button", { name: /^data/ }));
}

test("expanding folders and opening a compose file shows its content", async () => {
  setup();
  expect(screen.getByText("Select a file to view it.")).toBeInTheDocument();
  await userEvent.click(await screen.findByRole("button", { name: /shop/ }));
  await userEvent.click(await screen.findByRole("button", { name: /docker-compose\.yml/ }));
  expect(await screen.findByText(/image: ghcr\.io\/alice\/shop:1\.4\.2/)).toBeInTheDocument();
  expect(screen.getByTestId("file-viewer-path")).toHaveTextContent("shop/docker-compose.yml");
});

test("symlinks and special files are listed but cannot be opened", async () => {
  const calls = setup();
  await openShopData();
  expect(await screen.findByText("/etc/shadow")).toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /evil/ })).not.toBeInTheDocument();
  expect(screen.queryByRole("button", { name: /pipe/ })).not.toBeInTheDocument();
  expect(screen.getByText("fifo")).toBeInTheDocument();
  expect(calls.some((c) => c.path.endsWith("/content"))).toBe(false);
});

test("folders with too many entries say how many are hidden", async () => {
  setup();
  await openShopData();
  expect(await screen.findByText("+412 more not shown")).toBeInTheDocument();
});

test("binary files show only their size", async () => {
  setup();
  await openShopData();
  await userEvent.click(await screen.findByRole("button", { name: /db\.bin/ }));
  expect(await screen.findByText("Binary file, 3.2 MB. Content is not shown.")).toBeInTheDocument();
});

test("truncated files say so", async () => {
  setup();
  await openShopData();
  await userEvent.click(await screen.findByRole("button", { name: /big\.log/ }));
  expect(await screen.findByText(/Showing the first 512 KB of 8\.6 MB/)).toBeInTheDocument();
  expect(screen.getByText(/line 1/)).toBeInTheDocument();
});
