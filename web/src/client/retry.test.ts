import assert from "node:assert/strict";
import { test } from "node:test";
import { ProblemError } from "./api.ts";
import { failure, keepRequestId, requestIdFor } from "./retry.ts";

test("a failure is refused only when voice said so; a repeat it already took is taken", () => {
  const problem = (status: number, type = "about:blank") => new ProblemError(status, type, "T", "");
  const cases: [unknown, string][] = [
    [problem(400), "refused"],
    [problem(404), "refused"],
    [problem(422, "/v1/problems/request-reused"), "refused"],
    [problem(409), "busy"], // a turn in flight: this one only if this send was a retry
    [problem(409, "/v1/problems/turn-repeated"), "taken"],
    [problem(409, "/v1/problems/session-ended"), "refused"], // never taken, and never will be
    [problem(502), "unsure"],
    [problem(503), "unsure"],
    [new TypeError("Failed to fetch"), "unsure"],
  ];
  for (const [err, want] of cases) assert.equal(failure(err), want, String(err));
});

test("a send refused while another turn is in flight keeps its request ID only if it was a retry", () => {
  // A fresh send that voice turned away wasn't taken: the next send is a new request.
  assert.equal(keepRequestId("busy", false), false);
  // A retry may have met its own first try still in flight, so it stays the same request.
  assert.equal(keepRequestId("busy", true), true);
  assert.equal(keepRequestId("unsure", false), true);
  assert.equal(keepRequestId("refused", true), false);
  assert.equal(keepRequestId("taken", true), false);
});

test("sending the same again reuses the kept request ID; anything else gets a new one", () => {
  const kept = { key: "Hi", requestId: "r1" };
  assert.equal(requestIdFor("Hi", kept), "r1");
  assert.notEqual(requestIdFor("Hello", kept), "r1");
  assert.match(requestIdFor("Hi", undefined), /^[0-9a-f-]{36}$/);
});
