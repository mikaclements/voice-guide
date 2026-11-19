// Turns the user speaks: their words show as their message while voice hears them; once voice takes
// the turn, its reply follows as a typed turn's does, with how long after they finished speaking its
// first sound went out.

import type { Follow } from "./audio.ts";
import type { ReplySoundClock } from "./replysound.ts";
import { failure, keepRequestId } from "./retry.ts";
import { type Page, showEnd, showSpoken } from "./sending.ts";

export class SpokenTurns {
  private readonly page: Page;
  private readonly keep: (words: string, requestId: string) => void;
  private readonly clock: ReplySoundClock | undefined;

  // keep holds on to the request ID of a turn that may have been taken, for the words' next send;
  // clock times the typed sends' sound, which a spoken turn taken cuts.
  constructor(
    page: Page,
    keep: (words: string, requestId: string) => void = () => {},
    clock?: ReplySoundClock,
  ) {
    this.page = page;
    this.keep = keep;
    this.clock = clock;
  }

  // voice heard words of the turn requestId; follow is set once it took the turn with them, and lost
  // once it couldn't hear them all and took none.
  userSpeech(requestId: string, words: string, follow?: Follow, lost?: boolean): void {
    const youId = `${requestId}:you`;
    if (follow) {
      const unspoke = this.clock?.spoke();
      this.answer(requestId, youId, `${requestId}:reply`, words, follow, unspoke);
    } else if (lost) this.page.giveBack(youId, `${requestId}:reply`, words, notCaught(words));
    else this.page.showLiveWords(youId, words);
  }

  private async answer(
    requestId: string,
    youId: string,
    replyId: string,
    words: string,
    follow: Follow,
    unspoke?: () => void,
  ) {
    this.page.add(youId, words, replyId);
    try {
      const end = await follow((text) =>
        this.page.update(replyId, (r) => ({ ...r, text: r.text + text })),
      );
      showEnd(this.page, replyId, end);
      const spoken = await end.spoken;
      if (spoken?.talkedOn) {
        // Nothing of it sounded, and the turn is taken again with all the words.
        unspoke?.();
        this.page.takeBack(youId, replyId);
        return;
      }
      showSpoken(this.page, replyId, spoken, true);
    } catch (err) {
      // Nothing of it plays, so a typed reply before it is still the one heard and timed.
      unspoke?.();
      // The words go to the box, to send again: with this turn's request ID when voice may have
      // taken it (the connection closed, or it failed before its reply), so it is taken once.
      if (keepRequestId(failure(err), false)) this.keep(words, requestId);
      this.page.giveBack(youId, replyId, words, err instanceof Error ? err.message : String(err));
    }
  }
}

// The line the page shows when voice couldn't hear all the user said: what was heard is in the box.
const notCaught = (words: string) =>
  words
    ? "voice didn't catch the rest of that: send it from the box, or say it again."
    : "voice didn't catch that: say it again, or type it.";
