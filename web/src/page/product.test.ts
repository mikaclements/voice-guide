import assert from "node:assert/strict";
import { test } from "node:test";
import { contextFor, listAgents, listSessions, listVoices, startSession } from "./product.ts";

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

test("a start carries the voice, speed and documents the page chose, and none when it chose none", async () => {
  const bodies: unknown[] = [];
  const fetcher = (async (_url: string, init: RequestInit) => {
    bodies.push(JSON.parse(init.body as string));
    return Response.json({ id: "s1", ticket: "tk" });
  }) as typeof fetch;
  await startSession("a1", "r1", fetcher, {
    voice: "ja_jp",
    speed: 1.5,
    session_context: [{ name: "log", content: "x" }],
  });
  await startSession("a1", "r2", fetcher, {});
  assert.deepEqual(bodies, [
    {
      agent_id: "a1",
      request_id: "r1",
      voice: "ja_jp",
      speed: 1.5,
      session_context: [{ name: "log", content: "x" }],
    },
    { agent_id: "a1", request_id: "r2" },
  ]);
});

test("the voices, the agent's documents and the past sessions are asked of the product's server", async () => {
  const urls: string[] = [];
  const fetcher = (async (url: string) => {
    urls.push(url);
    if (url.endsWith("/voices")) return Response.json({ voices: ["en_us"] });
    if (url.endsWith("/sessions")) return Response.json({ sessions: [{ id: "s1" }] });
    return Response.json({ session_context: [{ name: "log", content: "x" }] });
  }) as typeof fetch;
  assert.deepEqual(await listVoices(fetcher), ["en_us"]);
  assert.deepEqual(await listSessions(fetcher), [{ id: "s1" }]);
  assert.deepEqual(await contextFor("a 1", fetcher), [{ name: "log", content: "x" }]);
  assert.deepEqual(urls, ["/product/voices", "/product/sessions", "/product/context?agent=a%201"]);
});

test("a problem from the product's server is thrown as voice's would be", async () => {
  const fetcher = (async () =>
    Response.json(
      {
        type: "/v1/problems/session-context-mismatch",
        title: "Unprocessable",
        detail: "log is empty",
      },
      { status: 422, headers: { "Content-Type": "application/problem+json" } },
    )) as typeof fetch;
  await assert.rejects(startSession("a1", "r1", fetcher), /Unprocessable \(422\): log is empty/);
  await assert.rejects(listSessions(fetcher), /422/);
});
