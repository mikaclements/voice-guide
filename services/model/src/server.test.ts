import assert from "node:assert/strict";
import { EventEmitter, once } from "node:events";
import type { Server } from "node:http";
import type { AddressInfo } from "node:net";
import { type TestContext, test } from "node:test";
import { type Provider, ProviderError, type ProviderEvent, type ReplyRequest } from "./api.ts";
import { createServer, type ServerOptions, shutdown, writeEvent } from "./server.ts";

const validBody = {
  model: "test-model",
  session_id: "s1",
  stable_layers: ["You are an agent."],
  conversation: [{ role: "user", text: "Hi" }],
  new_words: "Ask me something.",
};

const scripted = (events: ProviderEvent[], seen: ReplyRequest[] = []): Provider => ({
  name: "scripted",
  async *reply(request) {
    seen.push(request);
    yield* events;
  },
});

async function withServer(
  provider: Provider,
  run: (url: string, server: Server) => Promise<void>,
  options: ServerOptions = {},
) {
  const server = createServer(provider, { log: () => {}, ...options });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  try {
    await run(`http://127.0.0.1:${(server.address() as AddressInfo).port}`, server);
  } finally {
    server.closeAllConnections();
    await new Promise((resolve) => server.close(resolve));
  }
}

const post = (url: string, body: unknown, signal?: AbortSignal) =>
  fetch(`${url}/v1/replies`, {
    method: "POST",
    body: typeof body === "string" ? body : JSON.stringify(body),
    signal,
  });

const lines = async (res: Response) =>
  (await res.text())
    .split("\n")
    .filter(Boolean)
    .map((l) => JSON.parse(l));

test("streams text in order, then done with usage and timing", async () => {
  const usage = { inputTokens: 10, cachedInputTokens: 8, cacheWriteTokens: 0, outputTokens: 3 };
  const clock = [100, 140, 190];
  const seen: ReplyRequest[] = [];
  const events: ProviderEvent[] = [
    { type: "text", text: "Tell me " },
    { type: "text", text: "about it." },
    { type: "usage", usage },
  ];
  await withServer(
    scripted(events, seen),
    async (url) => {
      const res = await post(url, validBody);
      assert.equal(res.headers.get("content-type"), "application/x-ndjson");
      assert.deepEqual(await lines(res), [
        events[0],
        events[1],
        {
          type: "done",
          provider: "scripted",
          model: "test-model",
          usage: {
            input_tokens: 10,
            cached_input_tokens: 8,
            cache_write_tokens: 0,
            output_tokens: 3,
          },
          timing: { first_text_ms: 40, total_ms: 90 },
        },
      ]);
      assert.equal(seen[0]?.model, "test-model");
      assert.equal(seen[0]?.newWords, "Ask me something.");
    },
    { now: () => clock.shift() ?? 190 },
  );
});

test("done names the model the provider says answered, not only the one requested", async () => {
  const usage = { inputTokens: 1, cachedInputTokens: 0, cacheWriteTokens: 0, outputTokens: 1 };
  const answered = scripted([
    { type: "text", text: "Hi." },
    { type: "usage", usage, model: "test-model-20260101" },
  ]);
  await withServer(answered, async (url) => {
    const done = (await lines(await post(url, validBody))).at(-1);
    assert.equal(done.type, "done");
    assert.equal(done.model, "test-model-20260101");
  });
});

test("a provider that reports no usage ends with usage null, not zeros", async () => {
  await withServer(scripted([{ type: "text", text: "Hi." }]), async (url) => {
    const done = (await lines(await post(url, validBody))).at(-1);
    assert.equal(done.type, "done");
    assert.equal(done.usage, null);
  });
});

test("requests the API can't serve get problems", async () => {
  await withServer(scripted([]), async (url) => {
    const cases: [Promise<Response>, number][] = [
      [post(url, "{not json"), 400],
      [post(url, { ...validBody, model: "" }), 400],
      [post(url, { ...validBody, session_id: "" }), 400],
      [post(url, { ...validBody, conversation: [{ role: "bot", text: "x" }] }), 400],
      [fetch(`${url}/v1/nothing`), 404],
      [fetch(`${url}/v1/replies`), 405],
    ];
    for (const [response, status] of cases) {
      const res = await response;
      assert.equal(res.headers.get("content-type"), "application/problem+json");
      const problem = await res.json();
      assert.equal(problem.status, status);
      if (status === 400) assert.equal(typeof problem.detail, "string");
    }
  });
});

test("a provider failure ends the stream with an error naming its kind, never its message", async () => {
  for (const [thrown, kind] of [
    [new ProviderError("overloaded", "529 from upstream: user said hello"), "overloaded"],
    [new Error("exited with code 1. stderr: user said hello"), "unknown"],
  ] as const) {
    const failing: Provider = {
      name: "failing",
      async *reply() {
        yield { type: "text", text: "Half" };
        throw thrown;
      },
    };
    const logged: Record<string, unknown>[] = [];
    await withServer(
      failing,
      async (url) => {
        const events = await lines(await post(url, validBody));
        assert.deepEqual(events.at(-1), { type: "error", kind });
      },
      { log: (record) => logged.push(record) },
    );
    assert.equal(logged[0]?.kind, kind);
    assert.ok(!JSON.stringify(logged).includes("user said hello"));
  }
});

// A provider that sends one word, then waits until it is cancelled.
function waiting() {
  const { promise: cancelled, resolve } = Promise.withResolvers<void>();
  const provider: Provider = {
    name: "waiting",
    async *reply(_request, signal) {
      yield { type: "text", text: "Thinking" };
      if (!signal.aborted) await new Promise((done) => signal.addEventListener("abort", done));
      resolve();
    },
  };
  return { provider, cancelled };
}

// A deadline that never passes, so only the caller going away can end a reply.
const noDeadline = { deadline: () => new AbortController().signal };

// Bounds a failure only: a cancel arrives within milliseconds, so a missing one fails the test
// instead of hanging the run. A round guess, far above the few milliseconds a passing run takes.
const cancelWithin = { timeout: 5_000 };

// Starts a reply, reads its first word and hangs up; returns once the provider is cancelled. A test
// that timed out has already failed; its signal only lets the server close so the run ends.
async function hangUpMidReply(t: TestContext, options: ServerOptions = {}) {
  const { provider, cancelled } = waiting();
  await withServer(
    provider,
    async (url) => {
      const caller = new AbortController();
      await (await post(url, validBody, caller.signal)).body?.getReader().read();
      caller.abort();
      await Promise.race([cancelled, once(t.signal, "abort")]);
    },
    { ...noDeadline, ...options },
  );
}

test("a caller that disconnects cancels the provider", cancelWithin, async (t) => {
  await hangUpMidReply(t);
});

test(
  "a reply whose caller went away is logged as cancelled, with its timing only",
  cancelWithin,
  async (t) => {
    const clock = [100, 140, 190];
    const logged: Record<string, unknown>[] = [];
    await hangUpMidReply(t, {
      now: () => clock.shift() ?? 190,
      log: (record) => logged.push(record),
    });
    assert.deepEqual(logged, [
      {
        msg: "reply cancelled",
        provider: "waiting",
        model: "test-model",
        first_text_ms: 40,
        total_ms: 90,
      },
    ]);
  },
);

// A deadline the test fires by hand, so no test waits on the clock.
function handDeadline() {
  const passes = new AbortController();
  return { fire: () => passes.abort(), options: { deadline: () => passes.signal } };
}

const tick = () => new Promise((resolve) => setImmediate(resolve));

test("a reply past its deadline is cancelled and ends as unavailable", async () => {
  const { provider, cancelled } = waiting();
  const deadline = handDeadline();
  await withServer(
    provider,
    async (url) => {
      const res = await post(url, validBody);
      await tick();
      deadline.fire();
      assert.deepEqual((await lines(res)).at(-1), { type: "error", kind: "unavailable" });
      await cancelled;
    },
    deadline.options,
  );
});

test("past its deadline, a provider that ignores it is cut off: nothing it sends later arrives", async () => {
  const { promise: yielded, resolve: hasYielded } = Promise.withResolvers<void>();
  const { promise: released, resolve: release } = Promise.withResolvers<void>();
  const deaf: Provider = {
    name: "deaf",
    async *reply() {
      yield { type: "text", text: "Early " };
      hasYielded();
      await released;
      yield { type: "text", text: "late" };
      yield {
        type: "usage",
        usage: { inputTokens: 1, cachedInputTokens: 0, cacheWriteTokens: 0, outputTokens: 1 },
      };
    },
  };
  const deadline = handDeadline();
  await withServer(
    deaf,
    async (url) => {
      const res = await post(url, validBody);
      await yielded;
      deadline.fire();
      release();
      assert.deepEqual(await lines(res), [
        { type: "text", text: "Early " },
        { type: "error", kind: "unavailable" },
      ]);
    },
    deadline.options,
  );
});

test("a slow caller past the deadline gets exactly one final event", async () => {
  const big = "x".repeat(4 << 20);
  const { promise: started, resolve: start } = Promise.withResolvers<void>();
  const flooding: Provider = {
    name: "flooding",
    async *reply() {
      start();
      yield { type: "text", text: big };
      yield {
        type: "usage",
        usage: { inputTokens: 1, cachedInputTokens: 0, cacheWriteTokens: 0, outputTokens: 1 },
      };
    },
  };
  const deadline = handDeadline();
  await withServer(
    flooding,
    async (url) => {
      const res = await post(url, validBody);
      await started;
      await tick();
      deadline.fire();
      const finals = (await lines(res)).filter((e) => e.type !== "text");
      assert.deepEqual(finals, [{ type: "error", kind: "unavailable" }]);
    },
    deadline.options,
  );
});

test("a failure's detail from the provider is logged with its kind", async () => {
  const detail = { error_class: "process_exited_nonzero", exit_code: 1 };
  const failing: Provider = {
    name: "failing",
    async *reply() {
      yield { type: "text", text: "Half" };
      throw new ProviderError("unavailable", "exited with code 1. stderr: user said hello", detail);
    },
  };
  const logged: Record<string, unknown>[] = [];
  await withServer(failing, async (url) => void (await lines(await post(url, validBody))), {
    log: (record) => logged.push(record),
  });
  assert.deepEqual(logged, [
    { msg: "reply failed", provider: "failing", model: "test-model", kind: "unavailable", detail },
  ]);
});

test("an unexpected failure answers 500 and logs only the error's name", async () => {
  const logged: Record<string, unknown>[] = [];
  await withServer(
    scripted([]),
    async (url) => {
      const res = await post(url, validBody);
      assert.equal(res.status, 500);
      assert.equal(res.headers.get("content-type"), "application/problem+json");
    },
    {
      deadline: () => {
        throw new TypeError("user said hello");
      },
      log: (record) => logged.push(record),
    },
  );
  assert.deepEqual(logged, [{ msg: "request failed", error: "TypeError" }]);
});

test("replies beyond the concurrency cap are refused with a 503 problem", async () => {
  const { provider } = waiting();
  await withServer(
    provider,
    async (url) => {
      const first = new AbortController();
      await (await post(url, validBody, first.signal)).body?.getReader().read();
      const res = await post(url, validBody);
      assert.equal(res.status, 503);
      assert.equal(res.headers.get("content-type"), "application/problem+json");
      first.abort();
    },
    { maxConcurrentReplies: 1 },
  );
});

test("asks outside any session run one at a time, and live replies keep the other places", async () => {
  const { provider } = waiting();
  const { session_id: _, ...background } = validBody;
  await withServer(
    provider,
    async (url) => {
      const first = new AbortController();
      await (await post(url, background, first.signal)).body?.getReader().read();
      const refused = await post(url, background);
      assert.equal(refused.status, 503, "a second ask outside any session");
      assert.equal(
        (await refused.json()).type,
        "/v1/problems/at-capacity",
        "told apart from an overloaded provider",
      );
      const live = new AbortController();
      const res = await post(url, validBody, live.signal);
      assert.equal(res.status, 200, "a live reply still gets a place");
      live.abort();
      first.abort();
    },
    { maxConcurrentReplies: 3 },
  );
});

test("the background place frees once its ask fails or is cancelled", async () => {
  const { session_id: _, ...background } = validBody;
  const failing: Provider = {
    name: "failing",
    async *reply() {
      yield { type: "text", text: "Half" };
      throw new ProviderError("overloaded", "the model is busy");
    },
  };
  await withServer(failing, async (url) => {
    await (await post(url, background)).text();
    const res = await post(url, background);
    assert.equal(res.status, 200, "after a failed ask");
    await res.text();
  });
  const { provider } = waiting();
  await withServer(
    provider,
    async (url) => {
      const first = new AbortController();
      await (await post(url, background, first.signal)).body?.getReader().read();
      first.abort();
      await new Promise((r) => setTimeout(r, 50));
      const next = new AbortController();
      const res = await post(url, background, next.signal);
      assert.equal(res.status, 200, "after a cancelled ask");
      next.abort();
    },
    { maxConcurrentReplies: 3 },
  );
});

test("a reply refused at the concurrency cap is logged with the cap", async () => {
  const { provider } = waiting();
  const logged: Record<string, unknown>[] = [];
  await withServer(
    provider,
    async (url) => {
      const first = new AbortController();
      await (await post(url, validBody, first.signal)).body?.getReader().read();
      await (await post(url, validBody)).body?.cancel();
      assert.deepEqual(logged, [
        {
          msg: "reply refused",
          provider: "waiting",
          model: "test-model",
          max_concurrent_replies: 1,
        },
      ]);
      first.abort();
    },
    { maxConcurrentReplies: 1, log: (record) => logged.push(record) },
  );
});

test("a body over the size limit is refused with a 413 problem", async () => {
  await withServer(
    scripted([]),
    async (url) => {
      const res = await post(url, { ...validBody, new_words: "x".repeat(200) });
      assert.equal(res.status, 413);
      assert.equal(res.headers.get("content-type"), "application/problem+json");
    },
    { maxBodyBytes: 100 },
  );
});

test("shutting down cancels the replies in flight", cancelWithin, async (t) => {
  const { provider, cancelled } = waiting();
  await withServer(
    provider,
    async (url, server) => {
      await (await post(url, validBody)).body?.getReader().read();
      await Promise.race([Promise.all([shutdown(server), cancelled]), once(t.signal, "abort")]);
    },
    noDeadline,
  );
});

test(
  "shutting down closes the provider before the replies in flight see their caller go",
  cancelWithin,
  async (t) => {
    const { provider, cancelled } = waiting();
    const order: string[] = [];
    provider.close = async () => {
      order.push("provider closed");
    };
    cancelled.then(() => order.push("reply cancelled"));
    await withServer(
      provider,
      async (url, server) => {
        await (await post(url, validBody)).body?.getReader().read();
        await Promise.race([
          Promise.all([shutdown(server, provider), cancelled]),
          once(t.signal, "abort"),
        ]);
      },
      noDeadline,
    );
    assert.deepEqual(order, ["provider closed", "reply cancelled"]);
  },
);

test("a full stream drains before the next event is written", async () => {
  const res = Object.assign(new EventEmitter(), { write: () => false });
  let written = false;
  const pending = writeEvent(res, { type: "text" }, new AbortController().signal).then(() => {
    written = true;
  });
  await Promise.resolve();
  assert.equal(written, false);
  res.emit("drain");
  await pending;
  assert.equal(written, true);
});
