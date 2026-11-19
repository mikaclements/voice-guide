// One session the drop-in client holds on a product's page, joined with the session ID and ticket
// the product's server handed over: its audio connection, opened at once and reconnected with the
// same ticket, its transcript, typed turns, End, and the states it ends in. The page's own modules
// do the work; this wires them with no framework, so the script a product loads carries no React,
// and the element itself, as voice-guide's own page, only draws what this shows.
import { endSession as endOnVoice, ProblemError } from "./api.ts";
import { type Audio, type Closing, openAudio, type Playback, playAgent } from "./audio.ts";
import {
  afterAudioFailure,
  afterClosing,
  closings,
  endSession,
  microphoneRefused,
  type Over,
  overMessages,
  reactToOver,
  reconnectEveryMs,
  reconnectTries,
  whyOver,
} from "./ending.ts";
import { ReplySoundClock, shownReply } from "./replysound.ts";
import { Screen, type ScreenBlock } from "./screen.ts";
import { type Message, type Page, type Reply, Sender, transcriptPage } from "./sending.ts";
import { SpokenTurns } from "./spoken.ts";
import { httpTurns, type Turns } from "./turns.ts";

// What a session says on a page served over plain http, where the browser offers neither the
// microphone nor the random IDs every request carries.
export const insecureMessage = "voice-guide needs this page served over https";

// How long the microphone stays held for a session that never gets ready, as when its greeting's
// first sound never arrives: a round guess, room for a slow first model call before the greeting
// sounds (over 4 s seen); to be measured once the model starts with the session, which shortens it.
const holdMs = 30_000;

type State = {
  messages: Message[];
  notes: { error: string; audio: string; microphone: string };
  microphoneOn: boolean;
  // Muted by the user's own press; the microphone also sends silence while the session gets ready.
  muted: boolean;
  // The session isn't ready for the user's words yet: the agent's greeting is still to sound, or
  // voice hasn't said it is ready; the microphone holds muted meanwhile.
  gettingReady: boolean;
  // The browser refused to play the agent until the user interacts with the page.
  blocked: boolean;
  busy: boolean;
  over?: Over;
};

// What is known of a reply beyond its words, each part once known: the model that answered, how
// long the user waited in milliseconds, from a typed turn's press to its first text and sound, from
// the end of their speech to a spoken turn's first sound, and from the press that cut a reply short
// to its sound stopping; and whether it was lost or its audio stopped.
export type ReplyMeta = Pick<
  Reply,
  "model" | "firstTextMs" | "firstSoundMs" | "finishedToSoundMs" | "stoppedMs"
> & { lost?: true; audioFailed?: true };

function replyMeta(m: Message): ReplyMeta | undefined {
  const meta = {
    model: m.model,
    firstTextMs: m.firstTextMs,
    firstSoundMs: m.firstSoundMs,
    finishedToSoundMs: m.finishedToSoundMs,
    stoppedMs: m.heard === undefined ? undefined : m.stoppedMs,
    lost: m.ended === "lost" || undefined,
    audioFailed: m.audioFailed || undefined,
  };
  const known = Object.entries(meta).filter(([, v]) => v !== undefined);
  return known.length ? (Object.fromEntries(known) as ReplyMeta) : undefined;
}

// What a session shows: the transcript's lines, each by its message's ID, a reply's with what is
// known of it beyond its words, the notes, the microphone's state and the mute button's label (""
// with no microphone), whether the agent waits for a press to be heard, what a session over says,
// and whether a send or End can go now.
export type Shown = {
  lines: { id: string; who: "you" | "agent"; text: string; meta?: ReplyMeta }[];
  notes: string[];
  status: string;
  mute: string;
  blocked: boolean;
  over: string;
  locked: boolean;
  busy: boolean;
};

export function view(s: State, interrupts: boolean): Shown {
  const answering = s.messages.some((m) => m.who === "agent" && !m.ended);
  const live = !s.over;
  const mic = s.microphoneOn && live;
  const over = s.over ? overMessages[s.over] : "";
  const { error, audio, microphone } = s.notes;
  return {
    lines: s.messages.map((m) => {
      const text = [
        shownReply(m.text, m.heard) || (m.who === "agent" && !m.ended ? "…" : ""),
        m.live && "…",
        m.heard !== undefined && "(interrupted)",
      ]
        .filter(Boolean)
        .join(" ");
      const meta = m.who === "agent" ? replyMeta(m) : undefined;
      return { id: m.id, who: m.who, text, ...(meta && { meta }) };
    }),
    // A refusal's own words, once the session is over, say no more than the line that it is.
    notes: (live ? [error, audio, microphone] : [error]).filter((n) => n && n !== over),
    status: !mic
      ? ""
      : s.muted
        ? "Microphone muted: type, or unmute to talk"
        : s.gettingReady
          ? "Getting ready…"
          : answering
            ? "Answering"
            : "Listening",
    mute: !mic ? "" : s.muted ? "Unmute" : "Mute",
    blocked: s.blocked && live,
    over,
    // Over HTTP a turn sent while another is answered would be refused as busy.
    locked: s.busy || !live || (answering && !interrupts),
    busy: s.busy,
  };
}

// What a session is handed when joined, and tells back: what to show, the words of a
// send that wasn't taken, and, once, that the session is over, refused coming first with what made
// it unusable; and why voice refused a block of the screen, which the session goes on without.
export type Joining = {
  session: string;
  ticket: string;
  fetcher: typeof fetch;
  show(shown: Shown): void;
  giveBack(words: string): void;
  ended(reason: Over): void;
  refused(refusal: { message: string; status?: number; type?: string }): void;
  screenRefused?(refusal: { name: string; message: string }): void;
  // Once, when the session can first take the user's words, so the page can end its own loading.
  ready?(): void;
};

// One audio connection the session holds, with its playback once the agent's stream arrives; asked
// is set once the agent's opening was asked for on it.
type HeldAudio = { connection: Audio; playback?: Playback; asked?: boolean };

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

export class JoinedSession {
  private readonly deps: Joining;
  private readonly open: typeof openAudio;
  private readonly play: typeof playAgent;
  private state: State = {
    messages: [],
    notes: { error: "", audio: "", microphone: "" },
    microphoneOn: false,
    muted: false,
    gettingReady: false,
    blocked: false,
    busy: false,
  };
  // Set when the session is let go: nothing that arrives afterwards is shown or told, and a typed
  // turn still being answered over HTTP is called off.
  private left = false;
  private readonly stop = new AbortController();
  private audio: HeldAudio | undefined;
  private turns: Turns;
  private reconnect: ReturnType<typeof setTimeout> | undefined;
  // The cap on the microphone's hold while the session gets ready, while it lasts.
  private holdCap: ReturnType<typeof setTimeout> | undefined;
  // Whether the page was told the session is ready; it is told once, and nothing holds after.
  private readied = false;
  private ending: Promise<void> | undefined;
  // Reads the received audio for every connection the session has, one after another.
  private readonly clock = new ReplySoundClock();
  private readonly sender: Sender;
  private readonly spoken: SpokenTurns;
  // What the product's screen shows, which goes with the turns on whichever way they travel.
  private readonly screen: Screen;
  // Whether the agent's opening was asked for over HTTP, which happens once, when the first try at
  // the audio connection fails; each connection that opens asks on its data channel.
  private openedInText = false;

  constructor(
    deps: Joining,
    open: typeof openAudio = openAudio,
    play: typeof playAgent = playAgent,
  ) {
    this.deps = deps;
    this.open = open;
    this.play = play;
    this.screen = new Screen((name, message) => {
      if (!this.left) this.deps.screenRefused?.({ name, message });
    });
    this.turns = this.httpTurns();
    const page: Page = {
      ...transcriptPage(
        (change) => this.set({ messages: change(this.state.messages) }),
        (words, why) => {
          if (this.left) return;
          this.deps.giveBack(words);
          this.note({ error: why });
        },
      ),
      sessionOver: (why, err) => this.foundOver(why, err),
    };
    this.sender = new Sender(this.clock, page);
    this.spoken = new SpokenTurns(page, (words, id) => this.sender.keep(words, id), this.clock);
  }

  // Opens the session's audio; typing works over HTTP meanwhile, and whenever audio can't. A page
  // that isn't a secure context can't hold a session at all.
  join(secure: boolean): void {
    if (secure) {
      this.set({});
      void this.connect(reconnectTries);
    } else {
      this.note({ error: insecureMessage });
      this.markOver("unusable", insecureMessage);
    }
  }

  // What the product's screen shows now: each block that changed goes with the next turn.
  showScreen(blocks: readonly ScreenBlock[]): void {
    this.screen.set(blocks);
  }

  // Sends the user's words, unless a send can't go now; resolves once the turn and its audio end.
  send(words: string): Promise<void> | undefined {
    const said = words.trim();
    if (!said || view(this.state, this.turns.interrupts).locked) return undefined;
    this.note({ error: "" });
    return this.sender.send(said, this.turns, this.audio?.playback);
  }

  // Mutes the microphone, or unmutes it, so the user can stop the agent hearing itself or them;
  // muting carries over to the next connection. Unmuted while the session gets ready, it opens then.
  mute(): void {
    this.set({ muted: !this.state.muted });
    this.silence();
  }

  // The microphone sends silence while the user muted it or the session gets ready.
  private silence(): void {
    this.audio?.connection.microphone.mute(this.state.muted || this.state.gettingReady);
  }

  // Holds the microphone muted until the session is ready: the greeting's first sound, or the
  // opening ending without one once voice says the session is ready. Speech into
  // the silence before a greeting, while the model starts, would interrupt it at its first packet,
  // and the user would never hear it; once it sounds, the user cuts in as on any reply.
  private hold(): void {
    const cap = setTimeout(() => this.release(cap), holdMs);
    this.holdCap = cap;
    this.set({ gettingReady: true });
    this.silence();
  }

  // Ends the hold that cap belongs to, so an earlier hold's greeting sounding late opens nothing,
  // and, unless the audio closed under it, tells the page the session is ready.
  private release(cap: ReturnType<typeof setTimeout> | undefined, ready = true): void {
    if (cap === undefined || cap !== this.holdCap) return;
    clearTimeout(cap);
    this.holdCap = undefined;
    this.set({ gettingReady: false });
    this.silence();
    if (ready) this.becameReady();
  }

  private becameReady(): void {
    if (this.readied || this.left || this.state.over) return;
    this.readied = true;
    this.deps.ready?.();
  }

  // Plays the agent from the user's press, after the browser refused to without one.
  playAfterPress(): void {
    this.audio?.playback?.play?.();
  }

  // Ends the session: the audio closes first, so a reply still playing is cut where the user stopped
  // hearing it, then voice ends it. A failed End leaves it live, in text, to press again, and
  // rejects; an End already under way is the one answered.
  end(): Promise<void> {
    if (this.left || this.state.over) return Promise.resolve();
    this.ending ??= this.runEnd().finally(() => {
      this.ending = undefined;
    });
    return this.ending;
  }

  private async runEnd(): Promise<void> {
    this.set({ busy: true });
    try {
      await endSession(
        () => this.closeAudio(),
        () => endOnVoice(this.deps.ticket, this.deps.session, this.deps.fetcher),
        () => this.markOver("ended"),
      );
    } catch (e) {
      const why = whyOver(e);
      if (why) {
        this.markOver(why, e);
        return;
      }
      this.turns = this.httpTurns();
      this.note({ error: message(e) });
      throw e;
    } finally {
      this.set({ busy: false });
    }
  }

  // Lets the session go without ending it, as when the element leaves the page or is given another
  // session, or the user starts another on voice-guide's own page: the audio closes, a typed turn
  // and a reconnect under way are called off, and nothing heard afterwards is shown or told. The
  // session ends when the product's server ends it, or after it sits idle.
  leave(): void {
    this.left = true;
    clearTimeout(this.reconnect);
    this.stop.abort();
    this.closeAudio();
  }

  private httpTurns(): Turns {
    const signal = this.stop.signal;
    const fetcher: typeof fetch = (path, init) => this.deps.fetcher(path, { ...init, signal });
    return httpTurns(this.deps.session, this.deps.ticket, fetcher, this.screen);
  }

  private set(change: Partial<State>): void {
    if (this.left) return;
    this.state = { ...this.state, ...change };
    this.deps.show(view(this.state, this.turns.interrupts));
  }

  private note(change: Partial<State["notes"]>): void {
    this.set({ notes: { ...this.state.notes, ...change } });
  }

  // Stops the reply's sound at once and ends the audio connection; resolves once voice let it go.
  private closeAudio(): Promise<void> {
    this.audio?.playback?.stop();
    const closing = this.audio?.connection.close().catch(() => {});
    this.audio = undefined;
    // After the close, so letting go of the hold can't turn the closing microphone back on; the
    // session isn't ready for it, and the next connection holds again.
    this.release(this.holdCap, false);
    this.set({ microphoneOn: false, blocked: false });
    return closing ?? Promise.resolve();
  }

  // The session is over, shown by a refusal or by the audio closing. If its ticket ran out, End goes
  // once with that ticket, so the session ends now and its summary runs.
  private foundOver(why: Over, err?: unknown): void {
    if (this.left) return;
    reactToOver(
      !!this.state.over,
      why,
      () => endOnVoice(this.deps.ticket, this.deps.session, this.deps.fetcher),
      (w) => this.markOver(w, err),
    );
  }

  private markOver(why: Over, err?: unknown): void {
    if (this.left || this.state.over) return;
    this.closeAudio();
    this.set({ over: why, notes: { ...this.state.notes, audio: "" } });
    if (why === "unusable") {
      // Only voice's own refusal is told in its words; anything else, by its status alone.
      const problem = err instanceof ProblemError ? err : undefined;
      this.deps.refused({
        message: problem && !problem.fromVoice ? `Refused (${problem.status})` : message(err),
        ...(problem && { status: problem.status, type: problem.type }),
      });
    }
    this.deps.ended(why);
  }

  // Opens the session's audio, so typed turns go on its data channel and replies are heard; the
  // session stays in text while it can't, trying again tries times.
  private async connect(tries: number): Promise<void> {
    if (this.left || this.state.over) return;
    let mine: Audio | undefined;
    try {
      const connection = await this.open(
        this.deps.session,
        this.deps.ticket,
        (why) => {
          if (mine && this.audio?.connection === mine) this.afterAudioClosed(why); // else let go
        },
        {
          fetcher: this.deps.fetcher,
          onRefused: () => this.note({ microphone: microphoneRefused }),
          onUserSpeech: (id, words, follow, lost) =>
            this.spoken.onUserSpeech(id, words, follow, lost),
          screen: this.screen,
        },
      );
      mine = connection;
      if (this.left || this.state.over) {
        connection.close(); // let go, or over, while it opened
        return;
      }
      const opened: HeldAudio = { connection };
      connection.stream.then((stream) => this.startPlayback(opened, stream));
      // The session's first connection, before its opening: a reconnect once it has turns holds
      // nothing, and is ready when voice says so.
      if (!this.readied && !this.openedInText && !this.state.messages.length) this.hold();
      else if (!this.readied) void connection.ready.then(() => this.becameReady());
      connection.microphone.mute(this.state.muted || this.state.gettingReady);
      connection.microphone.given.then((given) => {
        if (this.audio === opened) this.set({ microphoneOn: given });
      });
      this.audio = opened;
      this.turns = connection.turns;
      this.note({ audio: "" });
    } catch (e) {
      this.afterConnectFailure(e, tries);
    }
  }

  // What the session does once voice closes the audio connection it holds, saying why.
  private afterAudioClosed(why: Closing): void {
    const next = afterClosing(why);
    if (next === "over") {
      this.foundOver(why === "expired" ? "expired" : "ended");
      return;
    }
    this.closeAudio();
    this.turns = this.httpTurns();
    this.note({ audio: closings[why] ?? closings.dropped });
    if (next === "reconnect") void this.connect(reconnectTries);
  }

  // Plays the agent's stream of a connection that is still the session's, and asks for the agent's
  // opening once the browser plays it.
  private startPlayback(opened: HeldAudio, stream: MediaStream): void {
    if (this.audio !== opened) return;
    opened.playback = this.play(
      stream,
      (loud, now) => this.clock.sample(loud, now),
      (blocked) => this.onPlaybackBlocked(opened, blocked),
    );
  }

  // The browser refuses to play the agent until a press, or plays it again after one.
  private onPlaybackBlocked(opened: HeldAudio, blocked: boolean): void {
    if (this.audio !== opened) return;
    this.set({ blocked });
    // The agent's opening, once the browser plays it, at once or from the user's press: a greeting
    // sent while it refused would be lost to the user, yet saved as heard. Again on each
    // connection, as after a drop: voice takes only the first, and refuses the rest. The greeting's
    // first sound ends the hold; an opening ending without one, as for an agent that waits, ends it
    // once voice says the session is ready.
    if (blocked || opened.asked) return;
    opened.asked = true;
    const cap = this.holdCap;
    const release = () => this.release(cap);
    const ready = opened.connection.ready;
    void this.sender.open(opened.connection.turns, release).finally(() => ready.then(release));
  }

  // Opening the audio failed with err: try again while tries are left, go on in text, or show the session over.
  private afterConnectFailure(err: unknown, tries: number): void {
    if (this.left) return;
    const next = afterAudioFailure(err, tries);
    if ((next === "retry" || next === "text") && !this.openedInText) {
      this.openedInText = true;
      void this.sender.open(this.turns); // shown, not heard, rather than waiting on the tries
      // In text the user waits on the first reply anyway, which shows as text.
      this.becameReady();
    }
    if (next === "over") this.foundOver(whyOver(err) ?? "ended", err);
    else if (next === "retry") {
      this.reconnect = setTimeout(() => this.connect(tries - 1), reconnectEveryMs);
    } else if (next === "text") {
      this.note({ audio: `No audio (${message(err)}); replies come as text.` });
    }
  }
}
