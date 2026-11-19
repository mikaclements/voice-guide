// Stepping through walk-throughs: within a story, across stories when played in order, and
// stopping at either end.
import assert from "node:assert/strict";
import { test } from "node:test";
import type { View } from "./types.ts";
import { walkAfter, walkBefore } from "./walk.ts";

const step = (caption: string) => ({ nodes: ["a"], edges: [], caption });
const view: View = {
  id: "v",
  title: "v",
  level: 1,
  lead: "",
  considerations: [],
  flows: [
    { id: "one", name: "One", steps: [step("1a"), step("1b")] },
    { id: "two", name: "Two", steps: [step("2a")] },
  ],
};

test("next goes on through a story, then into the next, then stops", () => {
  assert.deepEqual(walkAfter(view, { flow: "one", step: 0 }), { flow: "one", step: 1 });
  assert.deepEqual(walkAfter(view, { flow: "one", step: 1 }), { flow: "two", step: 0 });
  assert.equal(walkAfter(view, { flow: "two", step: 0 }), null);
});

test("back goes the other way and stops at the first step", () => {
  assert.deepEqual(walkBefore(view, { flow: "two", step: 0 }), { flow: "one", step: 1 });
  assert.deepEqual(walkBefore(view, { flow: "one", step: 1 }), { flow: "one", step: 0 });
  assert.equal(walkBefore(view, { flow: "one", step: 0 }), null);
});
