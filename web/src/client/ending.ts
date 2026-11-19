// How the page ends its session with the End button, and how it learns the session ended otherwise:
// by the product's server, or after sitting idle.
import { endsSession, expiredMessage, ProblemError, type Session, ticketExpired } from "./api.ts";
import type { Closing } from "./audio.ts";

// What the page says when voice ends the audio connection; the page goes on in text.
export const closings: Record<Closing, string> = {
  replaced: "Another tab took this session's audio; this one goes on in text.",
  stopping: "The server stopped; replies come as text until the audio reconnects.",
  ended: "The session ended. Start again for a new one.",
  expired: expiredMessage,
  dropped: "The audio connection dropped; replies come as text while it reconnects.",
};

// What the page says when the browser refuses the microphone; typing works meanwhile.
export const microphoneRefused =
  "The microphone is blocked: allow it in the browser's settings for this site, then start again to talk.";

// A dropped or stopped connection is tried again every 2s for 30s, long enough for a server to
// restart, after which the page stays in text until the session is started again.
export const reconnectEveryMs = 2000;
export const reconnectTries = 15;

// What the page does once voice ends its audio connection, saying why: voice ends it with "ended"
// only once the session is over, and with "expired" once its ticket has run out, which no request
// can use again; a stop or a drop is reconnected; anything else goes on in text.
export function afterClosing(why: Closing): "over" | "reconnect" | "text" {
  if (why === "ended" || why === "expired") return "over";
  return why === "stopping" || why === "dropped" ? "reconnect" : "text";
}

// Why a session is over for the page: it ended, its ticket ran out, or voice refused its ticket
// some other way (the signing key was changed, say), so nothing more can be sent in it.
export type Over = "ended" | "expired" | "unusable";

// What it says when it is over, and not ended: the user can only start again.
export const unusableMessage = "This session can't continue; start again";

// What the page says once its session is over, by why.
export const overMessages: Record<Over, string> = {
  ended: closings.ended,
  expired: expiredMessage,
  unusable: unusableMessage,
};

// Whether err from a session's route means the session is over, and why. Any 401 there does: the
// ticket is the only credential the page has. So does a 404: the ticket isn't this session's, as
// when a product's page hands over one session's ID with another's ticket, and no retry mends it.
export function whyOver(err: unknown): Over | undefined {
  if (endsSession(err)) return "ended";
  if (ticketExpired(err)) return "expired";
  if (err instanceof ProblemError && (err.status === 401 || err.status === 404)) return "unusable";
  return undefined;
}

// Whether the page, learning its ticket ran out, sends End once: voice takes a ticket for 15
// minutes past its cap for that, so the session ends at once and its summary runs. Not when it is
// already over, or when End itself came too late.
export function endsOnExpiry(why: Over, alreadyOver: boolean): boolean {
  return why === "expired" && !alreadyOver;
}

// What the page does when opening the session's audio failed with err and tries are left: for a
// session that is over, ended, expired or refused its ticket, no audio however often it asks.
export function afterAudioFailure(err: unknown, tries: number): "over" | "retry" | "text" {
  if (whyOver(err)) return "over";
  return tries > 0 ? "retry" : "text";
}

// The page found out its session is over, for why. alreadyOver says whether it knew before. If the
// ticket ran out and the page did not know, End goes once with that ticket; a second report finds
// the session already over and sends nothing more. Then markOver shows the session over.
export function reactToOver(
  alreadyOver: boolean,
  why: Over,
  end: () => Promise<unknown>,
  markOver: (why: Over) => void,
): void {
  if (endsOnExpiry(why, alreadyOver)) end().catch(() => {});
  markOver(why);
}

// Ends the session: the audio closes first, so a reply still playing is cut where the user stopped
// hearing it, as an interrupt is, then voice ends the session, then the page shows it over. A failed
// end leaves the session live, in text, for End to be pressed again.
export async function endSession(
  closeAudio: () => Promise<void>,
  end: () => Promise<Session>,
  markOver: () => void,
): Promise<void> {
  await closeAudio();
  await end();
  markOver();
}
