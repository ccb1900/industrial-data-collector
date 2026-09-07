import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Wails serves the built assets; dev mode proxies to the Wails dev server.
export default defineConfig({
  plugins: [react()],
  build: { outDir: "dist", emptyOutDir: true },
});
