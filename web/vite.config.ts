import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// The build is written into the Go embed directory; .keep stays so that
// `go test ./...` works without a frontend build.
export default defineConfig({
  plugins: [react()],
  build: { outDir: "../internal/webui/dist", emptyOutDir: false, chunkSizeWarningLimit: 1500 },
  server: { proxy: { "/api": "http://localhost:8081" } },
  test: { environment: "jsdom", globals: true, setupFiles: ["./src/test/setup.ts"], css: false },
});
