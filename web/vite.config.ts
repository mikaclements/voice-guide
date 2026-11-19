import { defineConfig } from "vite";
import { devProduct } from "./dev/product.ts";

export default defineConfig({
  plugins: [devProduct()],
  server: {
    // 127.0.0.1 only: the product routes start sessions for the placeholder user with the
    // development product key, so nothing else on the network may reach them.
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
    // Vite would otherwise let any page served from this machine, on any port, start a session here
    // and take its ticket, on the developer's Claude login. This page needs no cross-origin reads,
    // since its own origin serves everything it calls.
    cors: false,
    // The API goes through the dev server, so the page and voice share an origin and voice needs
    // no cross-origin rules; VOICE_ADDR is where the other scripts find voice too.
    proxy: { "/v1": `http://${process.env.VOICE_ADDR ?? "127.0.0.1:8080"}` },
  },
});
