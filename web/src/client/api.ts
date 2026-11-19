// voice-guide's public API, called the way any product's page would call it. The page's dev
// server plays the product's server: it starts each session with its product key and the user's
// session context, and hands the page the session and the ticket for it.
import { EventParser } from "./events.ts";

// The problem voice answers a request with once the session's ticket has run out.
export const ticketExpiredType = "/v1/problems/ticket-expired";

// What the page says then, whatever the problem's own words: the user can only start again.
export const expiredMessage = "This session has expired; start again";

// An API error, from its problem details document when it sent one.
export class ProblemError extends Error {
  readonly status: number;
  readonly type: string;
  // Whether voice said it, as a problem details document, and not the product's server or
  // something between: a start refused by anything else never reached voice.
  readonly fromVoice: boolean;

  constructor(status: number, type: string, title: string, detail: string, fromVoice = true) {
    super(
      type === ticketExpiredType
        ? expiredMessage
        : detail
          ? `${title} (${status}): ${detail}`
          : `${title} (${status})`,
    );
    this.status = status;
    this.type = type;
    this.fromVoice = fromVoice;
  }
}

export async function problemFrom(res: Response): Promise<ProblemError> {
  if (res.headers.get("Content-Type")?.includes("application/problem+json")) {
    const p = await res.json();
    return new ProblemError(
      res.status,
      p.type ?? "about:blank",
      p.title ?? res.statusText,
      p.detail ?? "",
    );
  }
  // Plain text is the product's server's own explanation; any other body, such as a proxy's error page,
  // isn't meant to be read by people, so only its status is told.
  const plain = res.headers.get("Content-Type")?.startsWith("text/plain");
  return new ProblemError(
    res.status,
    "about:blank",
    res.statusText || "Error",
    plain ? await res.text() : "",
    false,
  );
}

// The product's side, served by the page's dev server: the agents it offers, as the product's
// server lists them with its key.
async function product<T>(fetcher: typeof fetch, path: string, body?: unknown): Promise<T> {
  const res = await fetcher(path, {
    method: body === undefined ? "GET" : "POST",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await problemFrom(res);
  return res.json();
}

// Sends a request with the session's ticket, the same one for every request in it. The body is the
// same if the caller sends it again, request ID included; it goes as JSON unless another type is
// named, when it is sent as it is.
export async function authorized(
  ticket: string,
  fetcher: typeof fetch,
  method: string,
  path: string,
  body?: unknown,
  type = "application/json",
): Promise<Response> {
  return fetcher(path, {
    method,
    headers: { Authorization: `Bearer ${ticket}`, "Content-Type": type },
    body:
      body === undefined
        ? undefined
        : type === "application/json"
          ? JSON.stringify(body)
          : String(body),
  });
}

async function call<T>(
  ticket: string,
  fetcher: typeof fetch,
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const res = await authorized(ticket, fetcher, method, path, body);
  if (!res.ok) throw await problemFrom(res);
  return res.json();
}

export type Agent = { id: string; name: string; description: string; session_context: string[] };
export type Session = {
  id: string;
  agent_id: string;
  agent_version: number;
  started_at: string;
  // Left out while the session is live.
  ended_at?: string;
};
// A started session with the ticket for it: the product's server starts it and hands both over.
export type Started = Session & { ticket: string };

export async function listAgents(fetcher: typeof fetch = fetch): Promise<Agent[]> {
  return (await product<{ agents: Agent[] }>(fetcher, "/product/agents")).agents;
}

// Asks the product's server to start a session with the agent for the user; it sends voice the
// user's reference and session context, with its key. A start repeated with the same request ID
// gets the same session back, with a new ticket.
export function startSession(
  agentId: string,
  requestId: string,
  fetcher: typeof fetch = fetch,
): Promise<Started> {
  return product<Started>(fetcher, "/product/start", { agent_id: agentId, request_id: requestId });
}

// Ends a session: voice takes no more turns in it. Ending it again answers the same, so a press
// whose reply was lost can simply be repeated.
export function endSession(
  ticket: string,
  sessionId: string,
  fetcher: typeof fetch = fetch,
): Promise<Session> {
  return call<Session>(ticket, fetcher, "POST", `/v1/sessions/${sessionId}/end`);
}

// The problem voice answers a turn or an audio connection with once the session has ended.
const sessionEndedType = "/v1/problems/session-ended";

// Whether err says the session has ended, so the page stops offering it turns.
export function endsSession(err: unknown): boolean {
  return err instanceof ProblemError && err.type === sessionEndedType;
}

// Whether err says the session's ticket has run out: the session is over for this page, and the
// user starts again.
export function ticketExpired(err: unknown): boolean {
  return err instanceof ProblemError && err.type === ticketExpiredType;
}

// What a failed request means for what it carried:
// - taken: voice already took a turn with this request ID, and doesn't replay its reply;
// - refused: voice turned it down, so sending it again is a new request;
// - busy: voice was answering another turn, so it didn't take this one, unless this send was itself
//   a retry, whose first try may be the turn in flight;
// - unsure: voice may have acted on it (a server error or the network), so sending it again repeats
//   its request ID, and voice acts on it once.
type Failure = "taken" | "refused" | "busy" | "unsure";

// Whether the next send of the same words repeats this one's request ID: only when voice may have
// taken it. A fresh send turned away as busy wasn't taken, so the next is a new request; keeping its
// ID would make a later send of the same words look like a repeat of a turn never taken.
export function keepRequestId(f: Failure, retry: boolean): boolean {
  return f === "unsure" || (f === "busy" && retry);
}

// A turn that failed on the audio connection's data channel, as the failure it means.
export class TurnFailure extends Error {
  readonly kind: Failure;

  constructor(kind: Failure, message: string) {
    super(message);
    this.kind = kind;
  }
}

export function failure(err: unknown): Failure {
  if (err instanceof TurnFailure) return err.kind;
  if (!(err instanceof ProblemError)) return "unsure";
  if (err.type === "/v1/problems/turn-repeated") return "taken";
  // Never taken, and sending it again can't help: the session is over.
  if (err.type === sessionEndedType) return "refused";
  if (err.status === 409) return "busy";
  return err.status >= 400 && err.status < 500 ? "refused" : "unsure";
}

// A request kept after an unsure failure, with what it carried: the words, or the agent.
export type Kept = { key: string; requestId: string };

// The request ID for sending key: the kept one when it is the same again, else a new one.
export function requestIdFor(key: string, kept: Kept | undefined): string {
  return kept?.key === key ? kept.requestId : crypto.randomUUID();
}

// How a turn ended: done, or lost when the reply didn't finish (the text so far is kept), with what
// answered it as provider/model. A spoken reply also says when its audio has stopped.
export type TurnEnd = {
  ended: "done" | "lost";
  turnId?: string;
  seq?: number;
  model?: string;
  spoken?: Promise<Spoken>;
};

// How a reply's audio ended: played to the end, or failed part way in text-to-speech; heard is set
// when the user interrupted it, to the reply's text as far as they heard it. For a turn the user
// spoke, firstAudioMs is how long after their speech ended voice sent the reply's first audio, and
// talkedOn says they spoke on before any of it sounded: voice took the turn back, and takes it
// again with all their words once they finish.
export type Spoken = { failed: boolean; heard?: string; firstAudioMs?: number; talkedOn?: boolean };

// Takes the user's words and streams the reply's text back, or, with opening set and no words, asks
// for the agent's opening. One implementation per transport: HTTP, and the audio connection's data
// channel while audio is on. interrupts says what a turn sent while another is answered does:
// interrupts it, or is refused as busy (HTTP, and a voice that doesn't take interrupts).
export interface Turns {
  readonly interrupts: boolean;
  take(
    words: string,
    requestId: string,
    onText: (text: string) => void,
    opening?: boolean,
  ): Promise<TurnEnd>;
}

// What a turn sends: the words, or the opening, which carries none.
export const turnBody = (requestId: string, words: string, opening = false) =>
  opening ? { request_id: requestId, opening: true } : { request_id: requestId, words };

export function httpTurns(sessionId: string, ticket: string, fetcher: typeof fetch = fetch): Turns {
  return {
    interrupts: false,
    async take(words, requestId, onText, opening) {
      const res = await authorized(
        ticket,
        fetcher,
        "POST",
        `/v1/sessions/${sessionId}/turns`,
        turnBody(requestId, words, opening),
      );
      if (!res.ok || !res.body) throw await problemFrom(res);
      const reader = res.body.getReader();
      const parser = new EventParser();
      const decoder = new TextDecoder();
      for (;;) {
        // Only reading fails on the network; a failure in the page itself surfaces as itself.
        const chunk = await reader.read().catch(() => undefined);
        // The stream stopped or broke with no final event: the connection dropped, and voice
        // saves the turn lost when its caller goes.
        if (!chunk || chunk.done) return { ended: "lost" };
        for (const ev of parser.push(decoder.decode(chunk.value, { stream: true }))) {
          if (ev.event === "text") onText(JSON.parse(ev.data).text);
          else if (ev.event === "done" || ev.event === "lost") {
            const data = JSON.parse(ev.data);
            return { ended: ev.event, turnId: data.turn_id, seq: data.seq, model: data.model };
          }
        }
      }
    },
  };
}

// Times a turn from pressing send to its first text and its first sound.
export function startTimer(now: () => number = () => performance.now()) {
  const start = now();
  let text: number | undefined;
  let sound: number | undefined;
  return {
    text(): number {
      text ??= now() - start;
      return text;
    },
    sound(): number {
      sound ??= now() - start;
      return sound;
    },
  };
}
