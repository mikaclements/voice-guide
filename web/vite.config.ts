import { defineConfig } from "vite";
import { devProduct, proxiedHosts, viteAllowedHosts, voiceAddress } from "./dev/product-server.ts";

export default defineConfig({
  plugins: [devProduct()],
  server: {
    // 127.0.0.1 only: the product routes start sessions for the placeholder user with the
    // development product key, so nothing else on the network may reach them directly. Another
    // device of the developer's reaches the page only through an HTTPS proxy on this machine, under
    // a name WEB_ALLOWED_HOSTS lists.
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
    // Besides this machine's own names, only those an HTTPS proxy on this machine serves the page
    // under (WEB_ALLOWED_HOSTS), for the developer's other devices; the routes check the same list.
    allowedHosts: viteAllowedHosts(proxiedHosts),
    // Vite would otherwise let any page served from this machine, on any port, start a session here
    // and take its ticket, on the developer's Claude login. This page needs no cross-origin reads,
    // since its own origin serves everything it calls.
    cors: false,
    // The API goes through the dev server, so the page and voice share an origin and voice needs
    // no cross-origin rules.
    proxy: { "/v1": `http://${voiceAddress}` },
  },
});
