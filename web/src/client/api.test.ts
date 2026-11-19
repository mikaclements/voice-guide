import assert from "node:assert/strict";
import { test } from "node:test";
import { at, endSession, endsSession, ProblemError, problemFrom } from "./api.ts";

const ticket = "tok";

test("a refused ticket is not replaced or sent again: the page has only the one", async () => {
  const sent: string[] = [];
  const fetcher = (async (_url: string, init: RequestInit) => {
    sent.push((init.headers as Record<string, string>).Authorization);
    return new Response("", { status: 401 });
  }) as typeof fetch;
  const err = await endSession(ticket, "s1", fetcher).catch((e: unknown) => e);
  assert.ok(err instanceof ProblemError);
  assert.equal(err.status, 401);
  assert.deepEqual(sent, ["Bearer tok"]);
});

test("every request in a session carries the same ticket", async () => {
  const sent: string[] = [];
  const fetcher = (async (_url: string, init: RequestInit) => {
    sent.push((init.headers as Record<string, string>).Authorization);
    return Response.json({ id: "s1", agent_id: "a1", agent_version: 1, started_at: "" });
  }) as typeof fetch;
  await endSession(ticket, "s1", fetcher);
  await endSession(ticket, "s1", fetcher);
  assert.deepEqual(sent, ["Bearer tok", "Bearer tok"]);
});

test("ending a session posts to its end with the ticket and returns it ended", async () => {
  let asked = "";
  const fetcher = (async (url: string, init: RequestInit) => {
    asked = `${init.method} ${url} ${(init.headers as Record<string, string>).Authorization}`;
    return Response.json({
      id: "s1",
      agent_id: "a1",
      agent_version: 3,
      started_at: "2026-11-18T09:00:00Z",
      ended_at: "2026-11-18T09:20:00Z",
    });
  }) as typeof fetch;
  const s = await endSession(ticket, "s1", fetcher);
  assert.equal(asked, "POST /v1/sessions/s1/end Bearer tok");
  assert.equal(s.ended_at, "2026-11-18T09:20:00Z");
});

test("only the session-ended problem says the session has ended", () => {
  assert.equal(endsSession(new ProblemError(409, "/v1/problems/session-ended", "T", "")), true);
  assert.equal(endsSession(new ProblemError(409, "about:blank", "T", "")), false);
  assert.equal(endsSession(new TypeError("Failed to fetch")), false);
});

test("a problem document from voice says so, and any other refusal doesn't", async () => {
  const fromVoice = await problemFrom(
    Response.json(
      { type: "/v1/problems/request-reused", title: "T", status: 422, detail: "d" },
      { status: 422, headers: { "Content-Type": "application/problem+json" } },
    ),
  );
  assert.equal(fromVoice.fromVoice, true);
  assert.equal(fromVoice.type, "/v1/problems/request-reused");
  // The dev server's own plain-text refusal, or a proxy's page: not voice's answer.
  const plain = await problemFrom(new Response("no such context", { status: 404 }));
  assert.equal(plain.fromVoice, false);
  const html = await problemFrom(
    new Response("<html>", { status: 502, headers: { "Content-Type": "text/html" } }),
  );
  assert.equal(html.fromVoice, false);
  assert.equal(html.message, "Error (502)", "a page's body is never shown");
  assert.match(plain.message, /no such context/);
});

test("every path the element sends goes to voice's base, the audio connection's own address too", async () => {
  const reached: string[] = [];
  const fetcher = at("https://voice.example", (async (url: string) => {
    reached.push(url);
    return new Response();
  }) as typeof fetch);
  // The address voice's answer to an offer names, which hanging up sends DELETE to.
  await fetcher("/v1/sessions/s1/audio/c1", { method: "DELETE" });
  assert.deepEqual(reached, ["https://voice.example/v1/sessions/s1/audio/c1"]);
});
