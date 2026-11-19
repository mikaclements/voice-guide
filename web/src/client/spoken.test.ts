import assert from "node:assert/strict";
import { test } from "node:test";
import { ChannelTurns } from "./audio.ts";
import { ReplySoundClock } from "./replysound.ts";
import { type Page, type Reply, Sender } from "./sending.ts";
import { SpokenTurns } from "./spoken.ts";

// A data channel's page side whose spoken turns show on a page that keeps its messages in order.
function harness() {
  const shown = new Map<string, Reply>();
  const givenBack: string[] = [];
  const why: string[] = [];
  const page: Page = {
    add: (you, words, reply) => {
      shown.delete(you);
      shown.set(you, { text: words });
      shown.set(reply, { text: "" });
    },
    open: (reply) => shown.set(reply, { text: "" }),
    showLiveWords: (you, words) => (words ? shown.set(you, { text: words }) : shown.delete(you)),
    update: (id, change) => shown.has(id) && shown.set(id, change(shown.get(id) as Reply)),
    takeBack: (you, reply) => {
      shown.delete(reply);
      shown.set(you, { text: `${shown.get(you)?.text ?? ""} (hearing)` });
    },
    giveBack: (you, reply, words, reason) => {
      shown.delete(you);
      shown.delete(reply);
      givenBack.push(words);
      why.push(reason);
    },
  };
  // Typed sends share the page and the clock of the agent's sound, as on the page itself; their
  // messages are t1, t2 and on.
  const clock = new ReplySoundClock();
  let ids = 0;
  const sender = new Sender(
    clock,
    page,
    () => 0,
    () => `t${++ids}`,
  );
  const spoken = new SpokenTurns(page, (words, requestId) => sender.keep(words, requestId), clock);
  const sent: { request_id: string }[] = [];
  const turns = new ChannelTurns(
    (m) => sent.push(JSON.parse(m)),
    (id, words, follow, lost) => spoken.userSpeech(id, words, follow, lost),
  );
  const say = (m: object) => turns.receive(JSON.stringify({ request_id: "v1", ...m }));
  return { shown, givenBack, why, say, turns, sender, sent, clock };
}

const flush = () => new Promise((r) => setImmediate(r));

test("the words show as voice hears them, then the reply follows with finished speaking to first sound", async () => {
  const { shown, say } = harness();
  say({ type: "hearing", text: "Where" });
  assert.deepEqual(shown.get("v1:you"), { text: "Where" });
  say({ type: "hearing", text: "Where should we start?" });
  say({ type: "hearing", text: "Where should we start?", final: true });
  say({ type: "text", text: "Tell me" });
  say({ type: "done", turn_id: "t1", seq: 1, model: "fake/claude-test" });
  say({ type: "spoken", first_audio_ms: 1234 });
  await flush();
  assert.deepEqual([...shown.keys()], ["v1:you", "v1:reply"]);
  assert.deepEqual(shown.get("v1:you"), { text: "Where should we start?" });
  assert.deepEqual(shown.get("v1:reply"), {
    text: "Tell me",
    ended: "done",
    model: "fake/claude-test",
    finishedToSoundMs: 1234,
  });
});

test("words heard come to nothing once voice says none were taken", () => {
  const { shown, say } = harness();
  say({ type: "hearing", text: "Hm" });
  say({ type: "hearing", final: true });
  assert.equal(shown.size, 0);
});

test("a spoken turn voice couldn't take puts its words in the box to send again", async () => {
  const { shown, givenBack, say } = harness();
  say({ type: "hearing", text: "Hold on.", final: true });
  say({ type: "refused", reason: "busy", detail: "too many turns are waiting" });
  await flush();
  assert.equal(shown.size, 0);
  assert.deepEqual(givenBack, ["Hold on."]);
});

test("a spoken turn's interrupted reply shows as far as it was heard", async () => {
  const { shown, say } = harness();
  say({ type: "hearing", text: "Hi.", final: true });
  say({ type: "text", text: "Tell me about it." });
  say({ type: "done" });
  say({ type: "interrupted", heard: "Tell me", first_audio_ms: 900 });
  await flush();
  assert.equal(shown.get("v1:reply")?.heard, "Tell me");
  assert.equal(shown.get("v1:reply")?.finishedToSoundMs, 900);
});

test("talking on before the reply sounds takes it away, then one reply follows all the words", async () => {
  const { shown, givenBack, say } = harness();
  say({ type: "hearing", text: "I led the rebuild:", final: true });
  say({ type: "text", text: "Tell me" });
  say({ type: "done" });
  say({ type: "talking_on" });
  await flush();
  assert.deepEqual([...shown.keys()], ["v1:you"], "the reply goes");
  assert.deepEqual(shown.get("v1:you"), { text: "I led the rebuild: (hearing)" });

  say({ type: "hearing", text: "I led the rebuild: status moved to events" });
  say({ type: "hearing", text: "I led the rebuild: status moved to events.", final: true });
  say({ type: "text", text: "Why events?" });
  say({ type: "done" });
  say({ type: "spoken", first_audio_ms: 700 });
  await flush();
  assert.deepEqual([...shown.keys()], ["v1:you", "v1:reply"]);
  assert.deepEqual(shown.get("v1:you"), { text: "I led the rebuild: status moved to events." });
  assert.equal(shown.get("v1:reply")?.text, "Why events?", "only the reply to all the words");
  assert.deepEqual(givenBack, []);
});

test("words voice couldn't hear in full go to the box, with a line saying so, and take no turn", async () => {
  const { shown, givenBack, why, say } = harness();
  say({ type: "hearing", text: "I led the" });
  say({ type: "hearing", text: "I led the", final: true, failed: true });
  await flush();
  assert.equal(shown.size, 0);
  assert.deepEqual(givenBack, ["I led the"]);
  assert.match(why[0], /didn't catch/);
});

test("speech voice heard none of, as when speech-to-text is down, gets a line offering typing", async () => {
  const { shown, givenBack, why, say } = harness();
  say({ type: "hearing", final: true, failed: true });
  await flush();
  assert.equal(shown.size, 0);
  assert.deepEqual(givenBack, [""]);
  assert.match(why[0], /type it/);
});

test("words still being heard when the connection goes go to the box", () => {
  const { shown, givenBack, say, turns } = harness();
  say({ type: "hearing", text: "Where should" });
  say({ type: "hearing", request_id: "v2", text: "Taken.", final: true });
  turns.closed();
  assert.equal(shown.has("v1:you"), false);
  assert.deepEqual(givenBack, ["Where should"], "only the words no turn was taken with");
});

test("a spoken turn that may have been taken goes again with its request ID", async () => {
  for (const end of [{ type: "failed" }, undefined]) {
    const { givenBack, say, turns, sender, sent } = harness();
    say({ type: "hearing", text: "Hold on.", final: true });
    if (end) say(end);
    else turns.closed(); // the connection closed before the reply
    await flush();
    assert.deepEqual(givenBack, ["Hold on."]);
    sender.send("Hold on.", turns, undefined);
    assert.equal(
      sent[0]?.request_id,
      "v1",
      "sent again from the box, it repeats the spoken turn's ID",
    );
  }
});

test("a spoken turn taken before a typed send's first sound has that sound, not the typed send", () => {
  const { shown, say, turns, sender, clock } = harness();
  sender.send("Hi.", turns, undefined);
  say({ type: "hearing", text: "Hold on.", final: true });
  for (let now = 0; now <= 50; now += 10) clock.sample(true, now);
  assert.equal(shown.get("t2")?.firstSoundMs, undefined, "the typed reply, cut before it sounded");
});

test("a spoken turn's request ID never replaces a typed send's, each kept with its own words", async () => {
  const { say, turns, sender, sent } = harness();
  sender.send("Where should we start?", turns, undefined);
  say({ type: "hearing", text: "Hold on.", final: true });
  turns.closed(); // both may have been taken
  await flush();
  sender.send("Where should we start?", turns, undefined);
  sender.send("Hold on.", turns, undefined);
  const [typed, typedAgain, spokenAgain] = sent.map((m) => m.request_id);
  assert.equal(typedAgain, typed, "the typed words repeat their own ID");
  assert.equal(spokenAgain, "v1", "the spoken words repeat the spoken turn's ID");
});

test("a spoken turn voice refuses leaves the typed reply's first sound to be timed", async () => {
  const { shown, say, turns, sender, clock } = harness();
  sender.send("Hi.", turns, undefined);
  say({ type: "hearing", text: "Hold on.", final: true });
  say({ type: "refused", reason: "busy", detail: "too many turns are waiting" });
  await flush();
  for (let now = 0; now <= 50; now += 10) clock.sample(true, now);
  assert.equal(shown.get("t2")?.firstSoundMs, 0, "the typed reply, which still plays");
});
