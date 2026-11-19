import assert from "node:assert/strict";
import { test } from "node:test";
import { failure, ProblemError, ticketExpired } from "./api.ts";
import { httpTurns, startTimer } from "./turns.ts";

function clockAt(ms: number) {
  const c = { ms, now: () => c.ms };
  return c;
}

function sse(...chunks: (string | Uint8Array)[]): Response {
  const body = new ReadableStream({
    start(c) {
      for (const s of chunks) c.enqueue(typeof s === "string" ? new TextEncoder().encode(s) : s);
      c.close();
    },
  });
  return new Response(body, { headers: { "Content-Type": "text/event-stream" } });
}

const ticket = "tok";

test("a turn sends the words and its request ID with the ticket, and streams the reply", async () => {
  let sent: { url: string; init: RequestInit } | undefined;
  const fetcher = (async (url: string, init: RequestInit) => {
    sent = { url, init };
    return sse(
      'event: text\ndata: {"text":"Tell me"}\n\nevent: te',
      'xt\ndata: {"text":" more."}\n\n',
      'event: done\ndata: {"turn_id":"t9","seq":3,"model":"fake/claude-test"}\n\n',
    );
  }) as typeof fetch;
  const texts: string[] = [];
  const end = await httpTurns("s1", ticket, fetcher).take("Hi", "r1", (t) => texts.push(t));
  assert.deepEqual(texts, ["Tell me", " more."]);
  assert.deepEqual(end, { ended: "done", turnId: "t9", seq: 3, model: "fake/claude-test" });
  assert.ok(sent);
  assert.equal(sent.url, "/v1/sessions/s1/turns");
  assert.equal((sent.init.headers as Record<string, string>).Authorization, "Bearer tok");
  const body = JSON.parse(sent.init.body as string);
  assert.deepEqual(body, { request_id: "r1", words: "Hi" });
});

test("a character split between chunks arrives whole", async () => {
  const bytes = new TextEncoder().encode(
    'event: text\ndata: {"text":"Yes — go on."}\n\nevent: done\ndata: {}\n\n',
  );
  const cut = bytes.indexOf(0x80); // the middle of the dash's three bytes
  const fetcher = (async () => sse(bytes.slice(0, cut), bytes.slice(cut))) as typeof fetch;
  const texts: string[] = [];
  await httpTurns("s1", ticket, fetcher).take("Hi", "r1", (t) => texts.push(t));
  assert.deepEqual(texts, ["Yes — go on."]);
});

test("events the page doesn't know are skipped unread, and its own errors surface", async () => {
  const fetcher = (async () =>
    sse('event: ping\ndata: not json\n\nevent: text\ndata: {"text":"So"}\n\n')) as typeof fetch;
  const err = await httpTurns("s1", ticket, fetcher)
    .take("Hi", "r1", () => {
      throw new Error("a bug in the page");
    })
    .catch((e: unknown) => e);
  assert.ok(err instanceof Error);
  assert.equal(err.message, "a bug in the page");
});

test("an expired ticket says the session has expired, to start again", async () => {
  const fetcher = (async () =>
    Response.json(
      {
        type: "/v1/problems/ticket-expired",
        title: "Ticket expired",
        status: 401,
        detail: "the session's ticket has expired; start a new session",
      },
      { status: 401, headers: { "Content-Type": "application/problem+json" } },
    )) as typeof fetch;
  const err = await httpTurns("s1", ticket, fetcher)
    .take("Hi", "r1", () => {})
    .catch((e: unknown) => e);
  assert.ok(err instanceof ProblemError);
  assert.equal(err.message, "This session has expired; start again");
  assert.equal(ticketExpired(err), true);
  assert.equal(failure(err), "refused");
  // Any other 401 is a wrong credential, in its own words.
  const wrong = new ProblemError(401, "about:blank", "Unauthorized", "the ticket is invalid");
  assert.equal(ticketExpired(wrong), false);
  assert.equal(wrong.message, "Unauthorized (401): the ticket is invalid");
});

test("a lost reply ends lost, keeping the text so far", async () => {
  const fetcher = (async () =>
    sse(
      'event: text\ndata: {"text":"So"}\n\nevent: lost\ndata: {"turn_id":"t2","seq":2,"model":"claude-test"}\n\n',
    )) as typeof fetch;
  const texts: string[] = [];
  const end = await httpTurns("s1", ticket, fetcher).take("Hi", "r1", (t) => texts.push(t));
  assert.deepEqual(texts, ["So"]);
  assert.deepEqual(end, { ended: "lost", turnId: "t2", seq: 2, model: "claude-test" });
});

test("a stream cut before its final event ends lost", async () => {
  const fetcher = (async () => sse('event: text\ndata: {"text":"So"}\n\n')) as typeof fetch;
  assert.deepEqual(await httpTurns("s1", ticket, fetcher).take("Hi", "r1", () => {}), {
    ended: "lost",
  });
});

test("a stream that breaks mid-reply ends lost, keeping the text so far", async () => {
  let sent = false;
  const body = new ReadableStream({
    pull(c) {
      if (sent) return c.error(new TypeError("network error"));
      sent = true;
      c.enqueue(new TextEncoder().encode('event: text\ndata: {"text":"So"}\n\n'));
    },
  });
  const fetcher = (async () => new Response(body)) as typeof fetch;
  const texts: string[] = [];
  const end = await httpTurns("s1", ticket, fetcher).take("Hi", "r1", (t) => texts.push(t));
  assert.deepEqual(texts, ["So"]);
  assert.deepEqual(end, { ended: "lost" });
});

test("a refused turn throws its problem", async () => {
  const fetcher = (async () =>
    Response.json(
      {
        type: "about:blank",
        title: "Conflict",
        status: 409,
        detail: "the session is answering another turn",
      },
      { status: 409, headers: { "Content-Type": "application/problem+json" } },
    )) as typeof fetch;
  const err = await httpTurns("s1", ticket, fetcher)
    .take("Hi", "r1", () => {})
    .catch((e: unknown) => e);
  assert.ok(err instanceof ProblemError);
  assert.equal(err.status, 409);
  assert.equal(err.message, "Conflict (409): the session is answering another turn");
});

test("a turn's time is to its first text, and later text doesn't change it", () => {
  const clock = clockAt(100);
  const timer = startTimer(clock.now);
  clock.ms = 720;
  assert.equal(timer.text(), 620);
  clock.ms = 900;
  assert.equal(timer.text(), 620);
});
