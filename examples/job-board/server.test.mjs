import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { request } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { createJobBoard, readEnv, seedFile, verifyWebhook } from "./server.mjs";

// The published example of the signing format, with its 24-byte secret.
const EX_SECRET = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw";
const EX_ID = "msg_p5jXN8AQM9LWM0D4loKWxJek";
const TS = 1614265330;
const EX_BODY = '{"test": 2432232314}';
const EX_SIG = "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=";
const exHeaders = (over = {}) => ({
  "webhook-id": EX_ID,
  "webhook-timestamp": String(TS),
  "webhook-signature": EX_SIG,
  ...over,
});

const SECRET = `whsec_${Buffer.alloc(32, 7).toString("base64")}`;
const KEY = "vgk_secretkey";
const UUID = "0198a5c2-7d3e-7a10-8b2f-3c4d5e6f7a8b";
const sign = (id, body, ts = TS) =>
  `v1,${createHmac("sha256", Buffer.from(SECRET.slice(6), "base64"))
    .update(`${id}.${ts}.${body}`)
    .digest("base64")}`;

test("the published signing example verifies", () => {
  assert.equal(verifyWebhook(EX_SECRET, exHeaders(), EX_BODY, TS), true);
});

test("a wrong signature, body or secret is refused", () => {
  assert.equal(verifyWebhook(EX_SECRET, exHeaders(), `${EX_BODY} `, TS), false);
  assert.equal(
    verifyWebhook(EX_SECRET, exHeaders({ "webhook-signature": "v1,AAAA" }), EX_BODY, TS),
    false,
  );
  assert.equal(verifyWebhook("whsec_AAAA", exHeaders(), EX_BODY, TS), false);
  assert.equal(verifyWebhook("", exHeaders(), EX_BODY, TS), false);
  assert.equal(verifyWebhook("whsec_", exHeaders(), EX_BODY, TS), false);
});

test("a timestamp more than five minutes off is refused, either way", () => {
  assert.equal(verifyWebhook(EX_SECRET, exHeaders(), EX_BODY, TS + 299), true);
  assert.equal(verifyWebhook(EX_SECRET, exHeaders(), EX_BODY, TS + 301), false);
  assert.equal(verifyWebhook(EX_SECRET, exHeaders(), EX_BODY, TS - 301), false);
});

test("several signatures: a right second one verifies, a non-v1 version does not", () => {
  const sig = (h) => verifyWebhook(EX_SECRET, exHeaders({ "webhook-signature": h }), EX_BODY, TS);
  assert.equal(sig(`v1,AAAA ${EX_SIG}`), true);
  assert.equal(sig(EX_SIG.replace("v1,", "v2,")), false);
  assert.equal(sig(`v2,${EX_SIG.slice(3)} v1,AAAA`), false);
});

test("a missing header or a non-numeric timestamp is refused", () => {
  for (const name of ["webhook-id", "webhook-timestamp", "webhook-signature"]) {
    assert.equal(verifyWebhook(EX_SECRET, exHeaders({ [name]: undefined }), EX_BODY, TS), false);
  }
  for (const ts of ["abc", "1.5", "-5", "0x10"]) {
    assert.equal(
      verifyWebhook(EX_SECRET, exHeaders({ "webhook-timestamp": ts }), EX_BODY, TS),
      false,
    );
  }
});

test("readEnv reads only the three settings, as the shell would, and the environment wins", () => {
  const file = join(mkdtempSync(join(tmpdir(), "jb-")), ".env");
  writeFileSync(
    file,
    [
      "# a comment",
      "VOICE_TICKET_KEY=signing",
      "DATABASE_URL=postgres://x",
      "VOICE_DEV_PRODUCT_KEY=vgk_abc # trailing comment",
      'VOICE_ADDR="127.0.0.1:9090"',
      "export JOB_BOARD_WEBHOOK_SECRET='whsec_x'",
    ].join("\n"),
  );
  assert.deepEqual(readEnv(file, {}), {
    VOICE_DEV_PRODUCT_KEY: "vgk_abc",
    VOICE_ADDR: "127.0.0.1:9090",
    JOB_BOARD_WEBHOOK_SECRET: "whsec_x",
  });
  assert.equal(readEnv(file, { VOICE_ADDR: "h:1", HOME: "/x" }).VOICE_ADDR, "h:1");
  assert.equal(readEnv(file, { HOME: "/x" }).HOME, undefined);
  // The webhook secret comes from the file alone.
  const fromEnv = { JOB_BOARD_WEBHOOK_SECRET: "whsec_env" };
  assert.equal(readEnv(file, fromEnv).JOB_BOARD_WEBHOOK_SECRET, "whsec_x");
  assert.equal(readEnv("/nonexistent", fromEnv).JOB_BOARD_WEBHOOK_SECRET, undefined);
});

const make = (over = {}) => {
  const sent = [];
  const logs = [];
  const board = {
    voiceUrl: "http://voice.test",
    productKey: KEY,
    webhookSecret: SECRET,
    user: "placeholder-user",
    resume: "the resume",
    now: () => TS,
    log: (l) => logs.push(l),
    fetchImpl: async (url, init) => {
      sent.push({ url, init });
      return Response.json({ id: "s1", ticket: "t1", agent_id: "a", extra: "x" });
    },
    ...over,
  };
  return { board, sent, logs };
};

async function start(over) {
  const { board, sent, logs } = make(over);
  const server = createJobBoard(board);
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  const port = server.address().port;
  return { base: `http://127.0.0.1:${port}`, port, sent, logs, close: () => server.close() };
}

// A request with a Host header of our choosing, which fetch won't send.
function raw(port, { method = "GET", path = "/", headers = {}, body }) {
  return new Promise((resolve, reject) => {
    const req = request({ host: "127.0.0.1", port, method, path, headers, agent: false }, (res) => {
      let text = "";
      res.on("data", (c) => {
        text += c;
      });
      res.on("end", () => resolve({ status: res.statusCode, headers: res.headers, text }));
    });
    req.on("error", reject);
    req.end(body);
  });
}

const practice = (s, body, headers = { "Content-Type": "application/json" }) =>
  fetch(`${s.base}/api/practice`, { method: "POST", headers, body });
const asJson = (o) => JSON.stringify(o);

test("a webhook secret that is empty or not 32 bytes stops the board from starting", () => {
  for (const webhookSecret of [undefined, "", "whsec_", EX_SECRET]) {
    assert.throws(() => createJobBoard(make({ webhookSecret }).board));
  }
});

test("/api/practice answers only the session and ticket, passing the request ID to voice", async () => {
  const s = await start();
  try {
    const res = await practice(s, asJson({ request_id: UUID }));
    assert.equal(res.status, 200);
    assert.equal(res.headers.get("cache-control"), "no-store");
    assert.equal(res.headers.get("access-control-allow-origin"), null);
    const text = await res.text();
    assert.deepEqual(JSON.parse(text), { id: "s1", ticket: "t1" });
    assert.ok(!text.includes(KEY));
    assert.equal(s.sent[0].url, "http://voice.test/v1/sessions");
    assert.equal(s.sent[0].init.headers.Authorization, `Bearer ${KEY}`);
    assert.deepEqual(JSON.parse(s.sent[0].init.body), {
      agent_id: "01a1de84-fbfc-7758-9af4-3ced6d8a90cc",
      request_id: UUID,
      user: "placeholder-user",
      session_context: [{ name: "resume", content: "the resume" }],
    });
    assert.ok(!s.logs.join("\n").includes(KEY));
  } finally {
    s.close();
  }
});

test("/api/practice refuses a body that is not JSON, a request ID that is no UUID, or too large", async () => {
  const s = await start();
  try {
    assert.equal((await practice(s, "hi", { "Content-Type": "text/plain" })).status, 415);
    assert.equal((await practice(s, "{not json")).status, 400);
    assert.equal((await practice(s, asJson({}))).status, 400);
    assert.equal((await practice(s, asJson({ request_id: "abc" }))).status, 400);
    assert.equal(
      (await practice(s, asJson({ request_id: UUID, pad: "x".repeat(5000) }))).status,
      413,
    );
    assert.equal(s.sent.length, 0);
  } finally {
    s.close();
  }
});

test("voice down, failing, slow, refusing or giving an unusable session: 502 to retry, 409 to restart", async () => {
  const cases = [
    [
      502,
      "voice call failed: ECONNREFUSED",
      async () => {
        throw new TypeError(`fetch failed with ${KEY}`, { cause: { code: "ECONNREFUSED" } });
      },
    ],
    [502, "voice answered 500", async () => new Response(`bad ${KEY}`, { status: 500 })],
    [
      409,
      "voice refused the session: 401",
      async () => new Response(`bad ${KEY}`, { status: 401 }),
    ],
    [409, "voice refused the session: 422", async () => new Response("{}", { status: 422 })],
    [
      409,
      "voice answered with a session that cannot be joined",
      async () => Response.json({ id: "s1" }),
    ],
    [
      409,
      "voice answered with a session that cannot be joined",
      async () => Response.json({ id: "s1", ticket: "t1", ended_at: "2026-01-01T00:00:00Z" }),
    ],
    [
      504,
      "voice call failed: timeout",
      (_url, init) =>
        new Promise((_, reject) => {
          init.signal.addEventListener("abort", () => reject(init.signal.reason));
        }),
    ],
  ];
  for (const [status, logged, fetchImpl] of cases) {
    const s = await start({ fetchImpl, voiceTimeoutMs: 20 });
    try {
      const res = await practice(s, asJson({ request_id: UUID }));
      assert.equal(res.status, status);
      const text = await res.text();
      assert.ok(!text.includes(KEY));
      if (status === 409) assert.deepEqual(JSON.parse(text), { restart: true });
      assert.deepEqual(s.logs, [logged]);
    } finally {
      s.close();
    }
  }
});

test("only this server's own Host names are served, before anything else", async () => {
  const s = await start();
  try {
    for (const [path, method] of [
      ["/", "GET"],
      ["/api/last-session", "GET"],
      ["/api/practice", "POST"],
      ["/webhook", "POST"],
    ]) {
      const bad = await raw(s.port, {
        method,
        path,
        headers: { Host: "evil.example:5190", "Content-Type": "application/json" },
        body: asJson({ request_id: UUID }),
      });
      assert.equal(bad.status, 421, path);
    }
    assert.equal(s.sent.length, 0);
    assert.equal((await raw(s.port, { headers: { Host: `localhost:${s.port}` } })).status, 200);
    assert.equal((await raw(s.port, { headers: { Host: `127.0.0.1:${s.port}` } })).status, 200);
  } finally {
    s.close();
  }
});

test("a preflight is answered without CORS headers", async () => {
  const s = await start();
  try {
    const res = await raw(s.port, {
      method: "OPTIONS",
      path: "/api/practice",
      headers: {
        Host: `127.0.0.1:${s.port}`,
        Origin: "https://evil.example",
        "Access-Control-Request-Method": "POST",
      },
    });
    assert.equal(res.status, 405);
    assert.ok(!Object.keys(res.headers).some((h) => h.startsWith("access-control-")));
  } finally {
    s.close();
  }
});

test("the page is served with voice's address in it", async () => {
  const s = await start();
  try {
    const text = await (await fetch(`${s.base}/`)).text();
    assert.ok(text.includes("http://voice.test/client/voice-guide.js"));
    assert.ok(!text.includes("{{"));
  } finally {
    s.close();
  }
});

const ended = (over = {}) =>
  asJson({
    type: "session.ended",
    data: {
      id: "s1",
      user: "placeholder-user",
      turns: [{ words: "secret words" }, {}],
      summary: "Went well.\nMore.",
      ...over,
    },
  });

test("/webhook handles a signed delivery once and shows its feedback by session", async () => {
  const s = await start();
  const body = ended();
  const post = (id, signature, b = body) =>
    fetch(`${s.base}/webhook`, {
      method: "POST",
      headers: {
        "webhook-id": id,
        "webhook-timestamp": String(TS),
        "webhook-signature": signature,
      },
      body: b,
    });
  try {
    const feedback = (id) => fetch(`${s.base}/api/last-session?session=${id}`);
    assert.deepEqual(await (await feedback("s1")).json(), { ready: false });
    assert.equal((await post("m1", "v1,AAAA")).status, 401);
    assert.deepEqual(s.logs, ['webhook refused: bad signature or clock (webhook-id "m1")']);
    s.logs.length = 0;
    assert.equal((await post("m1", sign("m1", body))).status, 200);
    assert.equal((await post("m1", sign("m1", body))).status, 200);
    assert.deepEqual(s.logs, ["session s1 ended: 2 turns, summary written"]);
    assert.deepEqual(await (await feedback("s1")).json(), {
      ready: true,
      session: "s1",
      turns: 2,
      summary: "Went well.\nMore.",
    });
    // Another session is not that one, and none named is nothing.
    assert.deepEqual(await (await feedback("s2")).json(), { ready: false });
    assert.equal((await fetch(`${s.base}/api/last-session`)).status, 400);
    // Another event type is answered 2xx and changes nothing.
    const other = asJson({ type: "something.else", data: {} });
    assert.equal((await post("m2", sign("m2", other), other)).status, 200);
    assert.equal(s.logs.length, 1);
  } finally {
    s.close();
  }
});

test("a delivery that fails to process is not marked handled, so its retry is", async () => {
  const s = await start();
  const post = (b) =>
    fetch(`${s.base}/webhook`, {
      method: "POST",
      headers: {
        "webhook-id": "m3",
        "webhook-timestamp": String(TS),
        "webhook-signature": sign("m3", b),
      },
      body: b,
    });
  try {
    assert.equal((await post(ended({ turns: null }))).status, 500);
    assert.equal((await post(ended())).status, 200);
    assert.equal(s.logs.filter((l) => l.startsWith("session")).length, 1);
  } finally {
    s.close();
  }
});

test("/webhook refuses a body over 256 KiB with 413", async () => {
  const s = await start();
  try {
    const body = ended({ summary: "x".repeat(300 * 1024) });
    const res = await fetch(`${s.base}/webhook`, {
      method: "POST",
      headers: {
        "webhook-id": "m4",
        "webhook-timestamp": String(TS),
        "webhook-signature": sign("m4", body),
      },
      body,
    });
    assert.equal(res.status, 413);
    assert.equal(s.logs.length, 0);
  } finally {
    s.close();
  }
});

test("feedback is kept per session, the latest 20, and counts the turns left out", async () => {
  const s = await start();
  const deliver = (n, over) => {
    const body = ended({ id: `s${n}`, ...over });
    return fetch(`${s.base}/webhook`, {
      method: "POST",
      headers: {
        "webhook-id": `d${n}`,
        "webhook-timestamp": String(TS),
        "webhook-signature": sign(`d${n}`, body),
      },
      body,
    });
  };
  const feedback = (n) => fetch(`${s.base}/api/last-session?session=s${n}`);
  try {
    // A later session doesn't overwrite an earlier one.
    await deliver(1, { turns_left_out: 5, summary: "first" });
    await deliver(2, { summary: "second" });
    assert.deepEqual(await (await feedback(1)).json(), {
      ready: true,
      session: "s1",
      turns: 7,
      summary: "first",
    });
    assert.equal((await (await feedback(2)).json()).summary, "second");
    for (let n = 3; n <= 22; n++) await deliver(n, {});
    const ready = async (n) => (await (await feedback(n)).json()).ready;
    assert.equal(await ready(1), false);
    assert.equal(await ready(2), false);
    assert.equal(await ready(3), true);
    assert.equal(await ready(22), true);
  } finally {
    s.close();
  }
});

test("a file in seed/local is used, and the sample otherwise, each file on its own", () => {
  const seed = mkdtempSync(join(tmpdir(), "seed-"));
  mkdirSync(join(seed, "local"));
  mkdirSync(join(seed, "placeholder-user"));
  writeFileSync(join(seed, "local", "resume.md"), "mine");
  assert.equal(seedFile(seed, "resume.md"), join(seed, "local", "resume.md"));
  assert.equal(seedFile(seed, "reference.txt"), join(seed, "placeholder-user", "reference.txt"));
});
