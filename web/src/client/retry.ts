// What a failed request means for the request ID of the next send, and the failure a turn on the
// audio connection's data channel reports.
import { ProblemError, sessionEndedType } from "./api.ts";

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
