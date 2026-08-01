import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

export default defineConfig({
  build: {
    outDir: "dist/client",
  },
  optimizeDeps: {
    include: ["svelte", "phosphor-svelte", "@tauri-apps/api/core"],
  },
  server: {
    host: "0.0.0.0",
    port: 1437,
    strictPort: true,
    allowedHosts: ["terminal.local"],
    warmup: {
      clientFiles: ["./src/main.ts"],
    },
  },
  plugins: [svelte()],
});
