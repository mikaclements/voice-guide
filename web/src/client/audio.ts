// The session's audio connection: WebRTC with voice, the user's microphone and the agent's speech
// on its audio track and typed turns on its data channel, set up with one request in the shape of
// WHIP (RFC 9725).
import { authorized, problemFrom } from "./api.ts";
import { TurnFailure } from "./retry.ts";
import { type Spoken, type TurnEnd, type Turns, turnBody } from "./turns.ts";

// Why the connection ended: replaced by another tab (no reconnect), ended, expired (the session's
// ticket ran out; no reconnect), stopping or dropped (a crash or the network, with no word from
// voice); the last two reconnect, to any server.
export type Closing = "replaced" | "stopping" | "ended" | "expired" | "dropped";

type Pending = {
  onText: (text: string) => void;
  started: boolean;
  ended?: (end: TurnEnd) => void;
  failed?: (err: TurnFailure) => void;
  spoken: (s: Spoken) => void;
};

// What each refusal means for the words: busy is another turn in flight, which is this one only if
// this send was a retry (see keepRequestId).
const refusals: Record<string, "taken" | "refused" | "busy"> = {
  repeated: "taken",
  busy: "busy",
};

// Follows the reply to a turn: its text as it arrives, then how it ended.
export type Follow = (onText: (text: string) => void) => Promise<TurnEnd>;

// What the page does with a turn the user speaks: shows the words as voice hears them, and, once
// voice takes the turn with them, follows its reply. No words, once final, means nothing was heard;
// lost, that voice couldn't hear it all and took no turn, so the words heard are the user's to send
// or say again.
type OnUserSpeech = (requestId: string, words: string, follow?: Follow, lost?: boolean) => void;

// Turns on the data channel: text as it arrives, the ending, then spoken, or interrupted when a turn
// sent meanwhile cut the reply short; the interrupted turn's last messages may come after the new
// turn is sent, so each turn waits under its request ID. A voice that doesn't take interrupts
// refuses a turn sent mid-reply as busy, and the page gives the words back. Turns the user speaks
// come from voice, under request IDs it makes, first as the words it hears.
export class ChannelTurns implements Turns {
  readonly interrupts = true;
  private readonly send: (msg: string) => void;
  private readonly onUserSpeech: OnUserSpeech;
  private readonly turns = new Map<string, Pending>();
  // The words of each spoken turn voice is still hearing, by request ID.
  private readonly speaking = new Map<string, string>();

  constructor(send: (msg: string) => void, onUserSpeech: OnUserSpeech = () => {}) {
    this.send = send;
    this.onUserSpeech = onUserSpeech;
  }

  take(
    words: string,
    requestId: string,
    onText: (text: string) => void,
    opening = false,
  ): Promise<TurnEnd> {
    // A retry sent while its first try still waits: the first try is the turn in flight.
    if (this.turns.has(requestId)) {
      return Promise.reject(new TurnFailure("busy", "this request is still waiting for its reply"));
    }
    const reply = this.follow(requestId, onText);
    this.send(JSON.stringify({ type: "turn", ...turnBody(requestId, words, opening) }));
    return reply;
  }

  private follow(requestId: string, onText: (text: string) => void): Promise<TurnEnd> {
    return new Promise((resolve, reject) => {
      let spoken: Pending["spoken"] = () => {};
      const done = new Promise<Spoken>((r) => {
        spoken = r;
      });
      this.turns.set(requestId, {
        onText,
        started: false,
        ended: (end) => resolve({ ...end, spoken: done }),
        failed: reject,
        spoken,
      });
    });
  }

  // Takes one message from voice; one about no turn the page is waiting on is ignored.
  receive(raw: string): void {
    const m = JSON.parse(raw);
    if (m.type === "hearing") {
      const words = String(m.text ?? "");
      const taken = m.final && !m.failed && words;
      const follow: Follow | undefined = taken
        ? (onText) => this.follow(m.request_id, onText)
        : undefined;
      if (m.final) this.speaking.delete(m.request_id);
      else this.speaking.set(m.request_id, words);
      this.onUserSpeech(m.request_id, words, follow, Boolean(m.failed));
      return;
    }
    const t = this.turns.get(m.request_id);
    if (!t) return;
    switch (m.type) {
      case "text":
        t.started = true;
        t.onText(m.text);
        break;
      case "done":
      case "lost":
        t.ended?.({ ended: m.type, turnId: m.turn_id, seq: m.seq, model: m.model });
        t.ended = t.failed = undefined;
        break;
      case "refused":
        this.fail(m.request_id, refusals[m.reason] ?? "refused", m.detail || m.reason);
        break;
      case "failed":
        this.fail(m.request_id, "unsure", "the turn failed before its reply; send it again");
        break;
      case "spoken":
        t.spoken({ failed: Boolean(m.failed), ...firstAudio(m) });
        this.turns.delete(m.request_id);
        break;
      case "interrupted":
        // Voice sends the turn's ending first; settling it here too is only a safeguard, as done,
        // since voice saved it answered as far as it went.
        t.ended?.({ ended: "done" });
        t.spoken({ failed: false, heard: String(m.heard ?? ""), ...firstAudio(m) });
        this.turns.delete(m.request_id);
        break;
      case "talking_on":
        // The turn is heard again, under the same request ID, and taken once the user finishes.
        t.ended?.({ ended: "done" });
        t.spoken({ failed: false, talkedOn: true });
        this.turns.delete(m.request_id);
        break;
    }
  }

  // The data channel closed: a reply that had started is lost; one that hadn't may have been taken.
  // Words still being heard are the user's to send or say again: no turn was taken with them.
  closed(): void {
    for (const [requestId, words] of this.speaking)
      this.onUserSpeech(requestId, words, undefined, true);
    this.speaking.clear();
    for (const t of this.turns.values()) {
      if (t.started) t.ended?.({ ended: "lost" });
      else t.failed?.(new TurnFailure("unsure", "the audio connection closed"));
      t.spoken({ failed: true });
    }
    this.turns.clear();
  }

  private fail(requestId: string, kind: TurnFailure["kind"], message: string) {
    this.turns.get(requestId)?.failed?.(new TurnFailure(kind, message));
    this.turns.delete(requestId);
  }
}

// For a turn the user spoke, how long after their speech ended voice sent the reply's first audio.
const firstAudio = (m: { first_audio_ms?: number }) =>
  m.first_audio_ms === undefined ? {} : { firstAudioMs: m.first_audio_ms };

export type Audio = {
  turns: ChannelTurns;
  stream: Promise<MediaStream>;
  microphone: Microphone;
  close(): Promise<void>;
};

// How long the page waits for its own addresses and for the data channel to open. Both take
// milliseconds on one machine; past these, something is wrong and the page goes on in text.
const gatherWaitMs = 2000;
const openWaitMs = 10_000;
// A disconnected connection that comes back, after a network change say, does so within a few
// seconds; past 3s the server is taken as gone and the page reconnects, to any server.
const recoverWaitMs = 3000;

// Asks for the microphone, with the browser's echo canceller on so the agent's voice, which arrives
// over the connection, is taken out of what the user says and they can talk over it; noise
// suppression and gain control keep speech even for speech detection. Undefined when the user
// refuses, there is no microphone, or the page can't ask: the session goes on in typing. A refusal,
// by the user or the browser's settings, calls onRefused, so the page can say how to allow it.
export async function microphone(
  media: Pick<MediaDevices, "getUserMedia"> | undefined,
  onRefused: () => void = () => {},
): Promise<MediaStream | undefined> {
  if (!media) return undefined;
  try {
    return await media.getUserMedia({
      audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
    });
  } catch (e) {
    if (e instanceof DOMException && e.name === "NotAllowedError") onRefused();
    return undefined;
  }
}

// How the page reaches voice and the microphone, hears that the microphone was refused, and what
// voice hears the user say.
export type AudioOptions = {
  fetcher?: typeof fetch;
  media?: Pick<MediaDevices, "getUserMedia">;
  onRefused?: () => void;
  onUserSpeech?: OnUserSpeech;
};

// The microphone on an audio connection, stopped with it: at once, or as soon as the user gives it.
// given says whether the user gave one; muted, it sends silence, so voice hears nothing.
export type Microphone = { stop(): void; mute(on: boolean): void; given: Promise<boolean> };

// Offers audio both ways at once, with no track yet, so neither the connection nor typing waits on
// the microphone prompt, which the user may leave unanswered. A microphone given joins the same
// transceiver, which needs no new offer; one refused or missing leaves it without a track, so only
// the agent's voice flows.
export function sendMicrophone(
  pc: Pick<RTCPeerConnection, "addTransceiver">,
  mic: Promise<MediaStream | undefined>,
): Microphone {
  const { sender } = pc.addTransceiver("audio", { direction: "sendrecv" });
  let stopped = false;
  let muted = false;
  let given: MediaStream | undefined;
  let track: MediaStreamTrack | undefined;
  const stopTracks = () => {
    for (const t of given?.getTracks() ?? []) t.stop();
  };
  const sent = mic.then((stream) => {
    given = stream;
    if (stopped) {
      stopTracks();
      return false;
    }
    track = stream?.getAudioTracks()[0];
    if (!track) return false;
    track.enabled = !muted;
    sender.replaceTrack(track).catch(() => {});
    return true;
  });
  return {
    stop() {
      stopped = true;
      stopTracks();
    },
    mute(on) {
      muted = on;
      if (track) track.enabled = !on;
    },
    given: sent,
  };
}

// Opens the session's audio; the offer carries all the page's addresses, as voice takes none later.
export async function openAudio(
  sessionId: string,
  ticket: string,
  onClosing: (why: Closing) => void,
  { fetcher = fetch, media = navigator.mediaDevices, onRefused, onUserSpeech }: AudioOptions = {},
): Promise<Audio> {
  const pc = new RTCPeerConnection();
  const mic = sendMicrophone(pc, microphone(media, onRefused));
  // The microphone stops with the connection, so the browser stops recording and says so.
  const end = () => {
    pc.close();
    mic.stop();
  };
  // Made before the offer, since an offer without a data channel gets none.
  const dc = pc.createDataChannel("turns");
  const turns = new ChannelTurns((msg) => dc.send(msg), onUserSpeech);
  let told = false; // voice said why it closed, or the page closed it
  dc.onmessage = (e) => {
    told = onChannelMessage(e.data, turns, onClosing) || told;
  };
  const drop = () => {
    turns.closed();
    if (told) return;
    told = true;
    onClosing("dropped");
    end();
  };
  dc.onclose = drop;
  watchForDrop(pc, drop);
  const stream = new Promise<MediaStream>((resolve) => {
    pc.ontrack = (e) => resolve(e.streams[0] ?? new MediaStream([e.track]));
  });
  try {
    await pc.setLocalDescription(await pc.createOffer());
    await until(
      () => pc.iceGatheringState === "complete",
      pc,
      "icegatheringstatechange",
      gatherWaitMs,
    );
    const path = `/v1/sessions/${sessionId}/audio`;
    const res = await authorized(
      ticket,
      fetcher,
      "POST",
      path,
      pc.localDescription?.sdp,
      "application/sdp",
    );
    if (res.status !== 201) throw await problemFrom(res);
    const location = res.headers.get("Location") ?? "";
    await pc.setRemoteDescription({ type: "answer", sdp: await res.text() });
    if (!(await until(() => dc.readyState === "open", dc, "open", openWaitMs))) {
      throw new Error("the audio connection didn't open");
    }
    return {
      turns,
      stream,
      microphone: mic,
      async close() {
        told = true;
        // Closing here fires no close event, so a turn still waiting is settled here.
        turns.closed();
        end();
        if (location) await authorized(ticket, fetcher, "DELETE", location).catch(() => {});
      },
    };
  } catch (e) {
    told = true;
    end();
    throw e;
  }
}

// Takes one message from voice; true when voice says why it closes the connection, which settles a
// waiting turn at once, since the page's own close fires no close event.
export function onChannelMessage(
  raw: string,
  turns: ChannelTurns,
  onClosing: (why: Closing) => void,
): boolean {
  const m = JSON.parse(raw);
  if (m.type !== "closing") {
    turns.receive(raw);
    return false;
  }
  turns.closed();
  onClosing(m.reason);
  return true;
}

// Calls drop once the connection has failed, or stayed disconnected for recoverWaitMs. A server
// that dies never closes the data channel: Chrome only marks the connection disconnected, after
// about 5s without an answer, and failed much later.
export function watchForDrop(
  pc: Pick<RTCPeerConnection, "connectionState" | "onconnectionstatechange">,
  drop: () => void,
) {
  let gone: ReturnType<typeof setTimeout> | undefined;
  pc.onconnectionstatechange = () => {
    clearTimeout(gone);
    if (pc.connectionState === "failed") drop();
    if (pc.connectionState === "disconnected") gone = setTimeout(drop, recoverWaitMs);
  };
}

// Resolves true once ready() holds, checked on each of target's events, or false after waitMs.
function until(
  ready: () => boolean,
  target: EventTarget,
  event: string,
  waitMs: number,
): Promise<boolean> {
  return new Promise((resolve) => {
    if (ready()) return resolve(true);
    const check = () => ready() && finish(true);
    const timer = setTimeout(() => finish(false), waitMs);
    const finish = (ok: boolean) => {
      clearTimeout(timer);
      target.removeEventListener(event, check);
      resolve(ok);
    };
    target.addEventListener(event, check);
  });
}

// Above this, a sample is sound, not silence: 0.01 is -40 dB below full scale, under the quietest
// speech and above the near-silence between words and at a reply's start.
const soundLevel = 0.01;
// How often the page looks for sound: 10 ms, half a packet, so first sound is timed to within it.
const levelEveryMs = 10;

// The page's playback of the agent's audio; muting it leaves the readings, taken from the track.
// play starts it again, from a press, after the browser refused it.
export type Playback = { stop(): void; mute(on: boolean): void; play?(): void };

// Plays the agent's audio and passes on a reading of its level every levelEveryMs. onBlocked hears
// whether the browser refuses to play it until the user has interacted with the page.
export function playAgent(
  stream: MediaStream,
  onLevel: (loud: boolean, now: number) => void,
  onBlocked: (blocked: boolean) => void = () => {},
): Playback {
  // Played by an element, which also makes Chrome feed the stream to the analyser.
  const element = new Audio();
  element.srcObject = stream;
  const play = () =>
    element.play().then(
      () => onBlocked(false),
      (e) => e instanceof DOMException && e.name === "NotAllowedError" && onBlocked(true),
    );
  play();
  const context = new AudioContext();
  const analyser = context.createAnalyser();
  // 512 samples, about 11 ms at 48 kHz: sound lingers in the window for its length, so a longer one
  // would time a stop late; it still spans the 10 ms between readings, so no sound falls between.
  analyser.fftSize = 512;
  context.createMediaStreamSource(stream).connect(analyser);
  const samples = new Float32Array(analyser.fftSize);
  const timer = setInterval(() => {
    analyser.getFloatTimeDomainData(samples);
    const loud = samples.some((s) => Math.abs(s) > soundLevel);
    onLevel(loud, performance.now());
  }, levelEveryMs);
  return {
    stop() {
      clearInterval(timer);
      element.srcObject = null;
      context.close().catch(() => {});
    },
    mute(on) {
      element.muted = on;
    },
    play() {
      context.resume().catch(() => {});
      play();
    },
  };
}
