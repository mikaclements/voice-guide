import assert from "node:assert/strict";
import { test } from "node:test";
import { settingsFromEnv } from "./settings.ts";

const base = { MODEL_PROVIDER: "fake" };

test("unset settings take their defaults", () => {
  const s = settingsFromEnv(base);
  assert.equal(s.provider, "fake");
  assert.equal(s.host, "127.0.0.1");
  assert.equal(s.port, 8091);
  assert.equal(s.replyTimeoutMs, 15_000);
  assert.equal(s.maxConcurrentReplies, 4);
  assert.equal(s.maxBodyBytes, 1 << 20);
  assert.equal(s.maxSessionProcesses, 4);
  assert.equal(s.sessionProcessIdleMs, 300_000);
  assert.equal(s.maxTokens, 1024);
});

test("set values are used", () => {
  const s = settingsFromEnv({
    ...base,
    MODEL_MAX_CONCURRENT_REPLIES: "8",
    MODEL_REPLY_TIMEOUT_MS: "5000",
    MODEL_MAX_SESSION_PROCESSES: "0",
    MODEL_HOST: "0.0.0.0",
  });
  assert.equal(s.host, "0.0.0.0");
  assert.equal(s.maxConcurrentReplies, 8);
  assert.equal(s.replyTimeoutMs, 5000);
  assert.equal(s.maxSessionProcesses, 0);
});

test("an unusable value stops startup with a message naming the setting", () => {
  for (const [name, value] of [
    ["MODEL_MAX_BODY_BYTES", "1MB"],
    ["MODEL_MAX_CONCURRENT_REPLIES", "four"],
    ["MODEL_MAX_CONCURRENT_REPLIES", "0"],
    // One place would leave a live turn none while a summary is written.
    ["MODEL_MAX_CONCURRENT_REPLIES", "1"],
    ["MODEL_REPLY_TIMEOUT_MS", "30s"],
    ["MODEL_REPLY_TIMEOUT_MS", "-1"],
    ["MODEL_PORT", "99999"],
    ["MODEL_MAX_SESSION_PROCESSES", "65"],
    ["MODEL_SESSION_PROCESS_IDLE_MS", "0"],
    ["MODEL_FAKE_CHARS_PER_SECOND", "1.5"],
    ["MODEL_MAX_TOKENS", "0"],
    ["MODEL_MAX_TOKENS", "8193"],
    ["MODEL_PROVIDER", "gpt"],
    ["MODEL_HOST", "localhost"],
    ["MODEL_HOST", "127.0.0.1:8091"],
  ]) {
    assert.throws(() => settingsFromEnv({ ...base, [name]: value }), new RegExp(name));
  }
});

test("the API key is read for claude-api only, which refuses to start without one, naming each choice", () => {
  const key = "sk-ant-secret";
  assert.equal(settingsFromEnv({ ...base, ANTHROPIC_API_KEY: key }).apiKey, undefined);
  assert.equal(
    settingsFromEnv({ MODEL_PROVIDER: "claude-api", ANTHROPIC_API_KEY: key }).apiKey,
    key,
  );
  assert.throws(
    () => settingsFromEnv({ MODEL_PROVIDER: "claude-api" }),
    /ANTHROPIC_API_KEY.*MODEL_PROVIDER=claude-subscription.*MODEL_PROVIDER=fake/,
  );
  // A message for another unusable setting, which startup logs, never holds the key.
  assert.throws(
    () =>
      settingsFromEnv({
        MODEL_PROVIDER: "claude-api",
        ANTHROPIC_API_KEY: key,
        MODEL_PORT: "99999",
      }),
    (err) =>
      err instanceof Error && err.message.includes("MODEL_PORT") && !err.message.includes(key),
  );
});
