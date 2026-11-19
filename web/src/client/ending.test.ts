import assert from "node:assert/strict";
import { test } from "node:test";
import { ProblemError } from "./api.ts";
import {
  afterAudioFailure,
  afterClosing,
  endSession,
  endsOnExpiry,
  learnOver,
  overBy,
} from "./ending.ts";

const ended = new ProblemError(409, "/v1/problems/session-ended", "Session ended", "");

test("End closes the audio first, so the reply playing is cut, then ends the session", async () => {
  const steps: string[] = [];
  await endSession(
    async () => {
      steps.push("audio closed");
    },
    async () => {
      steps.push("ended");
      return { id: "s1", agent_id: "a1", agent_version: 1, started_at: "" };
    },
    () => steps.push("shown over"),
  );
  assert.deepEqual(steps, ["audio closed", "ended", "shown over"]);
});

test("an End that fails leaves the session shown live, to press again", async () => {
  let over = false;
  await assert.rejects(
    endSession(
      async () => {},
      () => Promise.reject(new TypeError("Failed to fetch")),
      () => {
        over = true;
      },
    ),
  );
  assert.equal(over, false);
});

test("voice ending the audio as ended means the session is over", () => {
  assert.equal(afterClosing("ended"), "over");
  assert.equal(afterClosing("stopping"), "reconnect");
  assert.equal(afterClosing("dropped"), "reconnect");
  assert.equal(afterClosing("replaced"), "text");
});

test("voice ending the audio as its ticket expired means the session is over, with no reconnect", () => {
  assert.equal(afterClosing("expired"), "over");
});

test("audio refused because the session ended isn't asked for again", () => {
  assert.equal(afterAudioFailure(ended, 15), "over");
  assert.equal(afterAudioFailure(new ProblemError(503, "about:blank", "T", ""), 15), "retry");
  assert.equal(afterAudioFailure(new ProblemError(503, "about:blank", "T", ""), 0), "text");
});

const expired = new ProblemError(401, "/v1/problems/ticket-expired", "Ticket expired", "");
const refused = new ProblemError(401, "about:blank", "Unauthorized", "the ticket is invalid");

test("an audio offer refused as ticket-expired ends the session view, with no retry", () => {
  // With 15 tries left, which a dropped ticketExpired check would spend.
  assert.equal(afterAudioFailure(expired, 15), "over");
  assert.equal(overBy(expired), "expired");
});

test("any other 401 on a session route means it can't continue, and no audio retry", () => {
  assert.equal(overBy(refused), "unusable");
  assert.equal(afterAudioFailure(refused, 15), "over");
  const another = new ProblemError(404, "about:blank", "Not Found", "");
  assert.equal(overBy(another), "unusable", "a ticket for another session: no retry mends it");
  assert.equal(overBy(new ProblemError(403, "about:blank", "T", "")), undefined);
  assert.equal(overBy(new ProblemError(409, "/v1/problems/session-ended", "T", "")), "ended");
  assert.equal(overBy(new TypeError("Failed to fetch")), undefined);
});

test("a page that learns its ticket ran out sends End once; End too late is not sent again", () => {
  assert.equal(endsOnExpiry("expired", false), true);
  assert.equal(endsOnExpiry("expired", true), false, "already over: End came too late, or sent");
  assert.equal(endsOnExpiry("ended", false), false);
  assert.equal(endsOnExpiry("unusable", false), false, "a refused ticket can't end it either");
});

// What learnOver was told, learning why, over saying whether the page knew already.
function learning(why: "expired" | "ended" | "unusable", over = false) {
  const calls: string[] = [];
  learnOver(
    over,
    why,
    async () => {
      calls.push("end");
    },
    (w) => calls.push(`over ${w}`),
  );
  return calls;
}

test("learning its ticket expired, the page sends End once with it, then shows the session over", () => {
  assert.deepEqual(learning("expired"), ["end", "over expired"]);
  // Learning it again, once over, sends nothing more.
  assert.deepEqual(learning("expired", true), ["over expired"]);
});

test("an ended or refused session is shown over with no End", () => {
  assert.deepEqual(learning("ended"), ["over ended"]);
  assert.deepEqual(learning("unusable"), ["over unusable"]);
});

test("a failing End changes nothing: the session is shown over all the same", async () => {
  const shown: string[] = [];
  learnOver(
    false,
    "expired",
    () => Promise.reject(new TypeError("Failed to fetch")),
    (w) => shown.push(w),
  );
  await new Promise((r) => setImmediate(r));
  assert.deepEqual(shown, ["expired"]);
});
