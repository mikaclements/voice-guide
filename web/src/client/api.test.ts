import assert from "node:assert/strict";
import { test } from "node:test";
import {
  endSession,
  endsSession,
  failure,
  httpTurns,
  keepRequestId,
  listAgents,
  ProblemError,
  problemFrom,
  requestIdFor,
  startSession,
  startTimer,
  ticketExpired,
} from "./api.ts";

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

test("a failure is refused only when voice said so; a repeat it already took is taken", () => {
  const problem = (status: number, type = "about:blank") => new ProblemError(status, type, "T", "");
  const cases: [unknown, string][] = [
    [problem(400), "refused"],
    [problem(404), "refused"],
    [problem(422, "/v1/problems/request-reused"), "refused"],
    [problem(409), "busy"], // a turn in flight: this one only if this send was a retry
    [problem(409, "/v1/problems/turn-repeated"), "taken"],
    [problem(409, "/v1/problems/session-ended"), "refused"], // never taken, and never will be
    [problem(502), "unsure"],
    [problem(503), "unsure"],
    [new TypeError("Failed to fetch"), "unsure"],
  ];
  for (const [err, want] of cases) assert.equal(failure(err), want, String(err));
});

test("a send refused while another turn is in flight keeps its request ID only if it was a retry", () => {
  // A fresh send that voice turned away wasn't taken: the next send is a new request.
  assert.equal(keepRequestId("busy", false), false);
  // A retry may have met its own first try still in flight, so it stays the same request.
  assert.equal(keepRequestId("busy", true), true);
  assert.equal(keepRequestId("unsure", false), true);
  assert.equal(keepRequestId("refused", true), false);
  assert.equal(keepRequestId("taken", true), false);
});

test("sending the same again reuses the kept request ID; anything else gets a new one", () => {
  const kept = { key: "Hi", requestId: "r1" };
  assert.equal(requestIdFor("Hi", kept), "r1");
  assert.notEqual(requestIdFor("Hello", kept), "r1");
  assert.match(requestIdFor("Hi", undefined), /^[0-9a-f-]{36}$/);
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
