import Anthropic from "@anthropic-ai/sdk";
import {
  type ErrorKind,
  type Message,
  type Provider,
  ProviderError,
  type ReplyRequest,
} from "../api.ts";
import {
  BACKGROUND_MAX_TOKENS,
  DEFAULT_BACKGROUND_REPLY_TIMEOUT_MS,
  DEFAULT_MAX_TOKENS,
  DEFAULT_REPLY_TIMEOUT_MS,
} from "../settings.ts";

// What the provider needs of the SDK: a reply streamed event by event, then the whole message, for
// why it stopped and its usage.
export interface ReplyStream extends AsyncIterable<Anthropic.MessageStreamEvent> {
  finalMessage(): Promise<Anthropic.Message>;
}
export type StreamFn = (
  params: Anthropic.MessageStreamParams,
  options: { signal: AbortSignal; timeout: number },
) => ReplyStream;

// Thinking before the first word adds seconds to every spoken reply, and each model turns it off
// its own way: Sonnet 5.5 refuses "disabled" and answers without thinking under "between_tools". A
// live reply on a model missing here is refused rather than left thinking; an ask outside any
// session, which nobody waits for, thinks as its model does by default, Opus 5.5 say, which can't
// turn thinking off.
const thinkingOff: Record<string, Anthropic.ThinkingConfigParam> = {
  "claude-sonnet-5-5": { type: "between_tools" },
  "claude-haiku-5-5": { type: "disabled" },
};

// The API takes the user's message first; a conversation the agent starts opens with this line,
// fixed, so it is the same bytes every turn and the cache still holds.
const conversationStart = "(The conversation starts.)";

const cached = { type: "ephemeral" } as const;

export interface ClaudeApiSettings {
  apiKey: string;
  maxTokens?: number;
  timeoutMs?: number;
  // The deadline of an ask outside any session, which the SDK then gives up at instead.
  backgroundTimeoutMs?: number;
  // Receives a record for a reply the model refused; never what was said.
  log?: (record: Record<string, unknown>) => void;
}

// Claude through the Messages API, billed per token on an API key. Nothing is kept between
// replies: every request carries the whole conversation, and the API's prompt cache makes the
// earlier turns quick and cheap to read again, so any copy of the service answers any turn.
export function claudeApiProvider(settings: ClaudeApiSettings, stream?: StreamFn): Provider {
  const {
    maxTokens = DEFAULT_MAX_TOKENS,
    timeoutMs = DEFAULT_REPLY_TIMEOUT_MS,
    backgroundTimeoutMs = DEFAULT_BACKGROUND_REPLY_TIMEOUT_MS,
    log = (record) => console.log(JSON.stringify(record)),
  } = settings;
  const send = stream ?? sdkStream(settings.apiKey, timeoutMs);
  return {
    name: "claude-api",
    async *reply(request, signal) {
      // The newest words go last as the user's; with none, the request would end on the agent's
      // message, which the API refuses.
      if (!request.newWords.trim())
        throw new ProviderError("bad_request", "claude-api: no new words", {
          reason: "the new words are blank",
        });
      const thinking = thinkingOff[request.model];
      if (!thinking && request.sessionId !== undefined)
        throw new ProviderError("bad_request", "claude-api: no rule turns thinking off", {
          reason: `no rule turns thinking off for this model; known: ${Object.keys(thinkingOff).join(", ")}`,
        });
      let message: Anthropic.Message;
      try {
        const timeout = request.sessionId === undefined ? backgroundTimeoutMs : timeoutMs;
        const reply = send(paramsFor(request, maxTokens, thinking), { signal, timeout });
        for await (const event of reply) {
          if (event.type === "content_block_delta" && event.delta.type === "text_delta")
            yield { type: "text", text: event.delta.text };
        }
        message = await reply.finalMessage();
      } catch (err) {
        if (signal.aborted) return;
        throw new ProviderError(kindOf(err), "claude-api: the reply failed", detailOf(err));
      }
      if (signal.aborted) return;
      // A refused reply, like one stopped at the token limit, ends with what was said.
      if (message.stop_reason === "refusal") {
        const category = message.stop_details?.category;
        log({
          msg: "model refused",
          provider: "claude-api",
          model: message.model,
          ...(typeof category === "string" && /^[a-z_]+$/.test(category) && { category }),
        });
      }
      const u = message.usage;
      yield {
        type: "usage",
        model: message.model,
        usage: {
          inputTokens: u.input_tokens,
          cachedInputTokens: u.cache_read_input_tokens ?? 0,
          cacheWriteTokens: u.cache_creation_input_tokens ?? 0,
          outputTokens: u.output_tokens,
        },
      };
    },
  };
}

// The SDK client, set here rather than by the environment: the API's own address, since a proxy
// named there, for Claude Code say, would receive the key and every conversation; no retries, since
// voice decides when to retry, as it does for every provider; giving up when the reply's deadline
// would; and no logs or traces of its own, which could quote a response, and so the user.
export function sdkClient(apiKey: string, timeoutMs: number): Anthropic {
  return new Anthropic({
    apiKey,
    baseURL: "https://api.anthropic.com",
    maxRetries: 0,
    timeout: timeoutMs,
    logLevel: "off",
    openTelemetry: false,
  });
}

function sdkStream(apiKey: string, timeoutMs: number): StreamFn {
  const client = sdkClient(apiKey, timeoutMs);
  return (params, options) => client.messages.stream(params, options);
}

// The layers as system blocks, cached after the agent's, shared by every session of its version,
// and after the last, which closes what the session keeps the same; then the conversation, cached
// after its last saved message, so the next turn reads it all from the cache; then the new message,
// after the cache point, since the newest screen input in it changes from turn to turn. Three cache
// points of the four the API takes. An ask outside any session is asked once, so caching its
// conversation would only pay to write it.
function paramsFor(
  r: ReplyRequest,
  maxTokens: number,
  thinking: Anthropic.ThinkingConfigParam | undefined,
): Anthropic.MessageStreamParams {
  const last = r.stableLayers.length - 1;
  const system = r.stableLayers.flatMap((text, i): Anthropic.TextBlockParam[] =>
    text.trim()
      ? [{ type: "text", text, ...((i === 0 || i === last) && { cache_control: cached }) }]
      : [],
  );
  return {
    model: r.model,
    // Thinking left on counts against the limit, so it gets room enough to leave the text whole.
    max_tokens: thinking ? maxTokens : BACKGROUND_MAX_TOKENS,
    ...(system.length > 0 && { system }),
    messages: messagesFor(r.conversation, r.newWords, r.sessionId !== undefined),
    ...(thinking && { thinking }),
    // A spoken reply is short and plain; the least effort reaches its first word soonest.
    output_config: { effort: "low" },
  };
}

type Side = { role: "user" | "assistant"; content: Anthropic.TextBlockParam[] };

// The API takes the two sides in turn, the user first. A message with nothing in it says nothing
// and is left out; two from the same side in a row, after a lost reply say, go as one. The last
// saved message's last block carries the cache point when asked; the new words never do.
function messagesFor(saved: Message[], newWords: string, cacheSaved: boolean): Side[] {
  const said = saved.filter((m) => m.text.trim() !== "");
  if (said.length > 0 && said[0]?.role !== "user")
    said.unshift({ role: "user", text: conversationStart });
  const messages: Side[] = [];
  const add = (role: Side["role"], block: Anthropic.TextBlockParam) => {
    const previous = messages.at(-1);
    if (previous?.role === role) previous.content.push(block);
    else messages.push({ role, content: [block] });
  };
  said.forEach((m, i) => {
    const block: Anthropic.TextBlockParam = { type: "text", text: m.text };
    if (cacheSaved && i === said.length - 1) block.cache_control = cached;
    add(m.role === "agent" ? "assistant" : "user", block);
  });
  add("user", { type: "text", text: newWords });
  return messages;
}

// The kind of a failure, from the SDK's error classes: a status before the stream started, the
// API's error type for one sent inside it, and a plain SDK error for a stream cut off midway.
function kindOf(err: unknown): ErrorKind {
  if (err instanceof Anthropic.RateLimitError) return "rate_limited";
  if (err instanceof Anthropic.OverloadedError) return "overloaded";
  if (
    err instanceof Anthropic.AuthenticationError ||
    err instanceof Anthropic.PermissionDeniedError
  )
    return "auth";
  if (err instanceof Anthropic.BadRequestError || err instanceof Anthropic.NotFoundError)
    return "bad_request";
  if (err instanceof Anthropic.ContentTooLargeError) return "too_large";
  // Out of credit: no retry clears it, as with a key that doesn't work. The SDK has no class for it.
  if (err instanceof Anthropic.APIError && err.status === 402) return "auth";
  if (err instanceof Anthropic.InternalServerError || err instanceof Anthropic.APIConnectionError)
    return "unavailable";
  if (err instanceof Anthropic.APIError && err.status === undefined)
    return streamErrorKinds[err.type ?? ""] ?? "unavailable";
  if (err instanceof Anthropic.AnthropicError) return "unavailable";
  return "unknown";
}

const streamErrorKinds: Record<string, ErrorKind> = {
  rate_limit_error: "rate_limited",
  overloaded_error: "overloaded",
  billing_error: "auth",
};

// The status and the API's error type, plain names and numbers; never the message, which can
// quote the user.
function detailOf(err: unknown): ProviderError["detail"] {
  if (!(err instanceof Anthropic.APIError)) return undefined;
  const detail: Record<string, string | number> = {};
  if (Number.isInteger(err.status)) detail.status = err.status as number;
  if (typeof err.type === "string" && /^[a-z_]+$/.test(err.type)) detail.error_type = err.type;
  return detail;
}
