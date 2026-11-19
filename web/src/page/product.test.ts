import assert from "node:assert/strict";
import { test } from "node:test";
import { listAgents, startSession } from "./product.ts";

test("listing agents asks the product's server, which holds the key", async () => {
  let asked: { url: string; init: RequestInit | undefined } | undefined;
  const fetcher = (async (url: string, init?: RequestInit) => {
    asked = { url, init };
    return Response.json({ agents: [{ id: "a1" }] });
  }) as typeof fetch;
  assert.deepEqual(await listAgents(fetcher), [{ id: "a1" }]);
  assert.equal(asked?.url, "/product/agents");
  assert.equal(asked?.init?.headers, undefined, "the page sends no credential of its own");
});

test("starting a session asks the product's server, which sends voice the user and the context", async () => {
  let asked: { url: string; method?: string; body: unknown } | undefined;
  const fetcher = (async (url: string, init: RequestInit) => {
    asked = { url, method: init.method, body: JSON.parse(init.body as string) };
    return Response.json(
      { id: "s1", agent_id: "a1", agent_version: 3, started_at: "", ticket: "tk" },
      { status: 201 },
    );
  }) as typeof fetch;
  const s = await startSession("a1", "r1", fetcher);
  assert.equal(s.id, "s1");
  assert.equal(s.ticket, "tk");
  assert.deepEqual(asked, {
    url: "/product/start",
    method: "POST",
    body: { agent_id: "a1", request_id: "r1" },
  });
});
