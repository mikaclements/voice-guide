import { defineConfig } from "vite";

// The hand-written schematic and the class views generated beside it live in data/, served as
// they are, so scripts/schematic.sh can regenerate the class views without a rebuild. Port 5180
// stays clear of the page's dev server (5173), voice (8080) and the model service (8091).
export default defineConfig({
  publicDir: "data",
  server: { host: "127.0.0.1", port: 5180, strictPort: true },
  preview: { host: "127.0.0.1", port: 5180, strictPort: true },
  build: {
    // One page with React Flow and ELK's layout engine in it, served only on this machine.
    chunkSizeWarningLimit: 2500,
    rollupOptions: {
      // React Flow marks its modules "use client", a directive for React's server components,
      // which mean nothing in a page that runs only in the browser.
      onwarn(warning, warn) {
        if (warning.code !== "MODULE_LEVEL_DIRECTIVE") warn(warning);
      },
    },
  },
});
