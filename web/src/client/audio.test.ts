import assert from "node:assert/strict";
import { test } from "node:test";
import { failure } from "./api.ts";
import {
  ChannelTurns,
  microphone,
  onChannelMessage,
  openAudio,
  sendMicrophone,
  watchForDrop,
} from "./audio.ts";
import { startTimer } from "./turns.ts";

// A browser's media devices whose microphone prompt the test answers, or never does, recording what
// was asked and which tracks were stopped.
function devices() {
  const asked: MediaStreamConstraints[] = [];
  const stopped: MediaStreamTrack[] = [];
  const track = { kind: "audio", stop: () => stopped.push(track) } as unknown as MediaStreamTrack;
  const stream = {
    getAudioTracks: () => [track],
    getTracks: () => [track],
  } as unknown as MediaStream;
  let answer: (grant: boolean) => void = () => {};
  let fail: (why: string) => void = () => {};
  const media = {
    getUserMedia(c?: MediaStreamConstraints) {
      asked.push(c ?? {});
      return new Promise<MediaStream>((resolve, reject) => {
        fail = (why) => reject(new DOMException(why, why));
        answer = (grant) => (grant ? resolve(stream) : fail("NotAllowedError"));
      });
    },
  };
  return {
    asked,
    stopped,
    track,
    stream,
    media,
    answer: (grant: boolean) => answer(grant),
    // The browser finds no microphone.
    missing: () => fail("NotFoundError"),
  };
}

// Lets the callbacks of promises already settled run.
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

// A peer connection's audio: the transceivers the page added, and the tracks it sent on them.
function offering() {
  const added: unknown[][] = [];
  const sent: unknown[] = [];
  const pc = {
    addTransceiver(...a: unknown[]) {
      added.push(a);
      return {
        sender: {
          replaceTrack: async (t: unknown) => {
            sent.push(t);
          },
        },
      };
    },
  } as never;
  return { added, sent, pc };
}

test("the microphone is asked for with echo cancellation, noise suppression and gain control", async () => {
  const { asked, stream, media, answer } = devices();
  const mic = microphone(media);
  answer(true);
  assert.equal(await mic, stream);
  assert.deepEqual(asked, [
    { audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } },
  ]);
});

test("audio is offered both ways at once, without waiting for the microphone prompt", async () => {
  const { media } = devices(); // the prompt is never answered
  const { added, sent, pc } = offering();
  sendMicrophone(pc, microphone(media));
  await settle();
  assert.deepEqual(added, [["audio", { direction: "sendrecv" }]]);
  assert.deepEqual(sent, [], "nothing to send until the user answers");
});

test("a microphone given joins the audio already offered, with no new offer", async () => {
  const { track, media, answer } = devices();
  const { added, sent, pc } = offering();
  sendMicrophone(pc, microphone(media));
  answer(true);
  await settle();
  assert.equal(added.length, 1);
  assert.deepEqual(sent, [track]);
});

test("a microphone refused or missing leaves the audio offered without a track", async () => {
  const refusing = devices();
  for (const mic of [microphone(refusing.media), microphone(undefined)]) {
    const { sent, pc } = offering();
    sendMicrophone(pc, mic);
    refusing.answer(false);
    await settle();
    assert.equal(await mic, undefined);
    assert.deepEqual(sent, []);
  }
});

test("a microphone the user refuses is said to be refused, and one missing isn't", async () => {
  const refusing = devices();
  const missing = devices();
  let refused = 0;
  const asked = [
    microphone(refusing.media, () => refused++),
    microphone(missing.media, () => refused++),
  ];
  refusing.answer(false);
  missing.missing();
  await Promise.all([...asked, microphone(undefined, () => refused++)]);
  assert.equal(refused, 1);
});

test("opening the audio passes on that the microphone was refused", { timeout: 2000 }, async () => {
  const { media, answer } = devices();
  let refused = false;
  const opening = openAudio("s1", ticket, () => {}, {
    fetcher: voice(201) as typeof fetch,
    media,
    onRefused: () => {
      refused = true;
    },
  });
  answer(false);
  await (await opening).close();
  assert.equal(refused, true);
});

test("a muted microphone sends silence, one given later too, until unmuted", async () => {
  const { track, media, answer } = devices();
  const { pc } = offering();
  const mic = sendMicrophone(pc, microphone(media));
  mic.mute(true);
  answer(true);
  assert.equal(await mic.given, true);
  assert.equal(track.enabled, false);
  mic.mute(false);
  assert.equal(track.enabled, true);
});

test("a microphone given after its connection stopped is stopped and never sent", async () => {
  const { stopped, track, media, answer } = devices();
  const { sent, pc } = offering();
  sendMicrophone(pc, microphone(media)).stop();
  answer(true);
  await settle();
  assert.deepEqual(stopped, [track]);
  assert.deepEqual(sent, []);
});

// The browser's peer connection, as far as openAudio uses it: its addresses gathered and its data
// channel open at once.
class FakePeer {
  static last: FakePeer;
  closed = false;
  iceGatheringState = "complete";
  connectionState = "connected";
  localDescription = { type: "offer", sdp: "offer" };
  onconnectionstatechange: (() => void) | null = null;
  ontrack: unknown = null;
  dc = {
    readyState: "open",
    send() {},
    onmessage: null as unknown,
    onclose: null as (() => void) | null,
    addEventListener() {},
    removeEventListener() {},
  };
  constructor() {
    FakePeer.last = this;
  }
  addTransceiver() {
    return { sender: { replaceTrack: async () => {} } };
  }
  createDataChannel() {
    return this.dc;
  }
  async createOffer() {
    return this.localDescription;
  }
  async setLocalDescription() {}
  async setRemoteDescription() {}
  addEventListener() {}
  removeEventListener() {}
  close() {
    this.closed = true;
  }
}
globalThis.RTCPeerConnection = FakePeer as unknown as typeof RTCPeerConnection;

// Voice answering the offer with status, and ending a connection with 200.
const voice = (status: number) => async (_: unknown, init?: RequestInit) =>
  init?.method === "DELETE"
    ? new Response(null, { status: 200 })
    : new Response(status === 201 ? "answer" : "", {
        status,
        headers: { Location: "/v1/sessions/s1/audio/c1" },
      });
const ticket = "a ticket";

test("the audio connection opens while the microphone prompt waits unanswered", {
  timeout: 2000,
}, async () => {
  const { media } = devices();
  const audio = await openAudio("s1", ticket, () => {}, {
    fetcher: voice(201) as typeof fetch,
    media,
  });
  await audio.close();
});

test("the microphone stops when the page closes the connection", { timeout: 2000 }, async () => {
  const { stopped, track, media, answer } = devices();
  const audio = await openAudio("s1", ticket, () => {}, {
    fetcher: voice(201) as typeof fetch,
    media,
  });
  answer(true);
  await settle();
  await audio.close();
  assert.deepEqual(stopped, [track]);
});

test("the microphone stops when the connection drops", { timeout: 2000 }, async () => {
  const { stopped, track, media, answer } = devices();
  const closings: string[] = [];
  await openAudio("s1", ticket, (why) => closings.push(why), {
    fetcher: voice(201) as typeof fetch,
    media,
  });
  answer(true);
  await settle();
  FakePeer.last.dc.onclose?.();
  assert.deepEqual(closings, ["dropped"]);
  assert.deepEqual(stopped, [track]);
});

test("the microphone stops when the connection fails to open, even one given afterwards", {
  timeout: 2000,
}, async () => {
  const { stopped, track, media, answer } = devices();
  await assert.rejects(
    openAudio("s1", ticket, () => {}, { fetcher: voice(503) as typeof fetch, media }),
  );
  answer(true);
  await settle();
  assert.equal(FakePeer.last.closed, true);
  assert.deepEqual(stopped, [track]);
});

// A data channel's page side: what the page sent, and a way to answer as voice.
function channel() {
  const sent: unknown[] = [];
  const turns = new ChannelTurns((msg) => sent.push(JSON.parse(msg)));
  const say = (m: object) => turns.receive(JSON.stringify(m));
  return { sent, turns, say };
}

test("a turn is sent as a turn message, its text streams back, and done ends it", async () => {
  const { sent, turns, say } = channel();
  const text: string[] = [];
  const end = turns.take("Hi", "r1", (t) => text.push(t));
  assert.deepEqual(sent, [{ type: "turn", request_id: "r1", words: "Hi" }]);
  say({ type: "text", request_id: "r1", text: "Tell me" });
  say({ type: "text", request_id: "other", text: "not this turn's" });
  say({ type: "text", request_id: "r1", text: " about it." });
  say({ type: "done", request_id: "r1", turn_id: "t1", seq: 3, model: "fake/claude-test" });
  const got = await end;
  assert.deepEqual(text, ["Tell me", " about it."]);
  assert.equal(got.ended, "done");
  assert.equal(got.turnId, "t1");
  assert.equal(got.seq, 3);
  assert.equal(got.model, "fake/claude-test", "the page shows what answered");
  say({ type: "spoken", request_id: "r1" });
  assert.deepEqual(await got.spoken, { failed: false });
});

test("a lost reply keeps its text and is still spoken as far as it went", async () => {
  const { turns, say } = channel();
  const end = turns.take("Hi", "r1", () => {});
  say({ type: "text", request_id: "r1", text: "Tell" });
  say({ type: "lost", request_id: "r1", turn_id: "t1", seq: 1 });
  const got = await end;
  assert.equal(got.ended, "lost");
  say({ type: "spoken", request_id: "r1", failed: true });
  assert.deepEqual(await got.spoken, { failed: true });
});

test("each refusal is the failure it means for the words", async () => {
  for (const [reason, want] of [
    ["repeated", "taken"],
    ["busy", "busy"],
    ["unusable", "refused"],
    ["unreadable", "refused"],
    ["no-session", "refused"],
  ]) {
    const { turns, say } = channel();
    const end = turns.take("Hi", "r1", () => {});
    say({ type: "refused", request_id: "r1", reason, detail: "why" });
    await assert.rejects(end, (err) => failure(err) === want, `refused as ${reason}`);
  }
});

test("a turn that failed before its reply may have been taken", async () => {
  const { turns, say } = channel();
  const end = turns.take("Hi", "r1", () => {});
  say({ type: "failed", request_id: "r1" });
  await assert.rejects(end, (err) => failure(err) === "unsure");
});

test("a channel that closes mid-reply loses the turn, and before any text leaves it unsure", async () => {
  const replying = channel();
  const lost = replying.turns.take("Hi", "r1", () => {});
  replying.say({ type: "text", request_id: "r1", text: "Tell" });
  replying.turns.closed();
  const got = await lost;
  assert.equal(got.ended, "lost");
  assert.deepEqual(await got.spoken, { failed: true });

  const waiting = channel();
  const unsure = waiting.turns.take("Hi", "r1", () => {});
  waiting.turns.closed();
  await assert.rejects(unsure, (err) => failure(err) === "unsure");
});

test("a request ID already waiting is refused as busy, and the waiting turn still ends", {
  timeout: 2000,
}, async () => {
  const { sent, turns, say } = channel();
  const first = turns.take("Wait", "r1", () => {});
  await assert.rejects(
    turns.take("Wait", "r1", () => {}),
    (err) => failure(err) === "busy",
  );
  assert.equal(sent.length, 1, "not sent again");
  say({ type: "done", request_id: "r1", turn_id: "t1", seq: 1 });
  assert.equal((await first).turnId, "t1");
});

test("an interrupt that comes before its ending still settles the turn, as done", {
  timeout: 2000,
}, async () => {
  const { turns, say } = channel();
  const end = turns.take("Hi", "r1", () => {});
  say({ type: "interrupted", request_id: "r1", heard: "" });
  const got = await end;
  assert.equal(got.ended, "done", "voice saved the turn answered as far as it went");
  assert.deepEqual(await got.spoken, { failed: false, heard: "" });
});

test("a closing channel settles every turn still waiting", { timeout: 2000 }, async () => {
  const { turns, say } = channel();
  const first = turns.take("Hi", "r1", () => {});
  say({ type: "done", request_id: "r1", turn_id: "t1", seq: 1 });
  const answered = await first;
  const second = turns.take("Wait", "r2", () => {});
  turns.closed();
  assert.deepEqual(await answered.spoken, { failed: true });
  await assert.rejects(second, (err) => failure(err) === "unsure");
});

test("the first sound is timed once, from pressing send", () => {
  let now = 1000;
  const timer = startTimer(() => now);
  now += 400;
  assert.equal(timer.text(), 400);
  now += 1500;
  assert.equal(timer.sound(), 1900);
  now += 300;
  assert.equal(timer.sound(), 1900, "a later sound in the same reply doesn't move it");
});

// The connection's state as the browser reports it, for watchForDrop.
function connection() {
  const c = {
    connectionState: "connected" as RTCPeerConnectionState,
    onconnectionstatechange: null as (() => void) | null,
    to(state: RTCPeerConnectionState) {
      c.connectionState = state;
      c.onconnectionstatechange?.();
    },
  };
  return c;
}

test("a dead server is noticed once the connection fails or stays disconnected for 3s", (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  let drops = 0;
  const failing = connection();
  watchForDrop(failing, () => drops++);
  failing.to("failed");
  assert.equal(drops, 1, "failed is a drop at once");

  const quiet = connection();
  watchForDrop(quiet, () => drops++);
  quiet.to("disconnected");
  t.mock.timers.tick(2999);
  assert.equal(drops, 1, "disconnected may still come back");
  t.mock.timers.tick(1);
  assert.equal(drops, 2, "3s disconnected is a drop");

  const blip = connection();
  watchForDrop(blip, () => drops++);
  blip.to("disconnected");
  t.mock.timers.tick(2000);
  blip.to("connected");
  t.mock.timers.tick(5000);
  assert.equal(drops, 2, "a connection that comes back isn't dropped");
});

// A turn that never settles fails here rather than hanging the run.
test("a turn waiting when voice closes the connection settles, and voice's reason is passed on", {
  timeout: 2000,
}, async () => {
  const { turns, say } = channel();
  const reasons: string[] = [];
  const waiting = turns.take("Hi", "r1", () => {});
  say({ type: "text", request_id: "r1", text: "Tell" });
  const closing = JSON.stringify({ type: "closing", reason: "stopping" });
  onChannelMessage(closing, turns, (why) => reasons.push(why));
  const got = await waiting;
  assert.equal(got.ended, "lost", "the reply had started, so it's lost");
  assert.deepEqual(await got.spoken, { failed: true });
  assert.deepEqual(reasons, ["stopping"]);

  const quiet = channel();
  const unsure = quiet.turns.take("Hi", "r2", () => {});
  onChannelMessage(JSON.stringify({ type: "closing", reason: "replaced" }), quiet.turns, () => {});
  await assert.rejects(unsure, (err) => failure(err) === "unsure");
});
