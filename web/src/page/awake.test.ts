import assert from "node:assert/strict";
import { mock, test } from "node:test";
import { type Lock, type Locks, mayTurnOff, quietMs, StayAwake, type Visibility } from "./awake.ts";

// Every test's quiet cap runs on a clock the test moves, so none is left waiting at the end.
mock.timers.enable({ apis: ["setTimeout"] });
// Lets the callbacks of promises already settled run; setImmediate, since setTimeout is mocked.
const settle = () => new Promise((resolve) => setImmediate(resolve));

// A lock the browser grants, which it or the page lets go of.
class FakeLock implements Lock {
  released = false;
  release() {
    this.released = true;
    return Promise.resolve();
  }
}

// A browser whose wake lock answers each request with the next outcome given, a lock or a refusal,
// or holds it unanswered until answer or refuse is called; and a page shown or hidden as the test
// says, its listener called only on a change, as the browser's is.
function browser(...outcomes: ("grant" | "refuse" | "hold")[]) {
  const granted: FakeLock[] = [];
  let requests = 0;
  let answer = () => {};
  let refuse = () => {};
  const locks: Locks = {
    wakeLock: {
      request: (type) => {
        assert.equal(type, "screen");
        requests += 1;
        const lock = new FakeLock();
        const asked = Promise.withResolvers<Lock>();
        answer = () => {
          granted.push(lock);
          asked.resolve(lock);
        };
        refuse = () => asked.reject(new DOMException("No", "NotAllowedError"));
        const outcome = outcomes.shift() ?? "grant";
        if (outcome === "grant") answer();
        if (outcome === "refuse") refuse();
        return asked.promise;
      },
    },
  };
  let listener: (() => void) | undefined;
  const page: Visibility & { show(state: "visible" | "hidden"): void } = {
    visibilityState: "visible",
    addEventListener: (_type, fn) => {
      listener = fn;
    },
    removeEventListener: (_type, fn) => {
      if (listener === fn) listener = undefined;
    },
    show(state) {
      Object.assign(this, { visibilityState: state });
      listener?.();
    },
  };
  const notes: string[] = [];
  const awake = new StayAwake(locks, page, (n) => notes.push(n));
  return {
    awake,
    page,
    granted,
    notes,
    requests: () => requests,
    answer: () => answer(),
    refuse: () => refuse(),
  };
}

test("a session's start keeps the display on, and its end lets it turn off", async () => {
  const b = browser();
  b.awake.hold();
  await settle();
  assert.equal(b.granted.length, 1);
  assert.equal(b.granted[0]?.released, false);
  b.awake.letGo();
  assert.equal(b.granted[0]?.released, true);
});

test("the lock the browser let go of while the page was hidden is asked for again when it shows", async () => {
  const b = browser();
  b.awake.hold();
  await settle();
  const [first] = b.granted;
  assert.ok(first);
  // The browser lets go of the lock as the page hides.
  first.released = true;
  b.page.show("hidden");
  await settle();
  assert.equal(b.requests(), 1, "nothing is asked for while hidden");
  b.page.show("visible");
  await settle();
  assert.equal(b.granted.length, 2);
  assert.equal(b.granted[1]?.released, false);
});

test("once the session is over, showing the page asks for nothing", async () => {
  const b = browser();
  b.awake.hold();
  await settle();
  b.awake.letGo();
  b.page.show("hidden");
  b.page.show("visible");
  await settle();
  assert.equal(b.requests(), 1);
});

test("each change the session shows while the lock holds asks for no second one", async () => {
  const b = browser();
  b.awake.hold();
  await settle();
  b.awake.hold();
  b.awake.hold();
  await settle();
  assert.equal(b.requests(), 1);
});

test("a session quiet for the cap lets the display turn off, and each change it shows puts the cap off", async () => {
  const b = browser();
  b.awake.hold();
  await settle();
  mock.timers.tick(quietMs - 1);
  b.awake.hold();
  mock.timers.tick(quietMs - 1);
  assert.equal(b.granted[0]?.released, false, "a change put the cap off");
  mock.timers.tick(1);
  assert.equal(b.granted[0]?.released, true);
  assert.deepEqual(b.notes, ["", ""]);
});

test("a lock granted after the session ended is let go of at once", async () => {
  const b = browser("hold");
  b.awake.hold();
  b.awake.letGo();
  b.answer();
  await settle();
  assert.equal(b.granted[0]?.released, true);
});

test("a session started again while the lock is still asked for keeps the lock that arrives", async () => {
  const b = browser("hold");
  b.awake.hold();
  b.awake.letGo();
  b.awake.hold();
  b.answer();
  await settle();
  assert.equal(b.requests(), 1);
  assert.equal(b.granted[0]?.released, false);
});

test("a browser with no wake lock leaves the session as it is, with a quiet note until it ends", () => {
  const notes: string[] = [];
  const page: Visibility = {
    visibilityState: "visible",
    addEventListener: () => {},
    removeEventListener: () => {},
  };
  const none = new StayAwake({}, page, (n) => notes.push(n));
  none.hold();
  none.letGo();
  assert.deepEqual(notes, [mayTurnOff, ""]);
});

test("a refused lock is noted quietly, not asked for at each change, and asked for again when the page next shows", async () => {
  const b = browser("refuse", "grant");
  b.awake.hold();
  await settle();
  assert.deepEqual(b.notes, [mayTurnOff]);
  b.awake.hold();
  await settle();
  assert.equal(b.requests(), 1);
  b.page.show("hidden");
  b.page.show("visible");
  await settle();
  assert.equal(b.granted.length, 1);
  // The note goes once the display stays on.
  assert.deepEqual(b.notes, [mayTurnOff, ""]);
});

test("a refusal that arrives after the session ended adds no note", async () => {
  const b = browser("hold");
  b.awake.hold();
  b.awake.letGo();
  b.refuse();
  await settle();
  assert.deepEqual(b.notes, [""]);
});
