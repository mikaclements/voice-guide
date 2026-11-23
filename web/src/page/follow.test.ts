import assert from "node:assert/strict";
import { test } from "node:test";
import { followSlackPx, follows } from "./follow.ts";

// What the user sees of the page: from 100px down to 700px.
const seen = { top: 100, bottom: 700 };

test("the page follows while the session's bottom is in view, or below it by no more than the slack", () => {
  for (const bottom of [100, 400, 700, 700 + followSlackPx]) {
    assert.equal(follows(bottom, seen), true, `bottom at ${bottom}`);
  }
});

test("the page stays put once the user scrolled up to reread, leaving the bottom further below", () => {
  assert.equal(follows(700 + followSlackPx + 1, seen), false);
  assert.equal(follows(5000, seen), false);
});

test("the page stays put once the user scrolled down past the bottom, as to the past sessions beneath it", () => {
  assert.equal(follows(99, seen), false);
  assert.equal(follows(-2000, seen), false);
});
