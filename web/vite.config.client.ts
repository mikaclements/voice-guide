import { resolve } from "node:path";
import { defineConfig } from "vite";

// The drop-in client: one module script with no React, which a product's page loads from voice,
// written where voice serves it from. Built as an app with one script for its entry, not as a
// library, so it is minified whitespace and all: every product page loads it.
export default defineConfig({
  publicDir: false,
  build: {
    outDir: resolve(import.meta.dirname, "dist-client"),
    emptyOutDir: true,
    modulePreload: false,
    rolldownOptions: {
      input: resolve(import.meta.dirname, "src/client/client.ts"),
      output: { format: "es", entryFileNames: "voice-guide.js" },
    },
  },
});
