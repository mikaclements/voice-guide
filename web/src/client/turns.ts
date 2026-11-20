// A turn: the words that go to voice, the reply's text that streams back over HTTP, and how it
// ended and was timed.
import { authorized, problemFrom } from "./api.ts";
import { EventParser } from "./events.ts";
import type { Screen } from "./screen.ts";

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

// Turns over HTTP, each carrying the screen's blocks voice hasn't saved with a turn yet.
export function httpTurns(
  sessionId: string,
  ticket: string,
  fetcher: typeof fetch = fetch,
  screen?: Screen,
): Turns {
  return {
    interrupts: false,
    async take(words, requestId, onText, opening) {
      const blocks = screen?.pending() ?? [];
      const res = await authorized(ticket, fetcher, "POST", `/v1/sessions/${sessionId}/turns`, {
        ...turnBody(requestId, words, opening),
        ...(blocks.length > 0 && { screen_input: blocks }),
      });
      if (!res.ok || !res.body) throw await problemFrom(res);
      // The stream starts only once voice has saved the turn, so its blocks went with it.
      screen?.carried(blocks);
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
