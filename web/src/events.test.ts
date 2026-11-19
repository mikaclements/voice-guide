import assert from "node:assert/strict";
import { test } from "node:test";
import { EventParser, type ServerEvent } from "./events.ts";

// What voice sends for a two-piece reply.
const stream =
  'event: text\ndata: {"text":"Hello"}\n\nevent: text\ndata: {"text":" there"}\n\n' +
  'event: done\ndata: {"turn_id":"t1","seq":2}\n\n';
const want: ServerEvent[] = [
  { event: "text", data: '{"text":"Hello"}' },
  { event: "text", data: '{"text":" there"}' },
  { event: "done", data: '{"turn_id":"t1","seq":2}' },
];

test("a whole stream gives each event in order", () => {
  assert.deepEqual(new EventParser().push(stream), want);
});

test("a stream cut into chunks anywhere gives the same events, with LF or CRLF line ends", () => {
  for (const s of [stream, stream.replaceAll("\n", "\r\n")]) {
    for (let i = 1; i < s.length; i++) {
      const p = new EventParser();
      const got = [...p.push(s.slice(0, i)), ...p.push(s.slice(i))];
      assert.deepEqual(got, want, `cut at ${i} of ${JSON.stringify(s)}`);
    }
  }
});

test("an event isn't given until its blank line arrives", () => {
  const p = new EventParser();
  assert.deepEqual(p.push('event: text\ndata: {"text":"Hi"}\n'), []);
  assert.deepEqual(p.push("\n"), [{ event: "text", data: '{"text":"Hi"}' }]);
});

test("CRLF and CR line endings work, even split between chunks", () => {
  const p = new EventParser();
  const got = [...p.push("event: a\r\ndata: 1\r"), ...p.push("\n\r\nevent: b\rdata: 2\r\r\n")];
  assert.deepEqual(got, [
    { event: "a", data: "1" },
    { event: "b", data: "2" },
  ]);
});

test("data lines join, comments and unknown fields are skipped, no event name means message", () => {
  const got = new EventParser().push(": ping\nid: 7\ndata: one\ndata:two\n\nevent: x\n\n");
  assert.deepEqual(got, [{ event: "message", data: "one\ntwo" }]);
});
