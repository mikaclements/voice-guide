// One send of the user's words and what the page does around it: muting the reply it interrupts,
// timing it, keeping its request ID when it may have been taken, and what the page shows.

import type { Playback } from "./audio.ts";
import { type Over, whyOver } from "./ending.ts";
import type { ReplySoundClock } from "./replysound.ts";
import { failure, keepRequestId, requestIdFor } from "./retry.ts";
import { type Spoken, startTimer, type TurnEnd, type Turns } from "./turns.ts";

// A reply as the page shows it.
export type Reply = {
  text: string;
  firstTextMs?: number;
  firstSoundMs?: number;
  ended?: TurnEnd["ended"];
  model?: string;
  audioFailed?: boolean;
  // Set when the user interrupted the reply: its text as far as they heard it.
  heard?: string;
  // How long after the interrupting press the reply's sound stopped.
  stoppedMs?: number;
  // For a turn the user spoke, how long after they finished speaking its first sound went out.
  finishedToSoundMs?: number;
};

// What a send shows, by message ID: an interrupted reply's last messages come after the next turn's.
export interface Page {
  // The user's words and the reply to them, after the messages before; a message of the user's
  // already shown under youId moves there.
  add(youId: string, words: string, replyId: string): void;
  // The user's words as voice hears them while they speak; none takes the message away.
  showLiveWords(youId: string, words: string): void;
  // The agent's opening, after the messages before, with no message of the user's before it.
  open(replyId: string): void;
  update(replyId: string, change: (r: Reply) => Reply): void;
  // The words weren't taken, or may not have been: their messages go and the words go back.
  giveBack(youId: string, replyId: string, words: string, why: string): void;
  // The user spoke on before the reply sounded: the reply goes, and their words show as being
  // heard again.
  takeBack(youId: string, replyId: string): void;
  // Voice refused a send because the session is over: ended, its ticket run out, or refused, with
  // the refusal itself.
  sessionEnded?(why: Over, err: unknown): void;
}

// One message of a page's transcript; live marks the user's words while voice is still hearing them.
export type Message = Reply & { id: string; who: "you" | "agent"; live?: boolean };

// The page over a transcript kept as a list of messages, which edit changes; the words of a send
// given back go to giveBack, with why.
export function transcriptPage(
  edit: (change: (ms: Message[]) => Message[]) => void,
  giveBack: (words: string, why: string) => void,
): Page {
  return {
    add: (youId, said, replyId) =>
      edit((ms) => [
        ...ms.filter((m) => m.id !== youId),
        { id: youId, who: "you", text: said },
        { id: replyId, who: "agent", text: "" },
      ]),
    open: (replyId) => edit((ms) => [...ms, { id: replyId, who: "agent", text: "" }]),
    showLiveWords: (youId, said) =>
      edit((ms) => {
        if (!said) return ms.filter((m) => m.id !== youId);
        if (!ms.some((m) => m.id === youId))
          return [...ms, { id: youId, who: "you", text: said, live: true }];
        return ms.map((m) => (m.id === youId ? { ...m, text: said } : m));
      }),
    update: (id, change) => edit((ms) => ms.map((m) => (m.id === id ? { ...m, ...change(m) } : m))),
    giveBack: (youId, replyId, said, why) => {
      edit((ms) => ms.filter((m) => m.id !== youId && m.id !== replyId));
      giveBack(said, why);
    },
    takeBack: (youId, replyId) =>
      edit((ms) =>
        ms.filter((m) => m.id !== replyId).map((m) => (m.id === youId ? { ...m, live: true } : m)),
      ),
  };
}

// Shows on the reply how its turn ended and with which model.
export function showEnd(page: Page, replyId: string, end: TurnEnd): void {
  page.update(replyId, (r) => ({ ...r, ended: end.ended, model: end.model }));
}

// Shows on the reply how its audio went: that it stopped, and how far the user heard it. For a turn
// the user spoke, also how long after they finished speaking its first sound went out.
export function showSpoken(
  page: Page,
  replyId: string,
  spoken: Spoken | undefined,
  spokenTurn = false,
): void {
  page.update(replyId, (r) => ({
    ...r,
    ...(spoken?.failed && { audioFailed: true }),
    ...(spoken?.heard !== undefined && { heard: spoken.heard }),
    ...(spokenTurn &&
      spoken?.firstAudioMs !== undefined && { finishedToSoundMs: spoken.firstAudioMs }),
  }));
}

export class Sender {
  private readonly clock: ReplySoundClock;
  private readonly page: Page;
  private readonly now: () => number;
  private readonly newId: () => string;
  // The request ID of each send that may have been taken, by its words: whichever of them is in the
  // box when the user sends it again repeats its own ID. A few at most: one per send that failed.
  private readonly kept = new Map<string, string>();
  // The latest reply: a press while sound plays interrupts it, unless an earlier press's stop is
  // still being timed, which ReplySoundClock keeps with the reply that was sounding.
  private lastReply = "";

  constructor(
    clock: ReplySoundClock,
    page: Page,
    now: () => number = () => performance.now(),
    newId: () => string = () => crypto.randomUUID(),
  ) {
    this.clock = clock;
    this.page = page;
    this.now = now;
    this.newId = newId;
  }

  // Keeps the request ID of a spoken turn that may have been taken, whose words went back to the box:
  // sent again, they repeat it, so voice takes them once. An ID already kept for the same words, a
  // typed send's, stays.
  keep(words: string, requestId: string): void {
    if (!this.kept.has(words)) this.kept.set(words, requestId);
  }

  // Asks for the agent's opening; resolves once it and its audio have ended. Its reply shows once
  // its first text arrives, so an opening voice doesn't take, as for an agent that waits, after a
  // reload or once the user has spoken, shows nothing, and goes without a word, as does one that
  // fails: the agent then waits for the user. sounded, if given, is called at its first sound.
  async open(turns: Turns, sounded?: () => void): Promise<void> {
    const timer = startTimer(this.now);
    const replyId = this.newId();
    const before = this.lastReply;
    this.lastReply = replyId;
    const unsend = this.clock.sent(
      this.now(),
      () => {
        this.page.update(replyId, (r) => ({ ...r, firstSoundMs: r.firstSoundMs ?? timer.sound() }));
        sounded?.();
      },
      () => {},
    );
    let shown = false;
    try {
      const end = await turns.take(
        "",
        this.newId(),
        (text) => {
          if (!shown) this.page.open(replyId);
          shown = true;
          const ms = timer.text();
          this.page.update(replyId, (r) => ({ ...r, text: r.text + text, firstTextMs: ms }));
        },
        true,
      );
      showEnd(this.page, replyId, end);
      showSpoken(this.page, replyId, await end.spoken);
    } catch (err) {
      unsend();
      if (this.lastReply === replyId) this.lastReply = before;
      const over = whyOver(err);
      if (over) this.page.sessionEnded?.(over, err);
    }
  }

  // Sends words; resolves once the turn and its audio have ended.
  async send(words: string, turns: Turns, playback: Playback | undefined): Promise<void> {
    const keptId = this.kept.get(words);
    const requestId = requestIdFor(
      words,
      keptId === undefined ? undefined : { key: words, requestId: keptId },
    );
    const retry = keptId !== undefined;
    // The clock starts at the press, so the times shown are the times the user waits.
    const timer = startTimer(this.now);
    const youId = this.newId();
    const replyId = this.newId();
    const before = this.lastReply;
    this.lastReply = replyId;
    const unsend = this.clock.sent(
      this.now(),
      () => {
        const ms = timer.sound();
        this.page.update(replyId, (r) => ({ ...r, firstSoundMs: r.firstSoundMs ?? ms }));
      },
      (ms) => this.page.update(before, (r) => ({ ...r, stoppedMs: ms })),
    );
    // Silent at the press, without waiting for voice to stop the reply being interrupted; the sound
    // comes back with this turn's first text, long after that reply's last packet.
    const muted = turns.interrupts ? playback : undefined;
    muted?.mute(true);
    let silent = Boolean(muted);
    const unmute = () => {
      if (silent) muted?.mute(false);
      silent = false;
    };
    this.page.add(youId, words, replyId);
    try {
      const end = await turns.take(words, requestId, (text) => {
        unmute();
        const ms = timer.text();
        this.page.update(replyId, (r) => ({ ...r, text: r.text + text, firstTextMs: ms }));
      });
      unmute();
      // Only this request's: another send may have kept its own meanwhile.
      if (this.kept.get(words) === requestId) this.kept.delete(words);
      showEnd(this.page, replyId, end);
      showSpoken(this.page, replyId, await end.spoken);
    } catch (err) {
      unmute();
      // Nothing of this send plays, so the reply before it is still the one heard and timed.
      unsend();
      if (this.lastReply === replyId) this.lastReply = before;
      const f = failure(err);
      if (keepRequestId(f, retry)) this.kept.set(words, requestId);
      else if (this.kept.get(words) === requestId) this.kept.delete(words);
      if (f === "taken") {
        // An earlier try took these words; voice doesn't replay its reply, so it shows as lost.
        this.page.update(replyId, (r) => ({ ...r, ended: "lost" }));
      } else {
        this.page.giveBack(youId, replyId, words, err instanceof Error ? err.message : String(err));
      }
      const over = whyOver(err);
      if (over) this.page.sessionEnded?.(over, err);
    }
  }
}
