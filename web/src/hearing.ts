// When a reply's first sound comes after a send and, for a send while the agent was sounding, how
// long after it that sound stopped, from readings of the received track: the page mutes its own
// playback at the press, so they show how fast voice stopped.

// Silence this long ends a reply's sound: longer than the pauses inside a reply, shorter than the
// second or more before the next reply can sound. A round guess, until pauses in real replies are
// measured.
export const quietMs = 500;

export class Hearing {
  private loudAt = Number.NEGATIVE_INFINITY; // the last reading with sound
  private sounding = false;
  private firstSound: (() => void) | undefined;
  private stopping: { sentAt: number; stopped: (ms: number) => void } | undefined;

  // The user pressed send at now: firstSound is called at the reply's first sound, and stopped with
  // how long after now the agent's sound stopped, if it was sounding then. A stop already being
  // timed stays: it is the reply that was sounding, interrupted by the earlier press. Returns an
  // undo for a send that wasn't taken, so the reply before it is heard and timed as its own.
  sent(now: number, firstSound: () => void, stopped: (ms: number) => void): () => void {
    const before = { firstSound: this.firstSound, stopping: this.stopping };
    this.firstSound = firstSound;
    this.stopping ??= now - this.loudAt < quietMs ? { sentAt: now, stopped } : undefined;
    const mine = { firstSound: this.firstSound, stopping: this.stopping };
    return () => {
      if (this.firstSound === mine.firstSound) this.firstSound = before.firstSound;
      if (this.stopping === mine.stopping) this.stopping = before.stopping;
    };
  }

  // voice took a turn the user spoke, which cuts any reply still to sound: the next sound is its
  // reply's, not an earlier send's. Returns an undo for a spoken turn voice then refused, so the
  // reply before it is heard and timed as its own.
  spoke(): () => void {
    const before = this.firstSound;
    this.firstSound = undefined;
    return () => {
      this.firstSound ??= before;
    };
  }

  // One reading of the received audio at now: loud when it holds sound.
  sample(loud: boolean, now: number): void {
    if (loud) {
      // Until the earlier reply's sound has stopped, sound is still that reply's.
      if (!this.sounding && !this.stopping) {
        this.firstSound?.();
        this.firstSound = undefined;
      }
      this.loudAt = now;
    } else if (this.stopping && now - this.loudAt >= quietMs) {
      this.stopping.stopped(Math.max(0, this.loudAt - this.stopping.sentAt));
      this.stopping = undefined;
    }
    this.sounding = loud;
  }
}

// The reply as the page shows it: when the user interrupted it, only as far as they heard it,
// ending with a dash where it was cut, as the agent's next prompt shows it.
export function shownReply(text: string, heard: string | undefined): string {
  if (heard === undefined) return text;
  const kept = heard.trimEnd();
  return kept ? `${kept}—` : "";
}
