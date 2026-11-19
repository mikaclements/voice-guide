// The session's requests to voice's public API, sent with its ticket, and how voice's errors are
// read. The product's own calls, which hold the product key, are in the page's code, not here.

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

export type Session = {
  id: string;
  agent_id: string;
  agent_version: number;
  started_at: string;
  // Left out while the session is live.
  ended_at?: string;
};

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
export const sessionEndedType = "/v1/problems/session-ended";

// Whether err says the session has ended, so the page stops offering it turns.
export function endsSession(err: unknown): boolean {
  return err instanceof ProblemError && err.type === sessionEndedType;
}

// Whether err says the session's ticket has run out: the session is over for this page, and the
// user starts again.
export function ticketExpired(err: unknown): boolean {
  return err instanceof ProblemError && err.type === ticketExpiredType;
}

// fetch with voice's paths under its base URL.
export function at(api: string, fetcher: typeof fetch = fetch): typeof fetch {
  return (path, init) => fetcher(`${api}${String(path)}`, init);
}
