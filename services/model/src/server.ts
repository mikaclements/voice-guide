import { type EventEmitter, once } from "node:events";
import {
  createServer as createHttpServer,
  type IncomingMessage,
  type Server,
  type ServerResponse,
} from "node:http";
import { type ErrorKind, type Provider, ProviderError, parseRequest, type Usage } from "./api.ts";
import {
  DEFAULT_MAX_BODY_BYTES,
  DEFAULT_MAX_CONCURRENT_REPLIES,
  DEFAULT_REPLY_TIMEOUT_MS,
  MAX_BACKGROUND_REPLIES,
} from "./settings.ts";

export interface ServerOptions {
  now?: () => number;
  // Receives one record per reply; never what the user said or the reply's text.
  log?: (record: Record<string, unknown>) => void;
  // Limits, from settings: how long one reply may run, how many run at once, how big a request is.
  replyTimeoutMs?: number;
  maxConcurrentReplies?: number;
  maxBackgroundReplies?: number;
  maxBodyBytes?: number;
  // Makes the signal a reply's deadline aborts; tests fire it by hand instead of waiting.
  deadline?: (ms: number) => AbortSignal;
}

class TooLarge extends Error {}

export function createServer(provider: Provider, options: ServerOptions = {}) {
  const now = options.now ?? (() => performance.now());
  const log = options.log ?? ((record) => console.log(JSON.stringify(record)));
  const {
    replyTimeoutMs = DEFAULT_REPLY_TIMEOUT_MS,
    maxConcurrentReplies = DEFAULT_MAX_CONCURRENT_REPLIES,
    maxBackgroundReplies = MAX_BACKGROUND_REPLIES,
    maxBodyBytes = DEFAULT_MAX_BODY_BYTES,
  } = options;
  const startDeadline = options.deadline ?? ((ms) => AbortSignal.timeout(ms));
  let inFlight = 0;
  let background = 0;

  const handle = async (req: IncomingMessage, res: ServerResponse) => {
    if (req.url !== "/v1/replies") return problem(res, 404, "Not Found");
    if (req.method !== "POST") {
      res.setHeader("allow", "POST");
      return problem(res, 405, "Method Not Allowed");
    }
    let body: unknown;
    try {
      body = JSON.parse(await readBody(req, maxBodyBytes));
    } catch (err) {
      if (err instanceof TooLarge) return problem(res, 413, "Content Too Large", err.message);
      return problem(res, 400, "Bad Request", (err as Error).message);
    }
    const request = parseRequest(body);
    if (typeof request === "string") return problem(res, 400, "Bad Request", request);
    // Each running reply holds a provider process of its own, on top of those kept between turns;
    // past the cap, the caller retries rather than piling up. An ask outside any session is
    // background work, held to fewer places so live turns keep the rest.
    const isBackground = request.sessionId === undefined;
    if (inFlight >= maxConcurrentReplies || (isBackground && background >= maxBackgroundReplies)) {
      log({
        msg: "reply refused",
        provider: provider.name,
        model: request.model,
        max_concurrent_replies: maxConcurrentReplies,
        ...(isBackground && { max_background_replies: maxBackgroundReplies }),
      });
      return problem(res, 503, "At capacity", undefined, AT_CAPACITY);
    }

    // A caller that goes away stops the provider, so nothing is spent on an unheard answer;
    // a reply past its deadline stops it too, and the caller hears why.
    const callerGone = new AbortController();
    const deadline = startDeadline(replyTimeoutMs);
    const signal = AbortSignal.any([callerGone.signal, deadline]);

    inFlight++;
    if (isBackground) background++;
    res.writeHead(200, { "content-type": "application/x-ndjson" });
    const started = now();
    let firstText: number | null = null;
    let usage: Usage | null = null;
    let model = request.model;
    // Every reply ends with exactly one final event, written without waiting on a slow caller.
    let finished = false;
    const finish = (event?: object) => {
      if (finished) return;
      finished = true;
      if (event && !callerGone.signal.aborted) res.write(`${JSON.stringify(event)}\n`);
      res.end();
    };
    const fail = (kind: ErrorKind, detail?: ProviderError["detail"]) => {
      if (finished || callerGone.signal.aborted) return finish();
      finish({ type: "error", kind });
      log({ msg: "reply failed", provider: provider.name, model, kind, ...(detail && { detail }) });
    };
    const timingSoFar = () => ({
      first_text_ms: firstText === null ? null : Math.round(firstText - started),
      total_ms: Math.round(now() - started),
    });
    // A caller gone before the final event cancelled the turn, or the service is shutting down;
    // the record shows how far an answer nobody hears got.
    res.on("close", () => {
      if (!finished)
        log({ msg: "reply cancelled", provider: provider.name, model, ...timingSoFar() });
      callerGone.abort();
    });
    // The caller hears at the deadline even if the provider ignores its signal and runs on.
    deadline.addEventListener("abort", () => fail("unavailable"), { once: true });
    try {
      for await (const event of provider.reply(request, signal)) {
        if (signal.aborted) break;
        if (event.type === "text") {
          firstText ??= now();
          await writeEvent(res, event, signal);
        } else {
          usage = event.usage;
          model = event.model ?? model;
        }
      }
      if (signal.aborted) return;
      const timing = timingSoFar();
      finish({ type: "done", provider: provider.name, model, usage: wireUsage(usage), timing });
      log({ msg: "reply", provider: provider.name, model, ...timing });
    } catch (err) {
      if (signal.aborted) return;
      if (err instanceof ProviderError) fail(err.kind, err.detail);
      else fail("unknown");
    } finally {
      inFlight--;
      if (isBackground) background--;
      finish();
    }
  };

  return createHttpServer((req, res) => {
    // Only the error's name is logged: its message could hold anything, including the user's words.
    handle(req, res).catch((err: unknown) => {
      log({ msg: "request failed", error: err instanceof Error ? err.name : "unknown" });
      if (res.headersSent) return void res.end();
      problem(res, 500, "Internal Server Error");
    });
  });
}

// Stops taking requests and closes every connection, so replies in flight see their caller go
// and stop their providers instead of holding shutdown open. The provider, if given, is closed
// first, so a reply cut short here starts nothing for an ask that won't come; it settles once the
// provider has let go of everything.
export async function shutdown(server: Server, provider?: Provider): Promise<void> {
  const closing = provider?.close?.();
  const closed = new Promise<void>((resolve) => server.close(() => resolve()));
  server.closeAllConnections();
  await closed;
  await closing;
}

// Waits for a full socket to drain, so a slow caller slows the provider instead of filling memory.
export async function writeEvent(
  res: EventEmitter & { write(chunk: string): boolean },
  event: object,
  signal: AbortSignal,
) {
  if (!res.write(`${JSON.stringify(event)}\n`)) await once(res, "drain", { signal });
}

// null, not zeros, when the provider reported none, so an unmeasured reply never looks measured.
function wireUsage(u: Usage | null) {
  if (u === null) return null;
  return {
    input_tokens: u.inputTokens,
    cached_input_tokens: u.cachedInputTokens,
    cache_write_tokens: u.cacheWriteTokens,
    output_tokens: u.outputTokens,
  };
}

async function readBody(req: IncomingMessage, limit: number): Promise<string> {
  let size = 0;
  const chunks: Buffer[] = [];
  for await (const chunk of req) {
    size += chunk.length;
    if (size > limit) throw new TooLarge(`body over ${limit} bytes`);
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString("utf8");
}

// Errors follow RFC 9457 problem details, like the rest of voice-guide's API.
// The refusal at this service's own cap on replies, told apart from the provider being overloaded:
// a caller may ask again in moments, without counting it as a failure.
const AT_CAPACITY = "/v1/problems/at-capacity";

function problem(
  res: ServerResponse,
  status: number,
  title: string,
  detail?: string,
  type = "about:blank",
) {
  res.writeHead(status, { "content-type": "application/problem+json" });
  res.end(JSON.stringify({ type, title, status, detail }));
}
