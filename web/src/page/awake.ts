// Keeps the device's display on while a session is live, with a screen wake lock (W3C Screen Wake
// Lock API): a phone that turns its display off stops the microphone, so voice hears silence and
// ends the session as idle. The browser lets go of the lock whenever the page is hidden, so it is
// asked for again each time the page shows while a session still wants it. Where the browser has
// no wake lock, or refuses one, the session goes on without it, with a quiet note.

// What the page says while the display may turn off during a session.
export const mayTurnOff =
  "This browser won't keep the display on: on a phone, the microphone stops when it turns off.";

// How long the display stays on with nothing new in the session: voice's default idle time, five
// minutes, and the minute its sweep may take to end the session. A session ended idle tells the page
// nothing until its next turn, so without this a phone left in a quiet room would stay lit. The page
// can't read voice's setting; a longer one lets the display turn off first, as it did without a lock.
export const quietMs = 6 * 60_000;

// The parts of the browser it uses, so tests hand it fakes.
export type Lock = { readonly released: boolean; release(): Promise<void> };
export type Locks = { wakeLock?: { request(type: "screen"): Promise<Lock> } };
export type Visibility = {
  readonly visibilityState: string;
  addEventListener(type: "visibilitychange", listener: () => void): void;
  removeEventListener(type: "visibilitychange", listener: () => void): void;
};

export class StayAwake {
  private readonly locks: Locks;
  private readonly page: Visibility;
  // Told mayTurnOff when the display may turn off, and "" once it won't or the session is over.
  private readonly told: (note: string) => void;
  // Whether a session wants the display on: from hold until letGo.
  private wanted = false;
  private lock: Lock | undefined;
  private asking = false;
  // Refused, or no wake lock, since the page last showed: asked again only at the next show, not
  // at every change the session shows.
  private refused = false;
  private quiet: ReturnType<typeof setTimeout> | undefined;
  private readonly shown = () => {
    if (this.page.visibilityState !== "visible") return;
    this.refused = false;
    this.ask();
  };

  constructor(locks: Locks, page: Visibility, told: (note: string) => void) {
    this.locks = locks;
    this.page = page;
    this.told = told;
  }

  // At a session's start and at each change it shows after, which keeps the display on for another
  // quietMs; asks for the lock only when it isn't held already.
  hold(): void {
    if (!this.wanted) this.page.addEventListener("visibilitychange", this.shown);
    this.wanted = true;
    clearTimeout(this.quiet);
    this.quiet = setTimeout(() => this.letGo(), quietMs);
    this.ask();
  }

  // Once the session is over, or quiet for quietMs, so the display turns off as it normally would.
  letGo(): void {
    if (!this.wanted) return;
    this.wanted = false;
    this.refused = false;
    clearTimeout(this.quiet);
    this.page.removeEventListener("visibilitychange", this.shown);
    this.lock?.release().catch(() => {});
    this.lock = undefined;
    this.told("");
  }

  private ask(): void {
    if (this.asking || this.refused || (this.lock && !this.lock.released)) return;
    const wakeLock = this.locks.wakeLock;
    if (!wakeLock) {
      this.refused = true;
      this.told(mayTurnOff);
      return;
    }
    this.asking = true;
    wakeLock
      .request("screen")
      .then(
        (lock) => {
          if (!this.wanted) {
            // The session ended while the lock was asked for.
            lock.release().catch(() => {});
            return;
          }
          this.lock = lock;
          this.told("");
        },
        // Refused, as with the battery low, or the page hidden as it asked.
        () => {
          if (!this.wanted) return;
          this.refused = true;
          this.told(mayTurnOff);
        },
      )
      .finally(() => {
        this.asking = false;
      });
  }
}
