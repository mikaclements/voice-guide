import assert from "node:assert/strict";
import { test } from "node:test";
import { followSlackPx, follows } from "./follow.ts";

// A transcript box 400px tall around 1000px of lines, scrolled to the given point.
const scrolled = (scrollTop: number) => ({ scrollTop, clientHeight: 400, scrollHeight: 1000 });

test("the transcript follows while scrolled to its bottom, or short of it by no more than the slack", () => {
  for (const top of [600, 600 - followSlackPx]) {
    assert.equal(follows(scrolled(top)), true, `scrolled to ${top}`);
  }
});

test("the transcript stays put once the user scrolled up to reread", () => {
  assert.equal(follows(scrolled(600 - followSlackPx - 1)), false);
  assert.equal(follows(scrolled(0)), false);
});

test("a transcript shorter than its box follows, having nothing to scroll", () => {
  assert.equal(follows({ scrollTop: 0, clientHeight: 400, scrollHeight: 400 }), true);
});
