import assert from "node:assert/strict";
import { existsSync, mkdtempSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, test } from "node:test";
import { setImmediate as turnOfEventLoop } from "node:timers/promises";
import {
  type Options,
  type SDKMessage,
  SYSTEM_PROMPT_DYNAMIC_BOUNDARY,
} from "@anthropic-ai/claude-agent-sdk";
import { type Provider, ProviderError, type ProviderEvent, type ReplyRequest } from "../api.ts";
import { claudeSubscriptionProvider, type QueryFn } from "./claude-subscription.ts";

const request: ReplyRequest = {
  model: "claude-haiku-5-5",
  sessionId: "s1",
  stableLayers: ["You are an interviewer.", "<resume>Built a queue.</resume>"],
  conversation: [
    { role: "agent", text: "Tell me about the queue." },
    { role: "user", text: "It was fast." },
  ],
  newWords: "Mostly by batching.",
};

const delta = (text: string) =>
  ({
    type: "stream_event",
    event: { type: "content_block_delta", index: 0, delta: { type: "text_delta", text } },
  }) as unknown as SDKMessage;

const success = {
  type: "result",
  subtype: "success",
  is_error: false,
  modelUsage: {
    "claude-haiku-5-5": {
      inputTokens: 12,
      outputTokens: 4,
      cacheReadInputTokens: 900,
      cacheCreationInputTokens: 30,
    },
  },
} as unknown as SDKMessage;

// The SDK sums usage over a process's life, so a second turn's result holds both turns' usage.
const usage = { inputTokens: 20, outputTokens: 7, cacheReadInputTokens: 1500 };
const successAgain = {
  ...success,
  modelUsage: { "claude-haiku-5-5": { ...usage, cacheCreationInputTokens: 30 } },
} as unknown as SDKMessage;

// One turn of a fake Claude Code process: what it sends back for one message. "exit" ends the
// stream there, as the SDK does when Claude Code exits cleanly; "crash" throws, as the SDK does
// when Claude Code dies; an Error is thrown as given.
type Turn = (SDKMessage | "exit" | "crash" | Error)[];

// The error the SDK throws when Claude Code dies: a message with the tail of what it printed, which
// can quote the user, tagged with a fixed class and the exit code.
const crashError = () =>
  Object.assign(new Error("Claude Code process exited with code 1. stderr: user said hello"), {
    errorClass: "process_exited_nonzero",
    exitCode: 1,
  });

// The temporary folder the providers make their folders in, this file's own, so a service or test
// running elsewhere changes no count. Every provider a test makes is closed once the tests are
// done, and none may leave a folder behind, whichever way it was made.
const temp = mkdtempSync(join(tmpdir(), "model-test-"));
process.env.TMPDIR = temp;
const folders = () => readdirSync(temp).filter((f) => f.startsWith("voice-guide-model-"));
const made: Provider[] = [];
const newProvider = (query: QueryFn, env: NodeJS.ProcessEnv = {}) => {
  const provider = claudeSubscriptionProvider(query, env);
  made.push(provider);
  return provider;
};
after(async () => {
  await Promise.all(made.map((p) => p.close?.()));
  const left = folders();
  rmSync(temp, { recursive: true, force: true });
  assert.deepEqual(left, []);
});

// A fake SDK query: each process reads one message per turn and answers it with the next turn. A
// closed process has exited at once, unless a test holds its exit back until a promise settles.
function fakeQuery(...turns: Turn[]) {
  const calls: { options?: Options; prompts: string[]; closed: boolean }[] = [];
  let exit = Promise.resolve();
  const query: QueryFn = ({ prompt, options }) => {
    const call = { options, prompts: [] as string[], closed: false };
    calls.push(call);
    const messages = (async function* () {
      for await (const m of prompt) {
        call.prompts.push(m.message.content as string);
        for (const item of turns.shift() ?? []) {
          if (item === "exit") return;
          if (item === "crash") throw crashError();
          if (item instanceof Error) throw item;
          yield item;
        }
      }
    })();
    return Object.assign(messages, {
      [Symbol.asyncDispose]() {
        call.closed = true;
        return exit;
      },
    });
  };
  return {
    query,
    calls,
    holdExit(exited: Promise<void>) {
      exit = exited;
    },
  };
}

async function collect(events: AsyncIterable<ProviderEvent>) {
  const out: ProviderEvent[] = [];
  for await (const e of events) out.push(e);
  return out;
}

const reply = (query: QueryFn, signal = new AbortController().signal) =>
  newProvider(query).reply(request, signal);

const never = new AbortController().signal;

// The request voice sends after a reply: the conversation now holds the words and that reply.
const nextTurn = (r: ReplyRequest, replied: string, newWords: string): ReplyRequest => ({
  ...r,
  conversation: [
    ...r.conversation,
    { role: "user", text: r.newWords },
    { role: "agent", text: replied },
  ],
  newWords,
});

test("maps the layered prompt to a tool-less, thinking-off call on the requested model", async () => {
  const { query, calls } = fakeQuery([success]);
  const env = { PATH: "/bin", ANTHROPIC_API_KEY: "sk-test", ANTHROPIC_AUTH_TOKEN: "tok" };
  await collect(newProvider(query, env).reply(request, new AbortController().signal));

  const options = calls[0]?.options;
  assert.equal(options?.model, "claude-haiku-5-5");
  assert.deepEqual(options?.tools, []);
  assert.deepEqual(options?.thinking, { type: "disabled" });
  assert.equal(options?.includePartialMessages, true);
  // The agent layer is shared by every session of the agent; what follows the boundary is this session's.
  assert.deepEqual(options?.systemPrompt, [
    "You are an interviewer.",
    SYSTEM_PROMPT_DYNAMIC_BOUNDARY,
    "<resume>Built a queue.</resume>",
  ]);
  // An API key would silently bill the key instead of using the subscription; voice owns retrying,
  // so Claude Code's own retries are off and its API calls time out with the reply.
  assert.deepEqual(options?.env, {
    PATH: "/bin",
    ENABLE_CLAUDEAI_MCP_SERVERS: "false",
    CLAUDE_CODE_MAX_RETRIES: "0",
    API_TIMEOUT_MS: "15000",
  });

  const prompt = calls[0]?.prompts[0] ?? "";
  assert.ok(
    prompt.indexOf("agent: Tell me about the queue.") < prompt.indexOf("user: It was fast."),
  );
  assert.ok(prompt.endsWith("Mostly by batching."));
});

test("Claude Code gets only the environment it needs, never the service's own secrets", async () => {
  const { query, calls } = fakeQuery([success]);
  const needed = {
    PATH: "/usr/bin:/bin",
    HOME: "/home/dev",
    USER: "dev",
    TMPDIR: "/tmp/dev",
    CLAUDE_CONFIG_DIR: "/home/dev/.claude-other",
    CLAUDE_CODE_OAUTH_TOKEN: "subscription-token",
  };
  const env = {
    ...needed,
    VOICE_TICKET_KEY: "ticket-signing-key",
    DATABASE_URL: "postgres://voice:voice@localhost:5432/voice",
    SOME_OTHER_SETTING: "x",
  };
  await collect(newProvider(query, env).reply(request, never));

  assert.deepEqual(calls[0]?.options?.env, {
    ...needed,
    ENABLE_CLAUDEAI_MCP_SERVERS: "false",
    CLAUDE_CODE_MAX_RETRIES: "0",
    API_TIMEOUT_MS: "15000",
  });
});

test("nothing from the machine's Claude Code setup reaches the model", async () => {
  const { query, calls } = fakeQuery([success]);
  await collect(reply(query));

  const options = calls[0]?.options;
  assert.deepEqual(options?.settingSources, []);
  assert.equal(options?.persistSession, false);
  assert.deepEqual(options?.settings, {
    autoMemoryEnabled: false,
    disableClaudeAiConnectors: true,
    autoCompactEnabled: false,
  });
  // No MCP servers: neither configured ones nor the account's claude.ai connectors.
  assert.equal(options?.strictMcpConfig, true);
  assert.deepEqual(options?.mcpServers, {});
  assert.equal(options?.env?.ENABLE_CLAUDEAI_MCP_SERVERS, "false");
  // An empty folder, so no CLAUDE.md, project settings or memory folder sits where it runs.
  assert.deepEqual(readdirSync(options?.cwd ?? "."), []);
});

test("yields text deltas in order, then the usage and the model that answered", async () => {
  const { query } = fakeQuery([delta("How much "), delta("faster?"), success]);
  assert.deepEqual(await collect(reply(query)), [
    { type: "text", text: "How much " },
    { type: "text", text: "faster?" },
    {
      type: "usage",
      model: "claude-haiku-5-5",
      usage: { inputTokens: 12, cachedInputTokens: 900, cacheWriteTokens: 30, outputTokens: 4 },
    },
  ]);
});

test("the next turn of a session reuses its process, sending only the new words", async () => {
  const { query, calls } = fakeQuery(
    [delta("How much "), delta("faster?"), success],
    [delta("Why?"), successAgain],
  );
  const provider = newProvider(query);
  await collect(provider.reply(request, never));
  const second = nextTurn(request, "How much faster?", "Ten times.");

  assert.deepEqual((await collect(provider.reply(second, never))).at(-1), {
    type: "usage",
    model: "claude-haiku-5-5",
    usage: { inputTokens: 8, cachedInputTokens: 600, cacheWriteTokens: 0, outputTokens: 3 },
  });
  assert.equal(calls.length, 1);
  assert.deepEqual(calls[0]?.prompts.at(-1), "Ten times.");
});

test("a turn with a board continues its process when the next conversation holds the board's marker", async () => {
  const { query, calls } = fakeQuery([delta("Two boxes."), success], [delta("Why?"), successAgain]);
  const provider = newProvider(query);
  const withBoard = {
    ...request,
    newWords: "<board>\nTwo boxes.\n</board>\n\nLike this?",
    newWordsSaved: "(The user's board changed here.)\n\nLike this?",
  };
  await collect(provider.reply(withBoard, never));
  const second = {
    ...nextTurn({ ...withBoard, newWords: withBoard.newWordsSaved }, "Two boxes.", "Yes."),
    newWordsSaved: undefined,
  };
  await collect(provider.reply(second, never));
  assert.equal(calls.length, 1);
  assert.equal(calls[0]?.prompts[1], "Yes.");
  // A fresh process reads the markers in the transcript and the board whole in the new words.
  assert.match(
    calls[0]?.prompts[0] ?? "",
    /user: It was fast\.\n<\/transcript>[\s\S]*<board>\nTwo boxes\./,
  );

  const fresh = fakeQuery([success]);
  await collect(newProvider(fresh.query).reply(second, never));
  const transcript = fresh.calls[0]?.prompts[0] ?? "";
  assert.match(transcript, /user: \(The user's board changed here\.\)\n\nLike this\?/);
  assert.doesNotMatch(transcript, /Two boxes\.\n<\/board>/);
});

test("a kept process is sent only the screen input the new turn carried, never the earlier boards again", async () => {
  const { query, calls } = fakeQuery([delta("Two boxes."), success], [delta("Why?"), successAgain]);
  const provider = newProvider(query);
  const withBoard = {
    ...request,
    newWords: "<board>\nTwo boxes.\n</board>\n\nLike this?",
    newWordsSaved: "(The user's board changed here.)\n\nLike this?",
  };
  await collect(provider.reply(withBoard, never));
  const second = {
    ...nextTurn({ ...withBoard, newWords: withBoard.newWordsSaved }, "Two boxes.", ""),
    newWords: "<board>\nTwo boxes.\n</board>\n\n<notes>\nTidy.\n</notes>\n\nAnd these?",
    newWordsSaved: "(The user's notes changed here.)\n\nAnd these?",
    newWordsOwn: "<notes>\nTidy.\n</notes>\n\nAnd these?",
  };
  await collect(provider.reply(second, never));
  assert.equal(calls.length, 1);
  assert.equal(calls[0]?.prompts[1], "<notes>\nTidy.\n</notes>\n\nAnd these?");
});

test("a conversation the process hasn't seen exactly gets a fresh process", async () => {
  const changes: [string, (r: ReplyRequest) => ReplyRequest][] = [
    // The user cut the reply short, so voice saved it as far as heard.
    ["reply cut short", (r) => nextTurn(r, "How much", "Ten times.")],
    // A speculative start was cancelled and the user's words merged with what came next.
    ["words merged", (r) => ({ ...r, newWords: "Mostly by batching, and caching." })],
    ["another model", (r) => ({ ...nextTurn(r, "How much faster?", "Ten."), model: "other" })],
    ["another layer", (r) => ({ ...nextTurn(r, "How much faster?", "Ten."), stableLayers: [] })],
  ];
  for (const [name, change] of changes) {
    const { query, calls } = fakeQuery([delta("How much faster?"), success], [success]);
    const provider = newProvider(query);
    await collect(provider.reply(request, never));
    await collect(provider.reply(change(request), never));
    assert.equal(calls.length, 2, name);
    assert.equal(calls[0]?.closed, true, name);
  }
});

test("a process that died between turns is replaced, and the turn is still answered", async () => {
  for (const end of ["exit", "crash"] as const) {
    const { query, calls } = fakeQuery(
      [delta("How much faster?"), success],
      [end],
      [delta("Why?"), success],
    );
    const provider = newProvider(query);
    await collect(provider.reply(request, never));
    const events = await collect(
      provider.reply(nextTurn(request, "How much faster?", "Ten times."), never),
    );

    assert.deepEqual(events[0], { type: "text", text: "Why?" }, end);
    assert.equal(calls.length, 2, end);
    // The fresh process gets the whole conversation, the dead one's last turn included.
    assert.match(calls[1]?.prompts[0] ?? "", /agent: How much faster\?[\s\S]*Ten times\.$/, end);
  }
});

const assistantError = (error: string) =>
  ({ type: "assistant", message: { content: [] }, error }) as unknown as SDKMessage;
const result = (subtype: string, is_error = true) =>
  ({ type: "result", subtype, is_error, modelUsage: {} }) as unknown as SDKMessage;
const limit = (status: string) =>
  ({
    type: "rate_limit_event",
    rate_limit_info: { status, rateLimitType: "five_hour", resetsAt: 1794740000 },
  }) as unknown as SDKMessage;

test("a failed reply keeps the kind of error the SDK reported, and its process is closed", async () => {
  const cases: [Turn, string][] = [
    [[assistantError("rate_limit"), result("error_during_execution")], "rate_limited"],
    // The subscription's usage limit: no brief retry clears it before it resets.
    [[limit("rejected"), assistantError("rate_limit"), result("error_during_execution")], "quota"],
    [[limit("allowed_warning"), assistantError("rate_limit"), result("success")], "rate_limited"],
    [[assistantError("overloaded"), result("error_during_execution")], "overloaded"],
    [[assistantError("server_error"), result("error_during_execution")], "unavailable"],
    [[assistantError("authentication_failed"), result("success")], "auth"],
    [[assistantError("billing_error"), result("success")], "auth"],
    [[assistantError("model_not_found"), result("error_during_execution")], "bad_request"],
    // A success that is really an error, with no reason given.
    [[result("success")], "unknown"],
    [[result("error_max_turns")], "unknown"],
    [[delta("How much "), "exit"], "unavailable"],
    // A fresh process that dies, before its first word or midway, is gone: voice may retry.
    [["crash"], "unavailable"],
    [[delta("How much "), "crash"], "unavailable"],
    // A process that reported a failure and then died fails with what it reported.
    [[limit("rejected"), assistantError("rate_limit"), "crash"], "quota"],
    [[assistantError("overloaded"), "exit"], "overloaded"],
  ];
  for (const [messages, kind] of cases) {
    const { query, calls } = fakeQuery(messages);
    await assert.rejects(collect(reply(query)), (err) => {
      assert.ok(err instanceof ProviderError);
      assert.equal(err.kind, kind);
      return true;
    });
    assert.equal(calls[0]?.closed, true);
  }
});

test("a crash keeps the SDK's error class, exit code and signal, never its message", async () => {
  const killed = Object.assign(new Error("terminated by signal SIGKILL. stderr: user said hello"), {
    errorClass: "process_killed_by_signal",
    signal: "SIGKILL",
  });
  // A tag that isn't a plain name or number is left out, so no free text slips through.
  const odd = Object.assign(new Error("user said hello"), {
    errorClass: "user said hello",
    exitCode: "1",
    signal: "user said hello",
  });
  const cases: [Error, Record<string, string | number>][] = [
    [crashError(), { error_class: "process_exited_nonzero", exit_code: 1 }],
    [killed, { error_class: "process_killed_by_signal", signal: "SIGKILL" }],
    [odd, { error_class: "unknown" }],
  ];
  for (const [thrown, detail] of cases) {
    await assert.rejects(collect(reply(fakeQuery([thrown]).query)), (err) => {
      assert.ok(err instanceof ProviderError);
      assert.equal(err.kind, "unavailable");
      assert.deepEqual(err.detail, detail);
      assert.ok(!err.message.includes("user said hello"));
      return true;
    });
  }
});

test("a session process that reports a failure, then dies, fails with it and is not replaced", async () => {
  for (const end of ["exit", "crash"] as const) {
    const { query, calls } = fakeQuery(
      [delta("How much faster?"), success],
      [limit("rejected"), assistantError("rate_limit"), end],
      // What a second process would answer, if one were wrongly started.
      [delta("Why?"), success],
    );
    const provider = newProvider(query);
    await collect(provider.reply(request, never));

    // A fresh process would call the API again: a retry only voice decides.
    await assert.rejects(
      collect(provider.reply(nextTurn(request, "How much faster?", "Ten times."), never)),
      (err) => err instanceof ProviderError && err.kind === "quota",
      end,
    );
    assert.equal(calls.length, 1, end);
  }
});

test("a session process that fails a turn keeps the error kind and is closed, not run again", async () => {
  const { query, calls } = fakeQuery(
    [delta("How much faster?"), success],
    [limit("rejected"), assistantError("rate_limit"), result("error_during_execution")],
    // What a second process would answer, if one were wrongly started.
    [delta("Why?"), success],
  );
  const provider = newProvider(query);
  await collect(provider.reply(request, never));

  // voice alone decides when to retry, so the failure reaches it as it came.
  await assert.rejects(
    collect(provider.reply(nextTurn(request, "How much faster?", "Ten times."), never)),
    (err) => err instanceof ProviderError && err.kind === "quota",
  );
  assert.equal(calls.length, 1);
  assert.equal(calls[0]?.closed, true);
});

test("a session process that dies mid-reply fails the turn, with no second reply after its words", async () => {
  for (const end of ["exit", "crash"] as const) {
    const { query, calls } = fakeQuery(
      [delta("How much faster?"), success],
      [delta("Why "), end],
      [delta("A whole new reply."), success],
    );
    const provider = newProvider(query);
    await collect(provider.reply(request, never));

    const events: ProviderEvent[] = [];
    await assert.rejects(
      (async () => {
        for await (const e of provider.reply(nextTurn(request, "How much faster?", "Ten."), never))
          events.push(e);
      })(),
      (err) => err instanceof ProviderError && err.kind === "unavailable",
      end,
    );
    assert.deepEqual(events, [{ type: "text", text: "Why " }], end);
    assert.equal(calls.length, 1, end);
  }
});

test("cancelling a session process's reply ends it, closes the process and starts another", async () => {
  // The second turn sends one word, then nothing more until cancelled.
  const { query, calls } = fakeQuery([delta("How much faster?"), success], [delta("Why ")]);
  const provider = newProvider(query);
  await collect(provider.reply(request, never));

  const cancel = new AbortController();
  const events: ProviderEvent[] = [];
  for await (const e of provider.reply(
    nextTurn(request, "How much faster?", "Ten."),
    cancel.signal,
  )) {
    events.push(e);
    cancel.abort();
  }
  assert.deepEqual(events, [{ type: "text", text: "Why " }]);
  assert.equal(calls[0]?.closed, true);
  // Started for the session's next ask, and sent nothing yet.
  assert.equal(calls.length, 2);
  assert.deepEqual(calls[1]?.prompts, []);
});

test("the ask after a cancelled reply takes the process started for it, with the whole conversation", async () => {
  const words = [
    ["the same model and layers", request, 2],
    ["another model", { ...request, model: "other" }, 3],
  ] as const;
  for (const [name, next, processes] of words) {
    const { query, calls } = fakeQuery([delta("How ")], [delta("Why?"), success]);
    const provider = newProvider(query);
    const cancel = new AbortController();
    for await (const _ of provider.reply(request, cancel.signal)) cancel.abort();
    assert.equal(calls.length, 2, `${name}: a process is started before the next ask comes`);
    // The user talked on: the same conversation, with their words merged.
    const merged = { ...next, newWords: "Mostly by batching, and caching." };
    assert.deepEqual((await collect(provider.reply(merged, never)))[0], {
      type: "text",
      text: "Why?",
    });
    assert.equal(calls.length, processes, name);
    assert.match(calls.at(-1)?.prompts[0] ?? "", /user: It was fast\.[\s\S]*caching\.$/, name);
  }
});

test("a cancel starts no process in another session's kept place, nor after the reply's deadline", async () => {
  // Another session's process is kept, filling the one place.
  const full = fakeQuery([success], [delta("How ")]);
  const provider = claudeSubscriptionProvider(full.query, {}, { maxSessionProcesses: 1 });
  made.push(provider);
  await collect(provider.reply({ ...request, sessionId: "s0" }, never));
  const cancel = new AbortController();
  for await (const _ of provider.reply(request, cancel.signal)) cancel.abort();
  assert.equal(full.calls.length, 2, "no process started for the next ask");
  assert.equal(full.calls[0]?.closed, false, "the other session's process is kept");

  const timed = fakeQuery([delta("How ")]);
  const deadline = new AbortController();
  for await (const _ of newProvider(timed.query).reply(request, deadline.signal))
    deadline.abort(new DOMException("the reply's deadline passed", "TimeoutError"));
  assert.equal(timed.calls.length, 1, "no process started after the deadline");
});

test("cancelling mid-reply ends it at once, without waiting for the process to exit", async () => {
  const cancel = new AbortController();
  let closed = false;
  // A process that sends one word, then never another message, even once closed.
  const query: QueryFn = ({ prompt }) =>
    Object.assign(
      (async function* () {
        for await (const _ of prompt) {
          yield delta("How much ");
          await new Promise(() => {});
        }
      })(),
      {
        [Symbol.asyncDispose]() {
          closed = true;
          return Promise.resolve();
        },
      },
    );
  const events: ProviderEvent[] = [];
  for await (const e of reply(query, cancel.signal)) {
    events.push(e);
    cancel.abort();
  }
  assert.deepEqual(events, [{ type: "text", text: "How much " }]);
  assert.equal(closed, true);
});

test("closing the provider closes every session process it keeps", async () => {
  const { query, calls } = fakeQuery([success], [success]);
  const provider: Provider = newProvider(query);
  await collect(provider.reply(request, never));
  await collect(provider.reply({ ...request, sessionId: "s2" }, never));
  assert.deepEqual(
    calls.map((c) => c.closed),
    [false, false],
  );
  await provider.close?.();
  assert.deepEqual(
    calls.map((c) => c.closed),
    [true, true],
  );
});

test("a provider makes its folder at its first process, not before", async () => {
  const fake = fakeQuery([success]);
  const before = folders().length;
  const provider = newProvider(fake.query);
  assert.equal(folders().length, before);
  await collect(provider.reply(request, never));
  assert.equal(folders().length, before + 1);
  await provider.close?.();
  assert.equal(folders().length, before);
});

test("closing the provider removes its folder, with anything left in it, once its processes exit", async () => {
  const fake = fakeQuery([success]);
  const exited = Promise.withResolvers<void>();
  fake.holdExit(exited.promise);
  const provider = newProvider(fake.query);
  await collect(provider.reply(request, never));
  const cwd = fake.calls[0]?.options?.cwd ?? "";
  writeFileSync(join(cwd, "left-behind.json"), "{}");

  const closing = provider.close?.();
  await turnOfEventLoop();
  assert.equal(existsSync(cwd), true);
  exited.resolve();
  await closing;
  assert.equal(existsSync(cwd), false);
});

test("closing the provider stops a reply still running, and waits for its process to exit", async () => {
  // The reply sends one word, then nothing more.
  const fake = fakeQuery([delta("How much ")]);
  const exited = Promise.withResolvers<void>();
  fake.holdExit(exited.promise);
  const provider = newProvider(fake.query);
  const replying = provider.reply(request, never)[Symbol.asyncIterator]();
  assert.deepEqual((await replying.next()).value, { type: "text", text: "How much " });

  const closing = provider.close?.();
  await assert.rejects(replying.next(), (err) => err instanceof ProviderError);
  assert.equal(fake.calls[0]?.closed, true);
  await turnOfEventLoop();
  assert.equal(existsSync(fake.calls[0]?.options?.cwd ?? ""), true);
  exited.resolve();
  await closing;
  assert.equal(existsSync(fake.calls[0]?.options?.cwd ?? ""), false);
});

test("a reply after the provider has closed fails, and starts no process", async () => {
  const fake = fakeQuery([success]);
  const provider = newProvider(fake.query);
  await provider.close?.();
  await assert.rejects(
    collect(provider.reply(request, never)),
    (err) => err instanceof ProviderError && err.kind === "unavailable",
  );
  assert.equal(fake.calls.length, 0);
});

test("an ask outside any session gets a process of its own, closed once it answers", async () => {
  const { query, calls } = fakeQuery(
    [delta("How much faster?"), success],
    [delta("Covered caching."), success],
    [delta("Why?"), successAgain],
  );
  const provider = newProvider(query);
  await collect(provider.reply(request, never));
  const { sessionId: _, ...outside } = request;
  await collect(provider.reply({ ...outside, newWords: "Note what was covered." }, never));
  assert.equal(calls.length, 2);
  assert.equal(calls[1]?.closed, true, "closed once it answered");
  assert.equal(calls[0]?.closed, false, "the session's kept process is left alone");

  await collect(provider.reply(nextTurn(request, "How much faster?", "Ten times."), never));
  assert.equal(calls.length, 2, "the session's next turn still takes its kept process");
});
