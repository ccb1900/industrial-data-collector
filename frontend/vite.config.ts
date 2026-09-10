import { defineConfig } from "vite";
import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";

// Wails serves the built assets; dev mode proxies to the Wails dev server.
import path from "node:path";

export default defineConfig({
  resolve: {
    alias: {
      "@gocordis/console-client": path.resolve(
        path.dirname(fileURLToPath(import.meta.url)),
        "../../go-cordis/client/src/index.ts"
      ),
    },
  },
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true },
});
