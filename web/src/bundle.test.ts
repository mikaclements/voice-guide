import assert from "node:assert/strict";
import { test } from "node:test";
import { build } from "vite";
import client from "../vite.config.client.ts";

// The script a product's page loads, as the client build makes it, kept in memory.
async function bundle(): Promise<string> {
  const out = await build({
    ...client,
    configFile: false,
    logLevel: "silent",
    build: { ...client.build, write: false },
  });
  const [first] = Array.isArray(out) ? out : [out];
  assert.ok("output" in first);
  return first.output[0].type === "chunk" ? first.output[0].code : "";
}

test("the element's script carries no React and nothing of a product key or a session's start", async () => {
  const code = await bundle();
  assert.ok(code.includes("voice-guide-session"));
  assert.doesNotMatch(code, /react/i);
  assert.doesNotMatch(code, /product/i, "no product routes or key handling");
  assert.doesNotMatch(code, /vgk_/, "no product key");
  assert.doesNotMatch(code, /\/v1\/agents/, "the agent list takes a product key");
  assert.doesNotMatch(code, /\/v1\/sessions[`"']/, "starting a session takes a product key");
});

test("the element's look takes only custom properties of its own, so the page's own can't leak in", async () => {
  const code = await bundle();
  const used = new Set(code.match(/var\(--[\w-]+/g));
  assert.ok(used.size > 0);
  for (const name of used) assert.match(name, /^var\(--voice-guide-/);
});

test("the script loaded twice on one page defines the element once", async () => {
  const code = await bundle();
  const defined: string[] = [];
  const registry = new Map<string, unknown>();
  Object.assign(globalThis, {
    HTMLElement: class {},
    customElements: {
      get: (name: string) => registry.get(name),
      define: (name: string, element: unknown) => {
        defined.push(name);
        registry.set(name, element);
      },
    },
  });
  // Two addresses, as two copies of the script would be; one address would be loaded once.
  for (const copy of ["a", "b"]) {
    await import(`data:text/javascript,${encodeURIComponent(`${code}\n//${copy}`)}`);
  }
  assert.deepEqual(defined, ["voice-guide-session"]);
});
