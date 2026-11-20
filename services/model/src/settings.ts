import { isIP } from "node:net";

// How long one reply may run unless set. voice gives a turn 20s and keeps its last 5s for saving
// how the turn ended, so a reply gets the other 15s; ending it here at the same point also stops
// Claude Code's own API call, instead of leaving it running after voice has given up.
export const DEFAULT_REPLY_TIMEOUT_MS = 15_000;

// How long an ask outside any session may run unless set: a session's summary, which may run on a
// slower model over a long transcript, and nobody waits for it. A round guess, to be measured on the
// subscription with Opus; voice's summary job allows one such ask and 30 s around it in one run.
export const DEFAULT_BACKGROUND_REPLY_TIMEOUT_MS = 120_000;

// The longest answer, in tokens, to an ask outside any session on a model whose thinking stays on, or
// to any ask with a schema: a summary of up to 2,000 characters, some 500 tokens, the agent's own data,
// and room for the thinking before them, so the thinking never cuts the text short. A round guess,
// measured with the first summaries on Opus.
export const BACKGROUND_MAX_TOKENS = 16_000;

// The port voice calls unless told otherwise, so the two meet with neither set.
const DEFAULT_PORT = 8091;

// Replies at once, and session processes kept between turns: each is a Claude Code process of
// about 250 MB (measured) on one developer's subscription, so 4 of each covers one developer's
// sessions on a laptop. A round guess, revisited when the subscription route is replaced.
export const DEFAULT_MAX_CONCURRENT_REPLIES = 4;
// Of those, asks outside any session, a session's summary say, run one at a time: they are
// background work, and live turns keep the other places, so a few sessions ending together never
// leave a user's turn without one. Counted within the cap above.
export const MAX_BACKGROUND_REPLIES = 1;
export const DEFAULT_MAX_SESSION_PROCESSES = 4;

// The largest request body. A turn's prompt with a full session context and a long conversation
// stays far below it; a conversation that outgrows it is refused as too large until conversations
// are shortened. A round guess.
export const DEFAULT_MAX_BODY_BYTES = 1 << 20;

// How long an unused session process is kept: the prompt cache it reads from lasts 5 minutes by
// default, so a process idle for longer would read its whole prompt again anyway.
export const DEFAULT_SESSION_PROCESS_IDLE_MS = 300_000;

// The longest reply, in tokens, unless set: a spoken reply runs 50 to 150, so this only stops a
// runaway. A round guess, measured from real turns later.
export const DEFAULT_MAX_TOKENS = 1024;

// The fake's pace, close to the real model's as measured on Sonnet 5.5: first text after about a
// second, then about 50 tokens (200 characters) a second.
const DEFAULT_FAKE_FIRST_TEXT_MS = 800;
const DEFAULT_FAKE_CHARS_PER_SECOND = 200;

// The service's settings, read from the environment and checked once at startup: an unusable value
// stops the service with a message naming it, instead of quietly turning a limit off.
export interface Settings {
  provider: "claude-api" | "claude-subscription" | "fake";
  // The API key, read for claude-api only, the one provider that bills it; never logged.
  apiKey?: string;
  maxTokens: number;
  host: string;
  port: number;
  replyTimeoutMs: number;
  backgroundReplyTimeoutMs: number;
  maxConcurrentReplies: number;
  maxBodyBytes: number;
  maxSessionProcesses: number;
  sessionProcessIdleMs: number;
  fakeFirstTextMs: number;
  fakeCharsPerSecond: number;
}

export function settingsFromEnv(env: Record<string, string | undefined>): Settings {
  const whole = (name: string, fallback: number, min: number, max: number) => {
    const raw = env[name];
    if (raw === undefined || raw === "") return fallback;
    const value = /^\d+$/.test(raw) ? Number(raw) : Number.NaN;
    if (!(value >= min && value <= max)) {
      throw new Error(`${name} must be a whole number from ${min} to ${max}, got "${raw}"`);
    }
    return value;
  };
  const provider = env.MODEL_PROVIDER;
  if (provider !== "claude-api" && provider !== "claude-subscription" && provider !== "fake") {
    throw new Error(
      `MODEL_PROVIDER must be claude-api, claude-subscription or fake, got "${provider ?? ""}"`,
    );
  }
  const apiKey = provider === "claude-api" ? env.ANTHROPIC_API_KEY : undefined;
  if (provider === "claude-api" && !apiKey) {
    throw new Error(
      "MODEL_PROVIDER=claude-api needs ANTHROPIC_API_KEY, an API key from the Claude Console; or set MODEL_PROVIDER=claude-subscription to use the Claude login on this machine, or MODEL_PROVIDER=fake for free canned replies",
    );
  }
  // This machine only unless set: the service has no login of its own, so anyone who could reach
  // it could spend the Claude login it runs on. A container sets 0.0.0.0 inside its own network.
  const host = env.MODEL_HOST || "127.0.0.1";
  if (isIP(host) === 0) {
    throw new Error(`MODEL_HOST must be an IP address, such as 127.0.0.1, got "${host}"`);
  }
  return {
    provider,
    apiKey,
    host,
    port: whole("MODEL_PORT", DEFAULT_PORT, 1, 65_535),
    replyTimeoutMs: whole("MODEL_REPLY_TIMEOUT_MS", DEFAULT_REPLY_TIMEOUT_MS, 1_000, 600_000),
    backgroundReplyTimeoutMs: whole(
      "MODEL_BACKGROUND_REPLY_TIMEOUT_MS",
      DEFAULT_BACKGROUND_REPLY_TIMEOUT_MS,
      1_000,
      600_000,
    ),
    // At least two: one ask outside any session may hold a place, and live turns must keep one.
    maxConcurrentReplies: whole(
      "MODEL_MAX_CONCURRENT_REPLIES",
      DEFAULT_MAX_CONCURRENT_REPLIES,
      MAX_BACKGROUND_REPLIES + 1,
      64,
    ),
    maxBodyBytes: whole("MODEL_MAX_BODY_BYTES", DEFAULT_MAX_BODY_BYTES, 1 << 10, 16 << 20),
    // Session processes kept between turns (0 keeps none), and how long one is kept unused.
    maxSessionProcesses: whole("MODEL_MAX_SESSION_PROCESSES", DEFAULT_MAX_SESSION_PROCESSES, 0, 64),
    sessionProcessIdleMs: whole(
      "MODEL_SESSION_PROCESS_IDLE_MS",
      DEFAULT_SESSION_PROCESS_IDLE_MS,
      1_000,
      3_600_000,
    ),
    // Spoken replies are short; a value past a model's own limit would fail every turn.
    maxTokens: whole("MODEL_MAX_TOKENS", DEFAULT_MAX_TOKENS, 1, 8_192),
    fakeFirstTextMs: whole("MODEL_FAKE_FIRST_TEXT_MS", DEFAULT_FAKE_FIRST_TEXT_MS, 0, 60_000),
    fakeCharsPerSecond: whole(
      "MODEL_FAKE_CHARS_PER_SECOND",
      DEFAULT_FAKE_CHARS_PER_SECOND,
      1,
      100_000,
    ),
  };
}
