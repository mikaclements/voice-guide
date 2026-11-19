import assert from "node:assert/strict";
import { test } from "node:test";
import type { ProviderEvent, ReplyRequest } from "../api.ts";
import { fakeProvider } from "./fake.ts";

const request: ReplyRequest = {
  model: "any",
  sessionId: "s1",
  stableLayers: ["x".repeat(400)],
  conversation: [],
  newWords: "y".repeat(40),
};

test("replies with the same words and the same pacing every time", async () => {
  const waits: number[] = [];
  const sleep = async (ms: number) => void waits.push(ms);
  const provider = fakeProvider(
    { reply: "One two three.", firstTextMs: 300, charsPerSecond: 100 },
    sleep,
  );
  const events: ProviderEvent[] = [];
  for await (const e of provider.reply(request, new AbortController().signal)) events.push(e);

  assert.deepEqual(
    events.filter((e) => e.type === "text").map((e) => e.text),
    ["One ", "two ", "three."],
  );
  assert.deepEqual(waits, [300, 40, 60]);
  assert.deepEqual(events.at(-1), {
    type: "usage",
    usage: { inputTokens: 110, cachedInputTokens: 0, cacheWriteTokens: 0, outputTokens: 4 },
  });
});

test("stops as soon as the reply is cancelled", async () => {
  const cancel = new AbortController();
  const settings = { reply: "One two three.", firstTextMs: 0, charsPerSecond: 1000 };
  const events: ProviderEvent[] = [];
  for await (const e of fakeProvider(settings, async () => {}).reply(request, cancel.signal)) {
    events.push(e);
    cancel.abort();
  }
  assert.deepEqual(events, [{ type: "text", text: "One " }]);
});
