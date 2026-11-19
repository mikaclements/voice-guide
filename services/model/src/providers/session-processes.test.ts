import assert from "node:assert/strict";
import { test } from "node:test";
import { SessionProcesses } from "./session-processes.ts";

const process = () => ({
  closed: false,
  close() {
    this.closed = true;
  },
});

test("a session process is taken once, by its own session; a second one replaces it", () => {
  const processes = new SessionProcesses(2, 60_000);
  const [a, b] = [process(), process()];
  processes.put("s1", a);
  assert.equal(processes.take("s2"), undefined);
  assert.equal(processes.take("s1"), a);
  assert.equal(processes.take("s1"), undefined);
  processes.put("s1", a);
  processes.put("s1", b);
  assert.deepEqual([a.closed, processes.take("s1")], [true, b]);
});

test("past the cap, the least recently used process is closed; with a cap of zero, none is kept", () => {
  const processes = new SessionProcesses(2, 60_000);
  const [a, b, c] = [process(), process(), process()];
  processes.put("s1", a);
  processes.put("s2", b);
  // s1 answers another turn, so s2 is now the least recently used.
  processes.put("s1", processes.take("s1") ?? a);
  processes.put("s3", c);
  assert.deepEqual([a.closed, b.closed, c.closed], [false, true, false]);

  const none = new SessionProcesses(0, 60_000);
  const d = process();
  none.put("s1", d);
  assert.deepEqual([d.closed, none.take("s1")], [true, undefined]);
});

test("a process unused for the idle time is closed; one in use is not", (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const processes = new SessionProcesses(2, 1_000);
  const [a, b] = [process(), process()];
  processes.put("s1", a);
  processes.put("s2", b);
  t.mock.timers.tick(999);
  assert.equal(processes.take("s2"), b);
  t.mock.timers.tick(1);
  assert.deepEqual([a.closed, b.closed, processes.take("s1")], [true, false, undefined]);
});

test("closing all closes every session process", () => {
  const processes = new SessionProcesses(2, 60_000);
  const [a, b] = [process(), process()];
  processes.put("s1", a);
  processes.put("s2", b);
  processes.closeAll();
  assert.deepEqual([a.closed, b.closed, processes.take("s1")], [true, true, undefined]);
});
