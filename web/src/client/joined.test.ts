import assert from "node:assert/strict";
import { afterEach, type TestContext, test } from "node:test";
import { expiredMessage, ProblemError } from "./api.ts";
import { closings, microphoneRefused, reconnectTries, unusableMessage } from "./ending.ts";
import { fakeVoice } from "./fakes.ts";
import { at, insecureMessage, JoinedSession, type Shown, wanted } from "./joined.ts";
import type { Spoken } from "./turns.ts";

// Lets the callbacks of promises already settled run; setImmediate, since tests mock setTimeout.
const settle = () => new Promise((resolve) => setImmediate(resolve));

// Every session a test joined, let go after it, so no hold on the microphone outlives the test.
const joinedSessions: JoinedSession[] = [];
afterEach(() => {
  for (const j of joinedSessions.splice(0)) j.leave();
});

// A session joined on voice f, recording what it shows and tells the element.
function joining(f = fakeVoice(), session = "s1", ticket = "tk1", secure = true) {
  const shown: Shown[] = [];
  const told: string[] = [];
  const joined = new JoinedSession(
    {
      session,
      ticket,
      fetcher: at("https://voice.example", f.fetcher),
      show: (s) => shown.push(s),
      giveBack: (words) => told.push(`back ${words}`),
      ended: (reason) => told.push(`ended ${reason}`),
      refused: (r) => told.push(`refused ${r.status ?? "-"} ${r.message}`),
    },
    f.open,
    f.play,
  );
  joined.join(secure);
  joinedSessions.push(joined);
  return { f, joined, shown, told, last: () => shown.at(-1) as Shown };
}

const turn = "POST https://voice.example/v1/sessions/s1/turns Bearer tk1";
const end = "POST https://voice.example/v1/sessions/s1/end Bearer tk1";
const problem = (status: number, type = "about:blank") =>
  new ProblemError(status, type, "No", "", true);
// voice's answer to a request it refuses.
const refusal =
  (status: number, type = "about:blank", title = "No") =>
  async () =>
    Response.json(
      { type, title, status },
      { status, headers: { "Content-Type": "application/problem+json" } },
    );
// An answer the test gives when it chooses, whether the request was called off or not.
function later() {
  let answer: (r: Response) => void = () => {};
  const asked = () => new Promise<Response>((resolve) => (answer = resolve));
  return { asked, answer: (r: () => Promise<Response>) => r().then(answer) };
}

test("typing goes over HTTP to voice's base, and words voice refuses come back for the box", async () => {
  const s = joining();
  assert.equal(s.last().locked, false);
  await s.joined.send(" Hi ");
  assert.deepEqual(s.f.requests, [turn]);
  const lines = s.last().lines.map((l) => `${l.who} ${l.text}`);
  assert.deepEqual(lines, ["you Hi", "agent Hello"]);
  s.f.turn = refusal(400, "about:blank", "Bad Request");
  await s.joined.send("Again");
  assert.deepEqual(s.told, ["back Again"]);
  assert.deepEqual(s.last().notes, ["Bad Request (400)"]);
});

// A session whose audio connection opened, on a clock the test sets: sound(loud, at) is a reading of
// the agent's sound at that time, and from(m) a message from voice on the data channel.
async function playing(t: TestContext) {
  let clock = 1000;
  t.mock.method(performance, "now", () => clock);
  const s = joining();
  const c = s.f.offers[0].open();
  await settle();
  const sound = (loud: boolean, at: number) => {
    clock = at;
    s.f.played[0].level(loud, at);
  };
  const from = (m: object) => c.audio.turns.receive(JSON.stringify(m));
  return { s, c, sound, from, at: (now: number) => (clock = now) };
}

test("under a typed reply the view carries the model that answered, how long the user waited for its first text and sound, and how soon it went quiet when cut short", async (t) => {
  const { s, c, sound, from, at } = await playing(t);
  s.joined.send("Hi");
  const first = c.sent[0].request_id;
  at(1400);
  from({ type: "text", request_id: first, text: "Hello there" });
  sound(true, 1900);
  from({ type: "done", request_id: first, turn_id: "t1", seq: 2, model: "m" });
  await settle();
  sound(true, 2000);
  s.joined.send("Wait");
  sound(true, 2100);
  from({ type: "interrupted", request_id: first, heard: "Hello" });
  sound(false, 2700);
  await settle();
  const [you, reply] = s.last().lines;
  assert.deepEqual([you.meta, reply.text], [undefined, "Hello— (interrupted)"]);
  assert.deepEqual(reply.meta, { model: "m", firstTextMs: 400, firstSoundMs: 900, stoppedMs: 100 });
});

test("a press just after a reply finished sounding, cutting nothing, shows no stop under it", async (t) => {
  const { s, c, sound, from } = await playing(t);
  s.joined.send("Hi");
  const first = c.sent[0].request_id;
  from({ type: "text", request_id: first, text: "Hello" });
  sound(true, 1500);
  from({ type: "done", request_id: first, turn_id: "t1", seq: 2 });
  from({ type: "spoken", request_id: first });
  await settle();
  s.joined.send("Next");
  sound(false, 2000);
  await settle();
  assert.deepEqual(s.last().lines[1].meta, { firstTextMs: 0, firstSoundMs: 500 });
});

test("under a spoken reply the view carries how long after the user finished speaking it sounded, and whether it was lost or its audio stopped", async () => {
  const s = joining();
  s.f.offers[0].open();
  await settle();
  const said = (id: string, ended: "done" | "lost", spoken: Spoken) =>
    s.f.offers[0].options?.onHearing?.(id, "Hi", async (onText) => {
      onText("Hello");
      return { ended, model: "m", spoken: Promise.resolve(spoken) };
    });
  said("r1", "done", { failed: false, firstAudioMs: 7 });
  said("r2", "lost", { failed: true });
  await settle();
  const [, r1, , r2] = s.last().lines;
  assert.deepEqual(r1.meta, { model: "m", finishedToSoundMs: 7 });
  assert.deepEqual(r2.meta, { model: "m", lost: true, audioFailed: true });
});

test("a session let go shows nothing more and gives no words back", async () => {
  const s = joining();
  const reply = later();
  s.f.turn = reply.asked;
  s.joined.send("Hi");
  await settle();
  s.joined.leave();
  const seen = s.shown.length;
  await reply.answer(refusal(400));
  await settle();
  assert.equal(s.shown.length, seen);
  assert.deepEqual(s.told, []);
});

test("the audio opens with the ticket and takes typed turns before the microphone prompt is answered", async () => {
  const s = joining();
  assert.equal(s.f.offers[0].ticket, "tk1");
  const c = s.f.offers[0].open();
  await settle();
  s.joined.send("Hi");
  assert.equal(c.sent[0].words, "Hi", "on the data channel");
  assert.equal(s.last().status, "", "no microphone yet");
});

test("once the browser plays the agent the opening goes on the data channel at once, and voice refusing it shows and tells nothing", async () => {
  const s = joining();
  const c = s.f.offers[0].open();
  await settle();
  s.f.played[0].blocked(false);
  assert.deepEqual(c.sent, [{ type: "turn", request_id: c.sent[0]?.request_id, opening: true }]);
  c.audio.turns.receive(
    JSON.stringify({
      type: "refused",
      request_id: c.sent[0].request_id,
      reason: "opening-refused",
    }),
  );
  await settle();
  assert.deepEqual([s.told, s.last().lines, s.last().notes], [[], [], []]);
});

test("while the browser refuses to play the agent no opening goes, and the press to hear sends it once", async () => {
  const s = joining();
  const c = s.f.offers[0].open();
  await settle();
  s.f.played[0].blocked(true);
  assert.equal(c.sent.length, 0, "nothing before the press");
  s.joined.hear();
  s.joined.hear();
  assert.deepEqual(c.sent, [{ type: "turn", request_id: c.sent[0]?.request_id, opening: true }]);
});

test("the opening over HTTP carries opening, a request ID of its own and no words", async () => {
  const f = fakeVoice();
  const bodies: { request_id: string }[] = [];
  const fetcher = f.fetcher;
  f.fetcher = (async (url: string, init?: RequestInit) => {
    if (url.endsWith("/turns")) bodies.push(JSON.parse(String(init?.body)));
    return fetcher(url, init);
  }) as typeof fetch;
  const s = joining(f);
  s.f.offers[0].fail(new TypeError("Failed to fetch"));
  await settle();
  assert.deepEqual(bodies, [{ request_id: bodies[0]?.request_id, opening: true }]);
  assert.match(
    bodies[0].request_id,
    /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/,
  );
});

test("audio that fails sends the opening over HTTP, once, and a 409 for it shows and tells nothing", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const s = joining();
  s.f.turn = refusal(409, "/v1/problems/opening-refused");
  s.f.offers[0].fail(new TypeError("Failed to fetch"));
  await settle();
  t.mock.timers.tick(2000);
  s.f.offers[1].fail(new TypeError("Failed to fetch"));
  await settle();
  assert.deepEqual(s.f.requests, [turn]);
  assert.deepEqual([s.told, s.last().lines, s.last().notes], [[], [], []]);
});

const greetingStatus = "Agent is about to greet you: your microphone opens when it starts";

test("Mute silences the microphone and says so", async () => {
  const s = joining();
  const c = s.f.offers[0].open();
  c.give(true);
  await settle();
  assert.deepEqual([s.last().status, s.last().mute], [greetingStatus, "Mute"]);
  s.joined.mute();
  assert.deepEqual(c.muted, [true, true]);
  assert.deepEqual(
    [s.last().status, s.last().mute],
    ["Microphone muted: type, or unmute to talk", "Unmute"],
  );
});

// A session whose audio opened with the microphone given and the agent's opening sent on the data
// channel: from(m) is voice's message about the opening, sound() a reading of the agent's sound,
// muted() the microphone's last state, and timerCleared() whether a timer was called off.
async function greeting(t: TestContext) {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const cleared = t.mock.method(globalThis, "clearTimeout");
  const s = joining();
  const c = s.f.offers[0].open();
  c.give(true);
  await settle();
  s.f.played[0].blocked(false);
  const id = c.sent[0].request_id;
  const from = async (m: object) => {
    c.audio.turns.receive(JSON.stringify({ request_id: id, ...m }));
    await settle();
  };
  const timerCleared = () => cleared.mock.calls.some((call) => call.arguments[0] !== undefined);
  const sound = () => s.f.played[0].level(true, 1);
  return { s, c, from, sound, muted: () => c.muted.at(-1), timerCleared };
}

test("the microphone holds muted from the session's first connection set up, before the opening is sent, typing still goes, and the status line says why", async () => {
  const s = joining();
  const c = s.f.offers[0].open();
  c.give(true);
  await settle();
  assert.deepEqual(c.sent, [], "no opening yet");
  assert.deepEqual([c.muted, s.last().status, s.last().mute], [[true], greetingStatus, "Mute"]);
  assert.equal(s.last().locked, false);
});

test("the microphone opens at the greeting's first sound, while the greeting still plays", async (t) => {
  const { s, c, from, sound, muted } = await greeting(t);
  await from({ type: "text", text: "Welcome back" });
  assert.equal(muted(), true, "nothing sounds yet");
  sound();
  assert.deepEqual([muted(), s.last().status], [false, "Answering"]);
  await from({ type: "done", turn_id: "t1", seq: 1 });
  assert.deepEqual(c.muted, [true, false], "playing still, and open");
});

test("speech once the greeting sounds is sent, and cuts it as any reply", async (t) => {
  const { s, from, sound, muted } = await greeting(t);
  await from({ type: "text", text: "Welcome back" });
  sound();
  assert.equal(muted(), false, "the user's speech goes, not silence");
  s.f.offers[0].options?.onHearing?.("r1", "Hello", async (onText) => {
    onText("Hi");
    return { ended: "done", spoken: Promise.resolve({ failed: false }) };
  });
  await from({ type: "interrupted", heard: "Welcome" });
  assert.equal(muted(), false, "still open");
  const lines = s.last().lines.map((l) => `${l.who} ${l.text}`);
  assert.deepEqual(lines, ["agent Welcome— (interrupted)", "you Hello", "agent Hi"]);
});

test("a reconnect once the session has turns holds nothing", async (t) => {
  const { s, from, sound } = await greeting(t);
  await from({ type: "text", text: "Welcome back" });
  sound();
  s.f.offers[0].closing("dropped");
  const next = s.f.offers[1].open();
  next.give(true);
  await settle();
  s.f.played[1].blocked(false);
  assert.deepEqual([next.muted, s.last().status], [[false], "Listening"]);
});

test("an opening voice refuses, as for an agent that waits, opens the microphone at once", async (t) => {
  const { s, from, muted } = await greeting(t);
  assert.equal(muted(), true, "until voice answers");
  await from({ type: "refused", reason: "opening-refused" });
  assert.deepEqual([muted(), s.last().status], [false, "Listening"]);
});

test("a typed turn cutting the greeting short opens the microphone once the opening has ended", async (t) => {
  const { s, from, muted } = await greeting(t);
  s.joined.send("Hi");
  await settle();
  assert.equal(muted(), true, "until voice ends the opening");
  await from({ type: "interrupted", heard: "" });
  assert.equal(muted(), false);
});

test("the user's own Mute, pressed before or during the greeting, stays after it", async (t) => {
  const { s: unmuted, sound, muted } = await greeting(t);
  unmuted.joined.mute();
  unmuted.joined.mute();
  assert.deepEqual([muted(), unmuted.last().mute], [true, "Mute"], "unmuted during it: still held");
  sound();
  assert.equal(muted(), false);
  for (const before of [true, false]) {
    const s = joining();
    const c = s.f.offers[0].open();
    c.give(true);
    await settle();
    if (before) s.joined.mute();
    s.f.played[0].blocked(false);
    if (!before) s.joined.mute();
    assert.deepEqual([c.muted.at(-1), s.last().mute], [true, "Unmute"], String(before));
    const id = c.sent[0].request_id;
    c.audio.turns.receive(JSON.stringify({ type: "refused", request_id: id }));
    await settle();
    assert.deepEqual([c.muted.at(-1), s.last().mute], [true, "Unmute"], String(before));
  }
});

test("a greeting that never ends opens the microphone after 30 s", async (t) => {
  const { s, muted } = await greeting(t);
  t.mock.timers.tick(29_999);
  assert.equal(muted(), true);
  t.mock.timers.tick(1);
  assert.deepEqual([muted(), s.last().status], [false, "Listening"]);
});

test("leaving, or the session ending, during the greeting leaves no hold or timer behind", async (t) => {
  const left = await greeting(t);
  left.s.joined.leave();
  assert.ok(left.timerCleared(), "left");
  t.mock.reset();
  const ended = await greeting(t);
  ended.s.f.offers[0].closing("ended");
  await settle();
  assert.ok(ended.timerCleared(), "ended");
  const seen = ended.s.shown.length;
  t.mock.timers.tick(30_000);
  assert.equal(ended.s.shown.length, seen);
});

test("the opening over HTTP, with no audio to play it, holds no microphone", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const s = joining();
  s.f.turn = later().asked;
  s.f.offers[0].fail(new TypeError("Failed to fetch"));
  await settle();
  t.mock.timers.tick(2000);
  const c = s.f.offers[1].open();
  c.give(true);
  await settle();
  assert.deepEqual([c.muted, s.last().status], [[false], "Listening"]);
});

test("a microphone refused says how to allow it, a missing one says nothing, and typing goes on", async () => {
  for (const refused of [true, false]) {
    const s = joining();
    s.f.offers[0].open().give(false);
    if (refused) s.f.offers[0].options?.onRefused?.();
    await settle();
    assert.deepEqual(s.last().notes, refused ? [microphoneRefused] : [], String(refused));
    assert.deepEqual([s.last().status, s.last().mute, s.last().locked], ["", "", false]);
  }
});

test("the agent the browser won't play without a press waits for one", async () => {
  const s = joining();
  s.f.offers[0].open().give(true);
  await settle();
  s.f.played[0].blocked(true);
  assert.equal(s.last().blocked, true);
  s.joined.hear();
  assert.equal(s.f.played[0].plays, 1);
  assert.deepEqual([s.last().blocked, s.last().status], [false, greetingStatus]);
});

test("a dropped connection says so and goes on in text, typing over HTTP", async () => {
  const s = joining();
  s.f.offers[0].open();
  await settle();
  s.f.offers[0].closing("dropped");
  assert.deepEqual(s.last().notes, [closings.dropped]);
  await s.joined.send("Hi");
  assert.deepEqual(s.f.requests, [turn]);
});

test("a session let go closes its audio, a connection opening as it is let go or once the session is over is closed, and one failing then does nothing", async () => {
  const s = joining();
  const c = s.f.offers[0].open();
  await settle();
  s.joined.leave();
  assert.deepEqual(c.closed, [1]);
  const late = joining(s.f, "s2", "tk2");
  late.joined.leave();
  const opened = s.f.offers[1].open();
  await settle();
  assert.deepEqual(opened.closed, [1]);
  const ended = joining(s.f, "s3", "tk3");
  s.f.offers[2].open();
  await settle();
  s.f.offers[2].closing("dropped");
  await ended.joined.end();
  const reconnected = s.f.offers[3].open();
  await settle();
  assert.deepEqual(reconnected.closed, [1], "End pressed while a reconnect's offer was answered");
  const failed = joining(s.f, "s4", "tk4");
  failed.joined.leave();
  s.f.offers[4].fail(new TypeError("Failed to fetch"));
  await settle();
  assert.ok(!s.f.requests.some((r) => r.includes("/s4/")), "no opening over HTTP");
});

type Joined = ReturnType<typeof joining>;
// The audio connection opens, then act.
const opened = (act: (s: Joined) => unknown) => async (s: Joined) => {
  s.f.offers[0].open();
  await settle();
  await act(s);
};

test("each way a session ends shows its state once and tells the product once", async () => {
  const ended = ["ended ended"];
  const expired = ["ended expired"];
  const unusable = (status: number) => [`refused ${status} No (${status})`, "ended unusable"];
  const expiredType = "/v1/problems/ticket-expired";
  const expiredTurn = async (s: Joined) => {
    s.f.turn = refusal(401, expiredType);
    await s.joined.send("Hi");
  };
  // What happens, what the element sends, what it says, and what it tells the product's page.
  const cases: [string, (s: Joined) => unknown, string[], string, string[]][] = [
    ["End", opened((s) => s.joined.end().then(() => s.joined.end())), [end], closings.ended, ended],
    ["ended elsewhere", opened((s) => s.f.offers[0].closing("ended")), [], closings.ended, ended],
    ["expired", opened((s) => s.f.offers[0].closing("expired")), [end], expiredMessage, expired],
    [
      "expired before the audio opens",
      (s) => s.f.offers[0].fail(problem(401, expiredType)),
      [end],
      expiredMessage,
      expired,
    ],
    ["a turn refused as expired", expiredTurn, [turn, end], expiredMessage, expired],
    ["ticket refused", (s) => s.f.offers[0].fail(problem(401)), [], unusableMessage, unusable(401)],
    [
      "another session's ticket",
      (s) => s.f.offers[0].fail(problem(404)),
      [],
      unusableMessage,
      unusable(404),
    ],
  ];
  for (const [what, act, requests, over, told] of cases) {
    const s = joining();
    await act(s);
    await settle();
    assert.deepEqual(s.f.requests, requests, what);
    assert.equal(s.last().over, over, what);
    assert.ok(!s.last().notes.includes(over), `${what}: said once`);
    assert.equal(s.last().locked, true, what);
    assert.deepEqual(
      s.told.filter((m) => !m.startsWith("back")),
      told,
      what,
    );
  }
});

test("an End that doesn't reach voice rejects, and leaves the session live, typing over HTTP", async () => {
  const s = joining();
  s.f.offers[0].open();
  await settle();
  s.f.end = () => Promise.reject(new TypeError("Failed to fetch"));
  await assert.rejects(s.joined.end(), /Failed to fetch/);
  assert.deepEqual(
    [s.last().over, s.last().notes, s.last().locked],
    ["", ["Failed to fetch"], false],
  );
  await s.joined.send("Hi");
  assert.equal(s.f.requests.at(-1), turn, "not on the closed data channel");
});

test("a late refusal in a session let go sends no End", async () => {
  const s = joining();
  const reply = later();
  s.f.turn = reply.asked;
  s.joined.send("Hi");
  await settle();
  s.joined.leave();
  await reply.answer(refusal(401, "/v1/problems/ticket-expired"));
  await settle();
  assert.ok(!s.f.requests.some((r) => r.includes("/end")));
});

test("two sessions on one page keep apart", async () => {
  const f = fakeVoice();
  const a = joining(f, "s1", "tk1");
  const b = joining(f, "s2", "tk2");
  f.offers[0].open();
  f.offers[1].open();
  await settle();
  f.offers[0].closing("ended");
  assert.deepEqual([a.told, b.told, b.last().over], [["ended ended"], [], ""]);
});

test("audio that fails at the join or drops later is tried again with the same ticket, then goes on in text", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const s = joining();
  s.f.offers[0].fail(new TypeError("Failed to fetch"));
  await settle();
  t.mock.timers.tick(2000);
  s.f.offers[1].open();
  await settle();
  s.f.offers[1].closing("dropped");
  for (let i = 2; i <= reconnectTries + 2; i++) {
    s.f.offers[i].fail(new TypeError("Failed to fetch"));
    await settle();
    t.mock.timers.tick(2000);
  }
  assert.equal(s.f.offers.length, reconnectTries + 3);
  assert.ok(s.f.offers.every((o) => o.ticket === "tk1"));
  assert.deepEqual(s.last().notes, ["No audio (Failed to fetch); replies come as text."]);
  assert.deepEqual(s.told, []);
});

test("a connection that reconnects keeps the microphone muted", async () => {
  const s = joining();
  s.f.offers[0].open();
  await settle();
  s.joined.mute();
  s.f.offers[0].closing("stopping");
  const next = s.f.offers[1].open();
  await settle();
  assert.deepEqual(next.muted, [true]);
});

test("audio another tab took isn't taken back, so two pages on one session don't take turns at it", async () => {
  const s = joining();
  s.f.offers[0].open();
  await settle();
  s.f.offers[0].closing("replaced");
  await settle();
  assert.equal(s.f.offers.length, 1);
  assert.deepEqual(s.last().notes, [closings.replaced]);
});

test("a refusal that isn't voice's own is told by its status alone", async () => {
  const s = joining();
  s.f.offers[0].fail(new ProblemError(404, "about:blank", "Not Found", "<html>", false));
  await settle();
  assert.deepEqual(s.told, ["refused 404 Refused (404)", "ended unusable"]);
});

test("a session let go calls off its typed turn and its reconnect", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const cleared = t.mock.method(globalThis, "clearTimeout");
  const s = joining();
  s.f.offers[0].fail(new TypeError("Failed to fetch"));
  await settle();
  s.f.turn = later().asked;
  s.joined.send("Hi");
  await settle();
  s.joined.leave();
  assert.ok(
    cleared.mock.calls.some((call) => call.arguments[0] !== undefined),
    "timer cleared",
  );
  assert.equal(s.f.signals.at(-1)?.aborted, true, "the typed turn is called off");
  t.mock.timers.tick(2000);
  await settle();
  assert.equal(s.f.offers.length, 1, "no reconnect");
});

test("a page on plain http holds no session, and says why", () => {
  const s = joining(fakeVoice(), "s1", "tk1", false);
  assert.equal(s.f.offers.length, 0);
  assert.deepEqual(s.told, [`refused - ${insecureMessage}`, "ended unusable"]);
  assert.deepEqual([s.last().notes, s.last().over], [[insecureMessage], unusableMessage]);
});

test("the element joins what it was given once it is on the page, voice at api, else its script's origin", () => {
  const from = "https://voice.example";
  const given = { session: "s1", ticket: "tk1" };
  assert.equal(wanted(false, given, null, from), undefined, "off the page");
  assert.equal(wanted(true, undefined, null, from), undefined, "nothing given yet");
  assert.equal(wanted(true, { session: " ", ticket: "tk1" }, null, from), undefined);
  const join = wanted(true, given, null, from);
  assert.deepEqual(join, { ...given, api: from, key: `${from} s1 tk1` });
  const other = wanted(true, { session: "s1", ticket: "tk2" }, "https://v.example/", from);
  assert.equal(other?.api, "https://v.example");
  assert.notEqual(other?.key, join?.key, "a new ticket joins again");
  assert.equal(wanted(true, given, null, "")?.api, "", "the page's own origin");
});

test("every path the element sends goes to voice's base, the audio connection's own address too", async () => {
  const reached: string[] = [];
  const fetcher = at("https://voice.example", (async (url: string) => {
    reached.push(url);
    return new Response();
  }) as typeof fetch);
  // The address voice's answer to an offer names, which hanging up sends DELETE to.
  await fetcher("/v1/sessions/s1/audio/c1", { method: "DELETE" });
  assert.deepEqual(reached, ["https://voice.example/v1/sessions/s1/audio/c1"]);
});
