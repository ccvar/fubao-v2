import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import packageInfo from "./package.json" with { type: "json" };

export default defineConfig({
  define: {
    __APP_VERSION__: JSON.stringify(packageInfo.version),
  },
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
