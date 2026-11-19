import assert from "node:assert/strict";
import { test } from "node:test";
import { quietMs, ReplySoundClock, shownReply } from "./replysound.ts";

// Feeds the clock a reading every 10 ms from `from` up to `to`, loud or silent.
function play(h: ReplySoundClock, loud: boolean, from: number, to: number) {
  for (let now = from; now <= to; now += 10) h.sample(loud, now);
}

test("a send in a pause of the reply counts as stopped at once", () => {
  const h = new ReplySoundClock();
  play(h, true, 0, 40);
  const stops: number[] = [];
  h.sent(
    100,
    () => {},
    (ms) => stops.push(ms),
  );
  play(h, false, 110, 110 + quietMs);
  assert.deepEqual(stops, [0]);
});

test("an interrupted reply shows as far as it was heard, ending with a dash where it was cut", () => {
  const reply = "Tell me about a time you led a team. What was the hardest call you made?";
  assert.equal(shownReply(reply, undefined), reply, "a reply not interrupted shows whole");
  assert.equal(
    shownReply(reply, "Tell me about a time you led a team. What was the hardest "),
    "Tell me about a time you led a team. What was the hardest—",
  );
  assert.equal(shownReply(reply, ""), "", "cut before a word was heard, nothing of it shows");
});
