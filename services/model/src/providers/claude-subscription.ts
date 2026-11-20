import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  type Options,
  type SDKMessage,
  type SDKUserMessage,
  SYSTEM_PROMPT_DYNAMIC_BOUNDARY,
  query as sdkQuery,
} from "@anthropic-ai/claude-agent-sdk";
import {
  type ErrorKind,
  type Message,
  type Provider,
  ProviderError,
  type ProviderEvent,
  type ReplyRequest,
  type Usage,
} from "../api.ts";
import {
  DEFAULT_BACKGROUND_REPLY_TIMEOUT_MS,
  DEFAULT_MAX_SESSION_PROCESSES,
  DEFAULT_REPLY_TIMEOUT_MS,
  DEFAULT_SESSION_PROCESS_IDLE_MS,
} from "../settings.ts";
import { SessionProcesses } from "./session-processes.ts";

// What the provider needs of the SDK's query: messages read one at a time, so one Claude Code
// process answers many turns, and a way to stop that process that settles once it has exited, or
// once the SDK stops waiting for it after 2s and kills it.
export type SessionQuery = AsyncIterator<SDKMessage> & AsyncDisposable;
export type QueryFn = (params: {
  prompt: AsyncIterable<SDKUserMessage>;
  options?: Options;
}) => SessionQuery;

// The SDK counts its own internal steps as turns, per message sent; with no tools a reply is one
// real turn, so this is only a backstop against a runaway loop.
const maxTurns = 4;

// Claude on the developer's own subscription, reached through the Claude Agent SDK, which runs
// Claude Code: free during development, for personal use only. One Claude Code process is kept per
// session between turns, its session process, so a turn skips the process start and reads earlier
// turns from cache. The session process is a disposable cache of the conversation: voice sends the
// whole conversation every time, and a session process that hasn't seen exactly that conversation
// is replaced by a fresh one.
export function claudeSubscriptionProvider(
  query: QueryFn = sdkQuery,
  env: NodeJS.ProcessEnv = process.env,
  limits: {
    apiTimeoutMs?: number;
    backgroundApiTimeoutMs?: number;
    maxSessionProcesses?: number;
    sessionProcessIdleMs?: number;
  } = {},
): Provider {
  const {
    apiTimeoutMs = DEFAULT_REPLY_TIMEOUT_MS,
    backgroundApiTimeoutMs = DEFAULT_BACKGROUND_REPLY_TIMEOUT_MS,
    maxSessionProcesses = DEFAULT_MAX_SESSION_PROCESSES,
    sessionProcessIdleMs = DEFAULT_SESSION_PROCESS_IDLE_MS,
  } = limits;
  // Claude Code reads instructions, settings and memory from where it runs; an empty folder
  // keeps everything the agent didn't declare away from the model. Made at the first start, so a
  // service that never starts a process leaves no folder behind, however it ends.
  let cwd: string | undefined;
  const sessionProcesses = new SessionProcesses<SessionProcess>(
    maxSessionProcesses,
    sessionProcessIdleMs,
  );
  // Every process started here that hasn't exited yet: the folder they run in is removed only
  // after them. Once the provider is closed, nothing starts.
  const live = new Set<SessionProcess>();
  let closed = false;
  const start = (request: ReplyRequest) => {
    if (closed) throw new ProviderError("unavailable", "agent SDK: the provider is closed");
    cwd ??= mkdtempSync(join(tmpdir(), "voice-guide-model-"));
    const started = new SessionProcess(query, request, {
      model: request.model,
      systemPrompt: systemPromptFor(request.stableLayers),
      tools: [],
      // Thinking before the first word adds seconds to every spoken reply.
      thinking: { type: "disabled" },
      includePartialMessages: true,
      maxTurns,
      cwd,
      settingSources: [],
      // Compaction would rewrite the conversation inside a turn, so a session process would no
      // longer hold what voice sent; shortening the conversation is voice's.
      settings: {
        autoMemoryEnabled: false,
        disableClaudeAiConnectors: true,
        autoCompactEnabled: false,
      },
      // No MCP servers at all, including the Claude account's own connectors.
      strictMcpConfig: true,
      mcpServers: {},
      persistSession: false,
      // Claude Code holds the answer to the schema and retries an answer that misses it.
      ...(request.schema && { outputFormat: { type: "json_schema", schema: request.schema } }),
      env: childEnv(env, request.sessionId === undefined ? backgroundApiTimeoutMs : apiTimeoutMs),
    });
    live.add(started);
    started.exited.then(() => live.delete(started));
    return started;
  };

  return {
    name: "claude-subscription",
    async *reply(request, signal) {
      if (request.sessionId === undefined) {
        // Outside any session: a process of its own, closed once it has answered, so no kept
        // session process is taken, replaced or pushed out.
        const process = start(request);
        try {
          const answered = yield* process.turn(request, promptFor(request), signal);
          if (!answered && !signal.aborted) throw ended(process.crash);
        } finally {
          process.close();
        }
        return;
      }
      const sessionId = request.sessionId;
      let process = sessionProcesses.take(sessionId);
      let answered = false;
      try {
        if (process?.continues(request)) {
          // Only what changed: the process holds every earlier screen input block already.
          answered = yield* process.turn(request, request.newWordsOwn ?? request.newWords, signal);
          if (answered || signal.aborted) return;
          if (process.spoke) throw ended(process.crash);
          // The process died between turns. Nothing has reached the caller yet, so a fresh one
          // takes the turn.
        }
        if (!process?.unused(request)) {
          process?.close();
          process = start(request);
        }
        answered = yield* process.turn(request, promptFor(request), signal);
        if (!answered && !signal.aborted) throw ended(process.crash);
      } finally {
        if (process && answered && !closed) sessionProcesses.put(sessionId, process);
        else {
          process?.close();
          // A reply its caller cancelled is most often asked again at once, with the user's words
          // merged or the reply before cut: a process started now, which has seen nothing yet,
          // takes that ask without waiting for Claude Code to start. Not in place of another
          // session's kept process, nor after this service's deadline or once closing, when no ask
          // follows. A cancel that is in fact the caller's own deadline, voice's being shorter than
          // this one, looks the same and starts one too: it is a place that was free, closed after
          // the idle time like any kept process, so it never costs another session its process.
          const deadline = (signal.reason as Error | undefined)?.name === "TimeoutError";
          if (process && signal.aborted && !deadline && !closed && sessionProcesses.hasRoom())
            sessionProcesses.put(sessionId, start(request));
        }
      }
    },
    async close() {
      closed = true;
      sessionProcesses.closeAll();
      // A running reply's process too, and those closed earlier that are still exiting: the folder
      // goes, with whatever Claude Code left in it, only once none runs there.
      await Promise.all([...live].map((p) => p.close()));
      if (cwd) rmSync(cwd, { recursive: true, force: true });
    },
  };
}

type Crash = Record<string, string | number>;

const ended = (crash?: Crash) =>
  new ProviderError("unavailable", "agent SDK: stream ended without a result", crash);

// What a crash says without its message, which ends with what Claude Code printed and can quote
// the user: the SDK tags its error with a fixed class and the exit code or signal. Only plain
// names and whole numbers are kept.
function crashOf(err: unknown): Crash {
  const { errorClass, exitCode, signal } = (err ?? {}) as Record<string, unknown>;
  const crash: Crash = {
    error_class:
      typeof errorClass === "string" && /^[a-z_]+$/.test(errorClass) ? errorClass : "unknown",
  };
  if (Number.isInteger(exitCode)) crash.exit_code = exitCode as number;
  if (typeof signal === "string" && /^SIG[A-Z0-9]+$/.test(signal)) crash.signal = signal;
  return crash;
}

// The session process: one Claude Code process kept for one session. It has seen `seen`, the
// conversation as voice records it, and continues a request only if the request's conversation is
// exactly that.
class SessionProcess {
  private readonly inbox = inbox();
  private readonly query: SessionQuery;
  private readonly model: string;
  private readonly layers: string[];
  private seen: Message[] = [];
  // The SDK reports usage summed over the process's life; each turn reports its own share.
  private totals: ModelUsage = {};
  spoke = false;
  // Whether any words have been sent to the process.
  private sent = false;
  // How the process died, if it crashed.
  crash?: Crash;
  private readonly exit = Promise.withResolvers<void>();
  private stopping = false;
  // Settles once the process has exited, whichever way it was closed.
  readonly exited = this.exit.promise;

  constructor(query: QueryFn, request: ReplyRequest, options: Options) {
    this.model = request.model;
    this.layers = request.stableLayers;
    this.query = query({ prompt: this.inbox.messages, options });
  }

  continues(r: ReplyRequest): boolean {
    const text = (ms: Message[]) => ms.flatMap((m) => [m.role, m.text]);
    return this.started(r) && this.sent && same(text(r.conversation), text(this.seen));
  }

  // Whether the process was started for the request's model and stable layers and has been sent
  // nothing yet, so it takes the whole conversation as a fresh process does.
  unused(r: ReplyRequest): boolean {
    return this.started(r) && !this.sent;
  }

  private started(r: ReplyRequest): boolean {
    return r.model === this.model && same(r.stableLayers, this.layers);
  }

  // Sends the words and yields the reply; returns whether the process answered. The reason for a
  // failure arrives on an assistant message before the result that reports it, or before the
  // process dies; a refused usage limit wins, since no brief retry clears it before it resets.
  async *turn(
    request: ReplyRequest,
    words: string,
    signal: AbortSignal,
  ): AsyncGenerator<ProviderEvent, boolean> {
    this.spoke = false;
    this.sent = true;
    this.inbox.send(words);
    let reply = "";
    let reason: ErrorKind | undefined;
    let overQuota = false;
    const reported = () => (overQuota ? "quota" : reason);
    // A cancelled reply stops waiting at once; the process is closed and exits on its own.
    const { promise: cancelled, resolve } = Promise.withResolvers<undefined>();
    const cancel = () => resolve(undefined);
    signal.addEventListener("abort", cancel, { once: true });
    if (signal.aborted) cancel();
    try {
      for (let msg = await this.next(cancelled); msg; msg = await this.next(cancelled)) {
        // An answer to a schema arrives whole with the result; text before it is the model's aside.
        if (
          !request.schema &&
          msg.type === "stream_event" &&
          msg.event.type === "content_block_delta" &&
          msg.event.delta.type === "text_delta"
        ) {
          reply += msg.event.delta.text;
          this.spoke = true;
          yield { type: "text", text: msg.event.delta.text };
        }
        if (msg.type === "assistant" && msg.error) reason = errorKinds[msg.error] ?? "unknown";
        if (msg.type === "rate_limit_event" && msg.rate_limit_info.status === "rejected")
          overQuota = true;
        if (msg.type === "result") {
          // A result can say success and still be an error.
          if (msg.subtype !== "success" || msg.is_error)
            throw new ProviderError(reported() ?? "unknown", `agent SDK: ${msg.subtype}`);
          const usage = usageOf(msg.modelUsage, this.totals);
          this.totals = msg.modelUsage;
          // The words as voice will save them, so the next turn's conversation matches: a screen
          // input block becomes a marker there. The process still holds the block it was shown.
          this.seen = [
            ...request.conversation,
            { role: "user", text: request.newWordsSaved ?? request.newWords },
            { role: "agent", text: reply },
          ];
          // An answer held to the schema, or, without one, the result's own text for voice to check.
          if (request.schema) {
            const answer =
              msg.structured_output === undefined
                ? msg.result
                : JSON.stringify(msg.structured_output);
            yield { type: "text", text: answer };
          }
          yield { type: "usage", ...usage };
          return true;
        }
      }
      // Ended without a result. One that reported a failure first fails with it, so no fresh
      // process calls the API again: retrying is voice's.
      const kind = reported();
      if (kind && !signal.aborted)
        throw new ProviderError(kind, "agent SDK: ended after an error", this.crash);
      return false;
    } finally {
      signal.removeEventListener("abort", cancel);
    }
  }

  // The next message, or undefined once the process has ended or the reply is cancelled. The SDK
  // ends the stream when Claude Code exits and throws when it dies; either way the process is gone,
  // and only the crash's tags are kept.
  private async next(cancelled: Promise<undefined>): Promise<SDKMessage | undefined> {
    const crashed = (err: unknown) => {
      this.crash = crashOf(err);
      return undefined;
    };
    const result = await Promise.race([this.query.next().catch(crashed), cancelled]);
    return result?.done ? undefined : result?.value;
  }

  // Stops the process, once however often it is called, and returns when it has exited. A
  // failure to stop it still leaves nothing to wait for.
  close(): Promise<void> {
    if (!this.stopping) {
      this.stopping = true;
      this.inbox.end();
      const exited = () => this.exit.resolve();
      Promise.resolve(this.query[Symbol.asyncDispose]()).then(exited, exited);
    }
    return this.exited;
  }
}

const same = (a: string[], b: string[]) => a.length === b.length && a.every((x, i) => x === b[i]);

// The messages a session process reads, one per turn; ending them lets the process exit. One turn
// runs at a time, so at most one message waits.
function inbox() {
  const waiting: SDKUserMessage[] = [];
  let wake = () => {};
  let ended = false;
  async function* messages() {
    while (true) {
      const next = waiting.shift();
      if (next) yield next;
      else if (ended) return;
      else await new Promise<void>((resolve) => (wake = resolve));
    }
  }
  return {
    messages: messages(),
    send(text: string) {
      waiting.push({
        type: "user",
        message: { role: "user", content: text },
        parent_tool_use_id: null,
      });
      wake();
    },
    end() {
      ended = true;
      wake();
    },
  };
}

// The first layer is the agent's, the same for every session, so it goes before the boundary
// the SDK uses to mark what can be cached across sessions; the rest belong to this session.
function systemPromptFor([agent = "", ...session]: string[]): string[] {
  return session.length ? [agent, SYSTEM_PROMPT_DYNAMIC_BOUNDARY, ...session] : [agent];
}

// A fresh process gets the conversation so far as one transcript; after that it gets only the new
// words, so earlier turns are read from cache.
function promptFor(request: ReplyRequest): string {
  if (request.conversation.length === 0) return request.newWords;
  const transcript = request.conversation.map((m) => `${m.role}: ${m.text}`).join("\n");
  return [
    `<transcript>\n${transcript}\n</transcript>`,
    "The transcript is the conversation so far. Reply to the user's latest words, below, as you would say them.",
    request.newWords,
  ].join("\n\n");
}

type AssistantError = NonNullable<Extract<SDKMessage, { type: "assistant" }>["error"]>;

const errorKinds: Partial<Record<AssistantError, ErrorKind>> = {
  rate_limit: "rate_limited",
  overloaded: "overloaded",
  server_error: "unavailable",
  authentication_failed: "auth",
  oauth_org_not_allowed: "auth",
  account_on_hold: "auth",
  verification_required: "auth",
  billing_error: "auth",
  cloud_credential_error: "auth",
  invalid_request: "bad_request",
  model_not_found: "bad_request",
};

type ModelUsage = Extract<SDKMessage, { type: "result" }>["modelUsage"];

// This turn's usage, over every model the process used since the turn before; the one that wrote
// the most is the one that answered.
function usageOf(byModel: ModelUsage, before: ModelUsage): { usage: Usage; model?: string } {
  const usage = { inputTokens: 0, cachedInputTokens: 0, cacheWriteTokens: 0, outputTokens: 0 };
  let model: string | undefined;
  let most = -1;
  for (const [name, u] of Object.entries(byModel)) {
    const b = before[name];
    const output = u.outputTokens - (b?.outputTokens ?? 0);
    usage.inputTokens += u.inputTokens - (b?.inputTokens ?? 0);
    usage.cachedInputTokens += u.cacheReadInputTokens - (b?.cacheReadInputTokens ?? 0);
    usage.cacheWriteTokens += u.cacheCreationInputTokens - (b?.cacheCreationInputTokens ?? 0);
    usage.outputTokens += output;
    if (output > most) [model, most] = [name, output];
  }
  return { usage, model };
}

// What Claude Code needs from the service's environment, and nothing else: the service's own
// secrets, such as the ticket key and the database's address, never reach it. PATH finds the
// keychain tool that reads the login on macOS; HOME, USER and CLAUDE_CONFIG_DIR say where the
// login is kept and under which name; CLAUDE_CODE_OAUTH_TOKEN is the login itself where there is no
// keychain, as in a container; TMPDIR keeps its temporary files in the user's own folder. An API
// key is left out on purpose: Claude Code would bill it instead of using the subscription.
const childEnvNames = [
  "PATH",
  "HOME",
  "USER",
  "TMPDIR",
  "CLAUDE_CONFIG_DIR",
  "CLAUDE_CODE_OAUTH_TOKEN",
];

// Claude Code connects the account's claude.ai connectors unless told not to. voice decides when
// to retry, so Claude Code retries nothing itself and gives up on an API call when the reply's
// deadline would.
function childEnv(env: NodeJS.ProcessEnv, apiTimeoutMs: number): Record<string, string> {
  const passed: Record<string, string> = {};
  for (const name of childEnvNames) {
    const value = env[name];
    if (value !== undefined) passed[name] = value;
  }
  return {
    ...passed,
    ENABLE_CLAUDEAI_MCP_SERVERS: "false",
    CLAUDE_CODE_MAX_RETRIES: "0",
    API_TIMEOUT_MS: String(apiTimeoutMs),
  };
}
