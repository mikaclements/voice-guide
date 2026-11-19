import assert from "node:assert/strict";
import { test } from "node:test";
import Anthropic from "@anthropic-ai/sdk";
import { ProviderError, type ProviderEvent, type ReplyRequest } from "../api.ts";
import { claudeApiProvider, type StreamFn, sdkClient } from "./claude-api.ts";

const request: ReplyRequest = {
  model: "claude-sonnet-5-5",
  sessionId: "s1",
  stableLayers: ["You are an interviewer.", "<resume>Built a queue.</resume>"],
  conversation: [
    { role: "user", text: "(The user has just joined.)" },
    { role: "agent", text: "Tell me about the queue." },
    { role: "user", text: "It was fast." },
    { role: "agent", text: "How fast?" },
  ],
  newWords: "Mostly by batching.",
};

const delta = (text: string) =>
  ({
    type: "content_block_delta",
    index: 0,
    delta: { type: "text_delta", text },
  }) as Anthropic.MessageStreamEvent;

const answered = {
  model: "claude-sonnet-5-5",
  stop_reason: "end_turn",
  stop_details: null,
  usage: {
    input_tokens: 12,
    cache_read_input_tokens: 900,
    cache_creation_input_tokens: 30,
    output_tokens: 4,
  },
};

// One reply of a fake Messages API, as the SDK's stream gives it: its events in order, then the
// whole message. "hang" waits until the reply is cancelled, then fails as the SDK does then; an
// Error is thrown where it stands, as the SDK throws a failure the API reported or a cut stream.
type Reply = { events: (Anthropic.MessageStreamEvent | Error | "hang")[]; final?: object };

function fakeApi(...replies: Reply[]) {
  const calls: { params: Anthropic.MessageStreamParams; signal: AbortSignal; timeout: number }[] =
    [];
  const stream: StreamFn = (params, { signal, timeout }) => {
    calls.push({ params, signal, timeout });
    const { events, final = answered } = replies.shift() ?? { events: [] };
    const messages = (async function* () {
      for (const item of events) {
        if (item === "hang") {
          if (!signal.aborted) await new Promise((r) => signal.addEventListener("abort", r));
          throw new Anthropic.APIUserAbortError();
        } else if (item instanceof Error) throw item;
        else yield item;
      }
    })();
    return Object.assign(messages, {
      finalMessage: async () => final as Anthropic.Message,
    });
  };
  return { stream, calls };
}

const logs: Record<string, unknown>[] = [];
const provider = (stream: StreamFn) =>
  claudeApiProvider({ apiKey: "sk-ant-test", maxTokens: 512, log: (r) => logs.push(r) }, stream);

async function collect(events: AsyncIterable<ProviderEvent>) {
  const out: ProviderEvent[] = [];
  for await (const e of events) out.push(e);
  return out;
}

const never = new AbortController().signal;
const cached = { type: "ephemeral" };
// A message as the API takes it: one side, its words as text blocks.
const said = (role: string, ...words: string[]) => ({
  role,
  content: words.map((text) => ({ type: "text", text })),
});
// The same message with the cache point on its last block.
const cachedAt = (message: ReturnType<typeof said>) => ({
  ...message,
  content: message.content.map((block, i, all) =>
    i === all.length - 1 ? { ...block, cache_control: cached } : block,
  ),
});

test("sends the layers as cached system blocks and the conversation with the newest words last", async () => {
  const { stream, calls } = fakeApi({ events: [] });
  await collect(provider(stream).reply(request, never));

  const params = calls[0]?.params;
  assert.equal(params?.model, "claude-sonnet-5-5");
  assert.equal(params?.max_tokens, 512);
  // The agent's layer is shared by every session of its version, and the last layer closes what
  // this session keeps the same; the conversation so far is cached at its last saved message, and
  // the new words, with any screen input in them, come after it.
  assert.deepEqual(params?.system, [
    { type: "text", text: "You are an interviewer.", cache_control: cached },
    { type: "text", text: "<resume>Built a queue.</resume>", cache_control: cached },
  ]);
  assert.equal("cache_control" in (params ?? {}), false);
  assert.deepEqual(params?.messages, [
    said("user", "(The user has just joined.)"),
    said("assistant", "Tell me about the queue."),
    said("user", "It was fast."),
    cachedAt(said("assistant", "How fast?")),
    said("user", "Mostly by batching."),
  ]);
  // No thinking before the first word, and no sampling settings, which the API refuses.
  assert.deepEqual(params?.thinking, { type: "between_tools" });
  assert.deepEqual(params?.output_config, { effort: "low" });
  for (const name of ["temperature", "top_p", "top_k"]) assert.equal(name in (params ?? {}), false);
  assert.equal(calls[0]?.signal, never);
});

test("Haiku has thinking turned off its own way; a model with no rule is refused before any call", async () => {
  const { stream, calls } = fakeApi({ events: [] });
  await collect(provider(stream).reply({ ...request, model: "claude-haiku-5-5" }, never));
  assert.deepEqual(calls[0]?.params.thinking, { type: "disabled" });

  await assert.rejects(
    collect(provider(stream).reply({ ...request, model: "claude-opus-5-5" }, never)),
    (err) =>
      err instanceof ProviderError &&
      err.kind === "bad_request" &&
      typeof err.detail?.reason === "string",
  );
  assert.equal(calls.length, 1);
});

test("a conversation the agent starts opens with a fixed line, and a side with two messages in a row sends them as one", async () => {
  const { stream, calls } = fakeApi({ events: [] });
  // A lost reply leaves two of the user's messages in a row; an empty one says nothing.
  const conversation = [
    { role: "agent", text: "Hello!" },
    { role: "user", text: "Hi." },
    { role: "agent", text: "" },
  ] as const;
  const r = { ...request, conversation: [...conversation], newWords: "Are you there?" };
  await collect(provider(stream).reply(r, never));
  assert.deepEqual(calls[0]?.params.messages, [
    said("user", "(The conversation starts.)"),
    said("assistant", "Hello!"),
    {
      role: "user",
      content: [
        { type: "text", text: "Hi.", cache_control: cached },
        { type: "text", text: "Are you there?" },
      ],
    },
  ]);
});

test("a board change in the new words leaves the cache point on the last saved message", async () => {
  const { stream, calls } = fakeApi({ events: [] }, { events: [] });
  const p = provider(stream);
  const board = (shows: string) => `<board>\n${shows}\n</board>\n\nLike this?`;
  await collect(p.reply({ ...request, newWords: board("Two boxes.") }, never));
  await collect(p.reply({ ...request, newWords: board("Three boxes.") }, never));
  // The two requests differ only after the cache point, so the second reads the first's cache.
  const [first, second] = calls.map((c) => c.params.messages);
  assert.deepEqual(first?.slice(0, -1), second?.slice(0, -1));
  assert.deepEqual(second?.at(-2), cachedAt(said("assistant", "How fast?")));
  assert.deepEqual(second?.at(-1), said("user", board("Three boxes.")));
});

test("the opening caches only its layers, since nothing is saved before it", async () => {
  const { stream, calls } = fakeApi({ events: [] });
  await collect(provider(stream).reply({ ...request, conversation: [] }, never));
  assert.deepEqual(calls[0]?.params.messages, [said("user", "Mostly by batching.")]);
  assert.equal("cache_control" in (calls[0]?.params ?? {}), false);
});

test("each request is sent as it came, with nothing kept from the one before", async () => {
  const { stream, calls } = fakeApi({ events: [delta("Ten times.")] }, { events: [] });
  const p = provider(stream);
  await collect(p.reply(request, never));
  const cut = { ...request, conversation: request.conversation.slice(0, 2), newWords: "Again." };
  await collect(p.reply(cut, never));
  assert.equal(calls[1]?.params.messages.length, 3);
  assert.deepEqual(calls[1]?.params.messages.at(-1), said("user", "Again."));
});

test("an ask outside any session caches nothing past its layers", async () => {
  const { stream, calls } = fakeApi({ events: [] });
  await collect(provider(stream).reply({ ...request, sessionId: undefined }, never));
  assert.equal("cache_control" in (calls[0]?.params ?? {}), false);
  const blocks = calls[0]?.params.messages.flatMap((m) => m.content as Anthropic.TextBlockParam[]);
  assert.equal(
    blocks?.some((b) => "cache_control" in b),
    false,
  );
});

test("yields text deltas in order, then the usage and the model that answered", async () => {
  const { stream } = fakeApi({ events: [delta("How much "), delta("faster?")] });
  assert.deepEqual(await collect(provider(stream).reply(request, never)), [
    { type: "text", text: "How much " },
    { type: "text", text: "faster?" },
    {
      type: "usage",
      model: "claude-sonnet-5-5",
      usage: { inputTokens: 12, cachedInputTokens: 900, cacheWriteTokens: 30, outputTokens: 4 },
    },
  ]);
});

test("a reply stopped at the token limit, or refused, ends with what was said; a refusal is logged without it", async () => {
  for (const stop of ["max_tokens", "refusal"]) {
    logs.length = 0;
    const final = { ...answered, stop_reason: stop, stop_details: { category: "cyber" } };
    const { stream } = fakeApi({ events: [delta("Well,")], final });
    const types = (await collect(provider(stream).reply(request, never))).map((e) => e.type);
    assert.deepEqual(types, ["text", "usage"], stop);
    const refused = { msg: "model refused", provider: "claude-api", model: "claude-sonnet-5-5" };
    assert.deepEqual(logs, stop === "refusal" ? [{ ...refused, category: "cyber" }] : [], stop);
  }
});

test("a failure ends with its kind and status, never the API's message", async () => {
  // What the SDK throws for a failed response, its body quoting the user as an API message may.
  const body = (type: string) => ({ type: "error", error: { type, message: "user said hello" } });
  const api = (status: number, type: string) =>
    Anthropic.APIError.generate(status, body(type), undefined, new Headers());
  const cases: [Error, string][] = [
    [api(429, "rate_limit_error"), "rate_limited"],
    [api(529, "overloaded_error"), "overloaded"],
    [api(500, "api_error"), "unavailable"],
    [new Anthropic.APIConnectionError({ message: "user said hello" }), "unavailable"],
    [api(401, "authentication_error"), "auth"],
    [api(403, "permission_error"), "auth"],
    [api(402, "billing_error"), "auth"],
    // A prompt past the model's context window is refused as a bad request.
    [api(400, "invalid_request_error"), "bad_request"],
    [api(404, "not_found_error"), "bad_request"],
    // An error the API sends inside a stream it has started has a type and no status.
    [
      new Anthropic.APIError(undefined, body("overloaded_error"), undefined, new Headers()),
      "overloaded",
    ],
  ];
  for (const [thrown, kind] of cases) {
    const { stream } = fakeApi({ events: [thrown] });
    await assert.rejects(
      collect(provider(stream).reply(request, never)),
      (err) =>
        err instanceof ProviderError &&
        err.kind === kind &&
        !`${err.message}${JSON.stringify(err.detail)}`.includes("hello"),
      kind,
    );
  }
  const { stream } = fakeApi({ events: [cases[1]?.[0] as Error] });
  await assert.rejects(collect(provider(stream).reply(request, never)), {
    detail: { status: 529, error_type: "overloaded_error" },
  });
});

test("a stream cut off midway through a reply fails as unavailable after the words that came", async () => {
  const cut = new Anthropic.AnthropicError("terminated: user said hello");
  const { stream } = fakeApi({ events: [delta("Why "), cut] });
  const events: ProviderEvent[] = [];
  await assert.rejects(
    (async () => {
      for await (const e of provider(stream).reply(request, never)) events.push(e);
    })(),
    (err) => err instanceof ProviderError && err.kind === "unavailable",
  );
  assert.deepEqual(events, [{ type: "text", text: "Why " }]);
});

test("cancelling a reply stops its stream and yields nothing more", async () => {
  const { stream, calls } = fakeApi({ events: [delta("Why "), "hang", delta("never")] });
  const cancel = new AbortController();
  const events: ProviderEvent[] = [];
  for await (const e of provider(stream).reply(request, cancel.signal)) {
    events.push(e);
    cancel.abort();
  }
  assert.deepEqual(events, [{ type: "text", text: "Why " }]);
  assert.equal(calls[0]?.signal.aborted, true);
});

test("the SDK calls the API itself, whatever the environment names, retries nothing and gives up at the reply's deadline", () => {
  process.env.ANTHROPIC_BASE_URL = "http://proxy.invalid";
  try {
    const client = sdkClient("sk-ant-test", 15_000);
    assert.equal(client.baseURL, "https://api.anthropic.com");
    assert.equal(client.apiKey, "sk-ant-test");
    assert.equal(client.maxRetries, 0);
    assert.equal(client.timeout, 15_000);
    // Its own logs and traces could quote a response, and so the user.
    assert.equal(client.logLevel, "off");
    assert.equal(client.openTelemetry.traces.enabled, false);
  } finally {
    delete process.env.ANTHROPIC_BASE_URL;
  }
});

test("a request with blank new words is refused before any call", async () => {
  const { stream, calls } = fakeApi({ events: [] });
  await assert.rejects(collect(provider(stream).reply({ ...request, newWords: "  " }, never)), {
    kind: "bad_request",
  });
  assert.equal(calls.length, 0);
});

test("an ask outside any session has the background deadline; a turn keeps its own", async () => {
  const { stream, calls } = fakeApi({ events: [] }, { events: [] });
  const api = claudeApiProvider(
    { apiKey: "sk-ant-test", timeoutMs: 15_000, backgroundTimeoutMs: 120_000 },
    stream,
  );
  await collect(api.reply({ ...request, sessionId: undefined }, never));
  await collect(api.reply(request, never));
  assert.deepEqual(
    calls.map((c) => c.timeout),
    [120_000, 15_000],
  );
});
