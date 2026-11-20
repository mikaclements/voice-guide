import assert from "node:assert/strict";
import { afterEach, test } from "node:test";
import { ProblemError } from "./api.ts";
import { css, define, ticketAttributeWarning, wanted } from "./element.ts";
import { fakeVoice } from "./fakes.ts";
import { insecureMessage } from "./joined.ts";

const settle = () => new Promise((resolve) => setImmediate(resolve));

// The DOM as far as the element touches it: attributes, text, children, a listener per event, and
// scrolling, where each character of text is a pixel tall and a box shows 50 of them.
class Fake {
  readonly clientHeight = 50;
  #scrollTop = 0;
  get scrollHeight(): number {
    const height = (n: Fake | string): number =>
      typeof n === "string"
        ? n.length
        : n.textContent.length + n.children.reduce((h, c) => h + height(c), 0);
    return height(this);
  }
  get scrollTop() {
    return this.#scrollTop;
  }
  // As a browser does, a scroll stops at either end.
  set scrollTop(top: number) {
    this.#scrollTop = Math.max(0, Math.min(top, this.scrollHeight - this.clientHeight));
  }
  readonly attrs = new Map<string, string>();
  children: (Fake | string)[] = [];
  readonly listeners = new Map<string, (e: object) => void>();
  textContent = "";
  hidden = false;
  disabled = false;
  value = "";
  getAttribute(name: string) {
    return this.attrs.get(name) ?? null;
  }
  setAttribute(name: string, value: string | null) {
    if (value === null) this.attrs.delete(name);
    else this.attrs.set(name, String(value));
  }
  removeAttribute(name: string) {
    this.setAttribute(name, null);
  }
  append(...nodes: (Fake | string)[]) {
    this.children.push(...nodes);
  }
  replaceChildren(...nodes: (Fake | string)[]) {
    this.children = nodes;
  }
  addEventListener(type: string, listener: (e: object) => void) {
    this.listeners.set(type, listener);
  }
  fire(type: string) {
    this.listeners.get(type)?.({ preventDefault() {} });
  }
}

// The element's base: it calls back on observed attributes, as a browser does, and keeps what it
// dispatched and its shadow root, closed or not, for the test to read.
class Host extends Fake {
  isConnected = false;
  readonly events: CustomEvent[] = [];
  readonly shadow = new Fake();
  mode = "";
  attachShadow(init: { mode: string }) {
    this.mode = init.mode;
    return this.shadow;
  }
  setAttribute(name: string, value: string | null) {
    const old = this.getAttribute(name);
    super.setAttribute(name, value);
    const observed = (this.constructor as { observedAttributes?: string[] }).observedAttributes;
    if (observed?.includes(name))
      (this as unknown as Element).attributeChangedCallback(name, old, value);
  }
  dispatchEvent(e: Event) {
    this.events.push(e as CustomEvent);
    return true;
  }
}

type Element = Host & {
  session: string;
  screenInput: { name: string; content: string }[];
  join(given: { session: string; ticket: string }): void;
  end(): Promise<void>;
  connectedCallback(): void;
  disconnectedCallback(): void;
  attributeChangedCallback(name: string, old: string | null, value: string | null): void;
};

const registry = new Map<string, unknown>();
const secure = { isSecureContext: true };
Object.assign(globalThis, {
  HTMLElement: Host,
  customElements: {
    get: (name: string) => registry.get(name),
    define: (name: string, element: unknown) => registry.set(name, element),
  },
  document: { createElement: () => new Fake() },
  window: secure,
});

// A product's page with the element on it, voice faked.
// Every element a test made, taken off the page after it, so no hold on the microphone outlives
// the test.
const removals: (() => void)[] = [];
afterEach(async () => {
  for (const remove of removals.splice(0)) remove();
  await settle();
});

function page() {
  const f = fakeVoice();
  registry.clear();
  globalThis.fetch = f.fetcher;
  define(f.open, f.play);
  const El = registry.get("voice-guide-session") as new () => Element;
  const el = new El();
  const find = (node: Fake | string, name: string): Fake | undefined => {
    if (typeof node === "string") return undefined;
    if (node.getAttribute("part")?.split(" ").includes(name)) return node;
    for (const child of node.children) {
      const found = find(child, name);
      if (found) return found;
    }
  };
  const part = (name: string) => find(el.shadow, name) as Fake;
  const put = () => {
    el.isConnected = true;
    el.connectedCallback();
  };
  const remove = () => {
    el.isConnected = false;
    el.disconnectedCallback();
  };
  removals.push(remove);
  return { f, el, part, put, remove };
}

// voice's refusal of a typed turn, answered when the test chooses.
const refused = () =>
  Response.json(
    { type: "about:blank", title: "Bad Request", status: 400 },
    { status: 400, headers: { "Content-Type": "application/problem+json" } },
  );
function later() {
  let answer: (r: Response) => void = () => {};
  const asked = () => new Promise<Response>((resolve) => (answer = resolve));
  return { asked, answer: (r: Response) => answer(r) };
}
const lines = (p: ReturnType<typeof page>) =>
  p
    .part("transcript")
    .children.map((line) =>
      typeof line === "string"
        ? line
        : line.children.map((c) => (typeof c === "string" ? c : c.textContent)).join(" "),
    );

test("removing the element lets its session go: nothing it hears afterwards is drawn", async () => {
  const p = page();
  p.put();
  p.el.join({ session: "s1", ticket: "tk1" });
  await settle();
  const reply = later();
  p.f.turn = reply.asked;
  p.part("input").value = "Hi";
  p.part("form").fire("submit");
  await settle();
  p.remove();
  await settle();
  reply.answer(refused());
  await settle();
  assert.deepEqual(lines(p), []);
});

test("joining again with a new ticket, or another session, lets the old one go, its words never reaching the new box", async () => {
  const p = page();
  p.put();
  p.el.join({ session: "s1", ticket: "tk1" });
  await settle();
  const reply = later();
  p.f.turn = reply.asked;
  p.part("input").value = "Old words";
  p.part("form").fire("submit");
  await settle();
  p.el.join({ session: "s1", ticket: "tk2" });
  await settle();
  reply.answer(refused());
  await settle();
  assert.deepEqual([p.part("input").value, lines(p)], ["", []]);
  p.f.turn = async () => refused();
  p.part("input").value = "Hi";
  p.part("form").fire("submit");
  await settle();
  assert.equal(p.f.requests.at(-1), "POST /v1/sessions/s1/turns Bearer tk2");
});

test("words given back go to the box, never over what the user is typing", async () => {
  const p = page();
  p.put();
  p.el.join({ session: "s1", ticket: "tk1" });
  await settle();
  p.f.turn = async () => refused();
  const input = p.part("input");
  input.value = "Hi";
  p.part("form").fire("submit");
  await settle();
  assert.equal(input.value, "Hi");
  const reply = later();
  p.f.turn = reply.asked;
  p.part("form").fire("submit");
  input.value = "Draft";
  reply.answer(refused());
  await settle();
  assert.equal(input.value, "Draft");
});

test("a ticket set as an attribute is removed at once, with a warning naming join, and never used", async (t) => {
  const warned = t.mock.method(console, "warn", () => {});
  const p = page();
  p.put();
  p.el.setAttribute("ticket", "tk1");
  await settle();
  assert.equal(p.el.getAttribute("ticket"), null);
  assert.deepEqual(warned.mock.calls[0].arguments, [ticketAttributeWarning]);
  assert.match(ticketAttributeWarning, /join\(\{ session, ticket \}\)/);
  assert.equal(p.el.session, "");
});

test("it draws in a closed shadow root, and Send takes the box's words and empties it", async () => {
  const p = page();
  assert.equal(p.el.mode, "closed");
  p.put();
  p.el.join({ session: "s1", ticket: "tk1" });
  await settle();
  p.part("input").value = "Hi";
  p.part("form").fire("submit");
  assert.equal(p.part("input").value, "");
  await settle();
  assert.deepEqual(lines(p), ["You Hi", "Agent Hello"]);
});

test("the box is drawn inside, where the page's rules can't reach, and the host takes only display", () => {
  const host = css.match(/^:host \{([^}]*)\}/m)?.[1].trim();
  assert.equal(host, "display: block;");
  const frame = css.match(/\[part~="frame"\] \{([^}]*)\}/)?.[1] ?? "";
  for (const style of ["all: initial", "padding", "border:", "font:", "color:", "background:"]) {
    assert.ok(frame.includes(style), style);
  }
});

test("a move on the page keeps the audio connection, and removal closes it", async () => {
  const p = page();
  p.put();
  p.el.join({ session: "s1", ticket: "tk1" });
  await settle();
  const c = p.f.offers[0].open();
  await settle();
  p.remove();
  p.put();
  await settle();
  assert.deepEqual([p.f.offers.length, c.closed], [1, []], "moved: the same connection");
  p.remove();
  await settle();
  assert.deepEqual(c.closed, [1]);
});

test("a new join closes the old audio connection before opening its own", async () => {
  const p = page();
  p.put();
  p.el.join({ session: "s1", ticket: "tk1" });
  await settle();
  const c = p.f.offers[0].open();
  await settle();
  p.el.join({ session: "s1", ticket: "tk2" });
  await settle();
  assert.deepEqual([p.f.offers.length, p.f.offers[1].ticket, c.closed], [2, "tk2", [1]]);
});

test("joins asked for in one go act once, on the last", async () => {
  const p = page();
  p.el.join({ session: "s1", ticket: "tk1" });
  p.put();
  p.el.join({ session: "s2", ticket: "tk2" });
  await settle();
  assert.deepEqual([p.f.offers.length, p.f.offers[0].ticket, p.el.session], [1, "tk2", "s2"]);
});

test("Mute shows once the microphone is on, and a press to hear the agent when the browser won't play it", async () => {
  const p = page();
  p.put();
  p.el.join({ session: "s1", ticket: "tk1" });
  await settle();
  const c = p.f.offers[0].open();
  c.give(true);
  await settle();
  assert.deepEqual(
    [p.part("status").textContent, p.part("mute").textContent],
    ["Agent is about to greet you: your microphone opens when it starts", "Mute"],
  );
  p.part("mute").fire("click");
  assert.deepEqual([c.muted, p.part("mute").textContent], [[true, true], "Unmute"]);
  p.f.played[0].blocked(true);
  assert.deepEqual([p.part("hear").hidden, p.part("status").hidden], [false, true]);
  p.part("hear").fire("click");
  assert.deepEqual([p.f.played[0].plays, p.part("hear").hidden], [1, true]);
});

test("its events leave the shadow root and name the session, never the ticket", async () => {
  const p = page();
  p.put();
  p.el.join({ session: "s1", ticket: "tk-secret" });
  await settle();
  p.f.offers[0].fail(new ProblemError(401, "about:blank", "Unauthorized", "", true));
  await settle();
  assert.deepEqual(
    p.el.events.map((e) => [e.type, e.bubbles, e.composed, e.detail.session]),
    [
      ["voice-guide-error", true, true, "s1"],
      ["voice-guide-ended", true, true, "s1"],
    ],
  );
  assert.deepEqual(p.el.events[0].detail, {
    message: "Unauthorized (401)",
    status: 401,
    type: "about:blank",
    session: "s1",
  });
  assert.equal(p.el.events[1].detail.reason, "unusable");
  assert.doesNotMatch(JSON.stringify(p.el.events.map((e) => e.detail)), /tk-secret/);
});

test("end() right after join ends that session, and rejects when End doesn't reach voice", async () => {
  const p = page();
  p.put();
  p.el.join({ session: "s1", ticket: "tk1" });
  await p.el.end();
  assert.ok(p.f.requests.includes("POST /v1/sessions/s1/end Bearer tk1"));
  assert.equal(p.el.events.at(-1)?.detail.reason, "ended");

  const q = page();
  q.put();
  q.el.join({ session: "s2", ticket: "tk2" });
  q.f.end = () => Promise.reject(new TypeError("Failed to fetch"));
  await assert.rejects(q.el.end(), /Failed to fetch/);
});

test("on a page served over plain http it joins nothing and tells why", async (t) => {
  t.after(() => {
    secure.isSecureContext = true;
  });
  secure.isSecureContext = false;
  const p = page();
  p.put();
  p.el.join({ session: "s1", ticket: "tk1" });
  await settle();
  assert.equal(p.f.offers.length, 0);
  assert.equal(p.el.events[0].detail.message, insecureMessage);
});

// A typed turn whose reply streams in pieces the test sends, then ends.
function streamed(p: ReturnType<typeof page>) {
  let push: (chunk: string) => void = () => {};
  p.f.turn = async () =>
    new Response(
      new ReadableStream<Uint8Array>({
        start(c) {
          push = (chunk) => c.enqueue(new TextEncoder().encode(chunk));
        },
      }),
      { headers: { "Content-Type": "text/event-stream" } },
    );
  return {
    piece: (text: string) => push(`event: text\ndata: ${JSON.stringify({ text })}\n\n`),
    done: () => push("event: done\ndata: {}\n\n"),
  };
}

// A page holding a session whose transcript is already taller than its box.
async function talked() {
  const p = page();
  p.put();
  p.el.join({ session: "s1", ticket: "tk1" });
  await settle();
  for (const words of ["A first question, long enough to scroll", "And a second one"]) {
    p.part("input").value = words;
    p.part("form").fire("submit");
    await settle();
  }
  const transcript = p.part("transcript");
  const bottom = () => transcript.scrollHeight - transcript.clientHeight;
  return { p, transcript, bottom };
}

test("while the user is at the bottom, the transcript follows each new line and a reply as it streams in", async () => {
  const { p, transcript, bottom } = await talked();
  assert.ok(bottom() > 0);
  assert.equal(transcript.scrollTop, bottom());
  const reply = streamed(p);
  p.part("input").value = "Tell me more";
  p.part("form").fire("submit");
  await settle();
  assert.equal(transcript.scrollTop, bottom(), "the new line");
  for (const piece of ["Here is ", "a long answer, ", "streaming in."]) {
    reply.piece(piece);
    await settle();
    assert.equal(transcript.scrollTop, bottom(), piece);
  }
  reply.done();
  await settle();
  assert.match(lines(p).at(-1) ?? "", /streaming in\.$/);
});

test("once the user has scrolled up, new lines and a streaming reply leave the transcript where it is", async () => {
  const { p, transcript, bottom } = await talked();
  transcript.scrollTop = bottom() - 20;
  const read = transcript.scrollTop;
  const reply = streamed(p);
  p.part("input").value = "Tell me more";
  p.part("form").fire("submit");
  await settle();
  reply.piece("Here is a long answer");
  await settle();
  assert.deepEqual([transcript.scrollTop, bottom() > read + 20], [read, true]);
});

test("scrolling up while a reply streams in stops following at once", async () => {
  const { p, transcript, bottom } = await talked();
  const reply = streamed(p);
  p.part("input").value = "Tell me more";
  p.part("form").fire("submit");
  await settle();
  reply.piece("Here is ");
  await settle();
  assert.equal(transcript.scrollTop, bottom(), "following before the user scrolled");
  transcript.scrollTop = bottom() - 20;
  const read = transcript.scrollTop;
  reply.piece("a long answer, streaming in.");
  await settle();
  assert.deepEqual([transcript.scrollTop, bottom() > read + 20], [read, true]);
});

test("scrolling back to within a few pixels of the end follows again", async () => {
  const { p, transcript, bottom } = await talked();
  transcript.scrollTop = 0;
  transcript.scrollTop = bottom() - 2;
  p.part("input").value = "Tell me more";
  p.part("form").fire("submit");
  await settle();
  assert.equal(transcript.scrollTop, bottom());
});

test("the element joins what it was given once it is on the page, voice at api, else its script's origin", () => {
  const from = "https://voice.example";
  const given = { session: "s1", ticket: "tk1" };
  assert.equal(wanted(false, given, null, from), undefined, "off the page");
  assert.equal(wanted(true, undefined, null, from), undefined, "nothing given yet");
  assert.equal(wanted(true, { session: " ", ticket: "tk1" }, null, from), undefined);
  const join = wanted(true, given, null, from);
  assert.deepEqual(join, { ...given, api: from, key: `${from} s1 tk1` });
  const other = wanted(true, { session: "s1", ticket: "tk2" }, "https://v.example/", from);
  assert.equal(other?.api, "https://v.example");
  assert.notEqual(other?.key, join?.key, "a new ticket joins again");
  assert.equal(wanted(true, given, null, "")?.api, "", "the page's own origin");
});

test("screen input set before join goes on the audio connection once it opens, and a block voice refuses is told in an event", async () => {
  const p = page();
  const board = { name: "board", content: "Cache -> Database" };
  p.el.screenInput = [board];
  assert.throws(() => {
    p.el.screenInput = [board, board];
  }, TypeError);
  assert.deepEqual(p.el.screenInput, [board]);
  p.put();
  p.el.join({ session: "s1", ticket: "tk1" });
  await settle();
  const c = p.f.offers[0].open();
  await settle();
  assert.deepEqual(c.sent, [{ type: "screen", n: 1, screen_input: [board] }]);
  c.audio.turns.receive(JSON.stringify({ type: "screen_refused", n: 1, detail: "too large" }));
  const told = p.el.events.at(-1);
  assert.deepEqual(
    [told?.type, told?.detail],
    ["voice-guide-screen-refused", { name: "board", message: "too large", session: "s1" }],
  );
});
