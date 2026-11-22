import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import {
  contextBlock,
  defaultTimeoutMs,
  productRoutes,
  viteAllowedHosts,
} from "./product-server.ts";
import { seedFile } from "./seed.ts";

async function userFolder(t: { after(fn: () => unknown): void }) {
  const dir = await mkdtemp(join(tmpdir(), "context-route-"));
  t.after(() => rm(dir, { recursive: true }));
  const folder = join(dir, "user");
  await mkdir(folder);
  await writeFile(join(folder, "log.md"), "Keeps the lamp lit.");
  await writeFile(join(folder, "log.old.md"), "Kept the lamp lit.");
  await writeFile(join(folder, "reference.txt"), "keeper-1\n");
  await writeFile(join(dir, "secret.md"), "The key is under the mat.");
  return folder;
}

test("a context block is read by name, and nothing outside its folder", async (t) => {
  const folder = await userFolder(t);
  const cases: [string, string | undefined, string][] = [
    ["log", "Keeps the lamp lit.", "a block the folder has"],
    ["chart", undefined, "a block the folder doesn't have"],
    ["../secret", undefined, "a path out of the folder"],
    ["log.old", undefined, "a name with a dot, though its file is there"],
    ["reference", undefined, "a file that is not a block"],
  ];
  for (const [name, want, what] of cases) {
    assert.equal(await contextBlock(folder, name), want, what);
  }
});

const key = "vgk_the-product-key";

// A voice that answers the agents list and each start, recording what the product's server sent it.
function voice(started: Response = Response.json({ id: "s1", ticket: "tk" }, { status: 201 })) {
  const calls: { url: string; auth: string | null; body?: unknown }[] = [];
  const fetcher = (async (url: string, init?: RequestInit) => {
    const headers = new Headers(init?.headers);
    calls.push({
      url,
      auth: headers.get("Authorization"),
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });
    if (url.endsWith("/v1/agents")) {
      return Response.json({
        agents: [
          { id: "keeper", session_context: ["log"] },
          { id: "plain", session_context: [] },
        ],
      });
    }
    return started.clone();
  }) as typeof fetch;
  return { fetcher, calls };
}

function request(url: string, method: string, body = "") {
  return {
    url,
    method,
    headers: { "content-type": "application/json", host: "127.0.0.1:5173" },
    on(event: string, cb: (chunk: string) => void) {
      if (event === "data" && body) cb(body);
      if (event === "end") cb("");
    },
  };
}

function reply() {
  const res = {
    statusCode: 0,
    headers: {} as Record<string, string>,
    body: "",
    setHeader(name: string, value: string) {
      res.headers[name] = value;
    },
    end(body: string) {
      res.body = body;
    },
  };
  return res;
}

test("a start goes to voice with the product key, the user's reference and the context the agent asks for", async (t) => {
  const v = voice();
  const route = productRoutes({
    voice: "http://voice",
    key,
    user: await userFolder(t),
    fetcher: v.fetcher,
  });
  const res = reply();
  await route(request("/start", "POST", '{"agent_id":"keeper","request_id":"r1"}'), res);

  assert.equal(res.statusCode, 201);
  assert.deepEqual(JSON.parse(res.body), { id: "s1", ticket: "tk" });
  assert.equal(res.headers["Cache-Control"], "no-store", "the reply carries a credential");
  const start = v.calls.at(-1);
  assert.equal(start?.url, "http://voice/v1/sessions");
  assert.equal(start?.auth, `Bearer ${key}`);
  assert.deepEqual(start?.body, {
    agent_id: "keeper",
    request_id: "r1",
    user: "keeper-1",
    session_context: [{ name: "log", content: "Keeps the lamp lit." }],
  });
  assert.doesNotMatch(res.body, /product-key/, "the key never reaches the page");
});

test("an agent that asks for no context starts with none", async (t) => {
  const v = voice();
  const route = productRoutes({
    voice: "http://voice",
    key,
    user: await userFolder(t),
    fetcher: v.fetcher,
  });
  await route(request("/start", "POST", '{"agent_id":"plain","request_id":"r2"}'), reply());
  const start = v.calls.at(-1);
  assert.ok(start);
  assert.deepEqual((start.body as { session_context: unknown }).session_context, []);
});

test("a context block the product doesn't have stops the start before voice", async (t) => {
  const folder = await userFolder(t);
  await rm(join(folder, "log.md"));
  const v = voice();
  const route = productRoutes({ voice: "http://voice", key, user: folder, fetcher: v.fetcher });
  const res = reply();
  await route(request("/start", "POST", '{"agent_id":"keeper","request_id":"r1"}'), res);
  assert.equal(res.statusCode, 404);
  assert.match(res.body, /no "log"/);
  assert.equal(
    v.calls.filter((c) => c.url.endsWith("/v1/sessions")).length,
    0,
    "voice never heard the start",
  );
});

test("voice's refusal passes through as it is", async (t) => {
  const problem = Response.json(
    { type: "/v1/problems/session-context-mismatch", title: "T", status: 422 },
    { status: 422, headers: { "Content-Type": "application/problem+json" } },
  );
  const route = productRoutes({
    voice: "http://voice",
    key,
    user: await userFolder(t),
    fetcher: voice(problem).fetcher,
  });
  const res = reply();
  await route(request("/start", "POST", '{"agent_id":"keeper","request_id":"r1"}'), res);
  assert.equal(res.statusCode, 422);
  assert.equal(res.headers["Content-Type"], "application/problem+json");
  assert.equal(JSON.parse(res.body).type, "/v1/problems/session-context-mismatch");
});

test("the agents are listed with the key", async (t) => {
  const v = voice();
  const route = productRoutes({
    voice: "http://voice",
    key,
    user: await userFolder(t),
    fetcher: v.fetcher,
  });
  const res = reply();
  await route(request("/agents", "GET"), res);
  assert.equal(res.statusCode, 200);
  assert.equal(v.calls[0].auth, `Bearer ${key}`);
  assert.equal(JSON.parse(res.body).agents.length, 2);
});

test("a server without a key, a body that isn't a request, and a voice that is down are told plainly", async (t) => {
  const user = await userFolder(t);
  const down = (async () => {
    throw new TypeError("fetch failed");
  }) as typeof fetch;
  const cases: [
    string,
    ReturnType<typeof request>,
    Parameters<typeof productRoutes>[0],
    number,
    RegExp,
  ][] = [
    [
      "no key",
      request("/agents", "GET"),
      { voice: "http://voice", key: "", user },
      500,
      /VOICE_DEV_PRODUCT_KEY/,
    ],
    [
      "not JSON",
      request("/start", "POST", "start please"),
      { voice: "http://voice", key, user },
      400,
      /JSON object/,
    ],
    [
      "voice down",
      request("/agents", "GET"),
      { voice: "http://voice", key, user, fetcher: down },
      502,
      /fetch failed/,
    ],
    [
      "another route",
      request("/other", "GET"),
      { voice: "http://voice", key, user },
      404,
      /no such route/,
    ],
  ];
  for (const [what, req, options, status, text] of cases) {
    const res = reply();
    await productRoutes(options)(req, res);
    assert.equal(res.statusCode, status, what);
    assert.match(res.body, text, what);
    assert.doesNotMatch(res.body, /the-product-key/, what);
  }
});

test("a start not sent as JSON is refused before anything is read or sent", async (t) => {
  const v = voice();
  const route = productRoutes({
    voice: "http://voice",
    key,
    user: await userFolder(t),
    fetcher: v.fetcher,
  });
  for (const type of [undefined, "text/plain", "application/x-www-form-urlencoded"]) {
    const res = reply();
    const req = {
      ...request("/start", "POST", '{"agent_id":"keeper","request_id":"r1"}'),
      headers: type ? { "content-type": type, host: "127.0.0.1:5173" } : { host: "127.0.0.1:5173" },
    };
    await route(req, res);
    assert.equal(res.statusCode, 415, String(type));
  }
  assert.equal(v.calls.length, 0, "voice never heard of it");
});

test("a voice that doesn't answer in time ends the call with an error", async (t) => {
  const hangs = ((_url: string, init?: RequestInit) =>
    new Promise((_resolve, reject) => {
      init?.signal?.addEventListener("abort", () => reject(init.signal?.reason));
    })) as typeof fetch;
  const route = productRoutes({
    voice: "http://voice",
    key,
    user: await userFolder(t),
    fetcher: hangs,
    timeoutMs: 20,
  });
  const res = reply();
  await route(request("/agents", "GET"), res);
  assert.equal(res.statusCode, 502);
  assert.match(res.body, /couldn't reach voice/);
});

test("a call to voice gives up after ten seconds unless told otherwise", async (t) => {
  assert.equal(defaultTimeoutMs, 10_000);
  const timeout = t.mock.method(AbortSignal, "timeout");
  const v = voice();
  const route = productRoutes({
    voice: "http://voice",
    key,
    user: await userFolder(t),
    fetcher: v.fetcher,
  });
  await route(request("/agents", "GET"), reply());
  assert.deepEqual(timeout.mock.calls[0].arguments, [10_000]);
});

test("voice refusing the agent list inside a start is the product's failure, not voice's answer to the start", async (t) => {
  const refusing = (async () =>
    Response.json(
      { type: "about:blank", title: "Unauthorized", status: 401 },
      { status: 401, headers: { "Content-Type": "application/problem+json" } },
    )) as typeof fetch;
  const route = productRoutes({
    voice: "http://voice",
    key,
    user: await userFolder(t),
    fetcher: refusing,
  });
  const res = reply();
  await route(request("/start", "POST", '{"agent_id":"keeper","request_id":"r1"}'), res);
  assert.equal(res.statusCode, 502);
  assert.notEqual(res.headers["Content-Type"], "application/problem+json");
  assert.match(res.body, /agent list \(401\)/);
});

test("a file in the local folder is used, and the sample otherwise", async (t) => {
  const dir = await mkdtemp(join(tmpdir(), "seed-file-"));
  t.after(() => rm(dir, { recursive: true }));
  const sample = join(dir, "sample");
  const local = join(dir, "local");
  await mkdir(sample);
  await mkdir(local);
  await writeFile(join(local, "resume.md"), "mine");
  assert.equal(seedFile(sample, local, "resume.md"), join(local, "resume.md"));
  assert.equal(seedFile(sample, local, "reference.txt"), join(sample, "reference.txt"));
  assert.equal(seedFile(sample, undefined, "resume.md"), join(sample, "resume.md"));
  assert.equal(await contextBlock(sample, "resume", local), "mine");
});

// A voice that also answers the voices and a user's sessions, recording what it was sent.
function richVoice() {
  const v = voice();
  const calls = v.calls;
  const fetcher = (async (url: string, init?: RequestInit) => {
    if (url.endsWith("/v1/voices")) {
      calls.push({ url, auth: new Headers(init?.headers).get("Authorization") });
      return Response.json({ voices: ["en_us", "ja_jp"] });
    }
    if (url.includes("/v1/users/")) {
      calls.push({ url, auth: new Headers(init?.headers).get("Authorization") });
      return Response.json({ sessions: [{ id: "s1" }] });
    }
    return v.fetcher(url, init);
  }) as typeof fetch;
  return { fetcher, calls };
}

async function richRoute(
  t: { after(fn: () => unknown): void },
  fetcher?: typeof fetch,
  hosts?: string[],
) {
  const v = richVoice();
  const route = productRoutes({
    voice: "http://voice",
    key,
    user: await userFolder(t),
    fetcher: fetcher ?? v.fetcher,
    hosts,
  });
  return { route, calls: v.calls };
}

test("a start's voice, speed and documents go to voice, and a document it leaves out comes from the user's files", async (t) => {
  const { route, calls } = await richRoute(t);
  const res = reply();
  await route(
    request(
      "/start",
      "POST",
      JSON.stringify({
        agent_id: "keeper",
        request_id: "r1",
        voice: "ja_jp",
        speed: 1.25,
        session_context: [{ name: "log", content: "Mine." }],
      }),
    ),
    res,
  );
  assert.equal(res.statusCode, 201);
  assert.deepEqual(calls.at(-1)?.body, {
    agent_id: "keeper",
    request_id: "r1",
    user: "keeper-1",
    voice: "ja_jp",
    speed: 1.25,
    session_context: [{ name: "log", content: "Mine." }],
  });
  const plain = reply();
  await route(
    request("/start", "POST", '{"agent_id":"keeper","request_id":"r2","voice":""}'),
    plain,
  );
  assert.deepEqual(calls.at(-1)?.body, {
    agent_id: "keeper",
    request_id: "r2",
    user: "keeper-1",
    session_context: [{ name: "log", content: "Keeps the lamp lit." }],
  });
});

test("a document given empty goes to voice as it is, and voice's 422 comes back", async (t) => {
  const problem = Response.json(
    { type: "/v1/problems/session-context-mismatch", title: "T", status: 422 },
    { status: 422, headers: { "Content-Type": "application/problem+json" } },
  );
  const v = voice(problem);
  const route = productRoutes({
    voice: "http://voice",
    key,
    user: await userFolder(t),
    fetcher: v.fetcher,
  });
  const res = reply();
  await route(
    request(
      "/start",
      "POST",
      '{"agent_id":"keeper","request_id":"r1","session_context":[{"name":"log","content":""}]}',
    ),
    res,
  );
  const sent = v.calls.at(-1)?.body as { session_context: unknown } | undefined;
  assert.deepEqual(sent?.session_context, [{ name: "log", content: "" }]);
  assert.equal(res.statusCode, 422);
  assert.equal(JSON.parse(res.body).type, "/v1/problems/session-context-mismatch");
});

test("overrides that voice wouldn't explain are refused before anything is sent", async (t) => {
  const { route, calls } = await richRoute(t);
  const cases: [string, string][] = [
    ['"speed":"fast"', "a speed that isn't a number"],
    ['"session_context":"log"', "documents that aren't a list"],
    ['"session_context":[{"name":"log"}]', "a document with no content"],
    ['"session_context":[{"name":1,"content":"x"}]', "a document named by a number"],
    ['"voice":5', "a voice that isn't a string"],
  ];
  for (const [field, what] of cases) {
    const res = reply();
    await route(request("/start", "POST", `{"agent_id":"keeper","request_id":"r1",${field}}`), res);
    assert.equal(res.statusCode, 400, what);
  }
  assert.equal(calls.length, 0, "voice heard of none");
});

test("a start over the documents' limit is answered 413 with the limit, and the connection closes after", async (t) => {
  const { route, calls } = await richRoute(t);
  const res = reply();
  let destroyed = 0;
  const body = JSON.stringify({
    agent_id: "keeper",
    request_id: "r1",
    session_context: [{ name: "log", content: "x".repeat(1_100_000) }],
  });
  await route({ ...request("/start", "POST", body), destroy: () => destroyed++ }, res);
  assert.equal(res.statusCode, 413);
  assert.match(res.body, /over 1048576 bytes/);
  assert.equal(res.headers.Connection, "close");
  assert.equal(destroyed, 0, "the connection is left for the answer");
  assert.equal(calls.length, 0);
});

test("a document with a character split across two chunks reaches voice whole", async (t) => {
  const { route, calls } = await richRoute(t);
  const content = "灯台守の日記。".repeat(30_000);
  const bytes = Buffer.from(
    JSON.stringify({
      agent_id: "keeper",
      request_id: "r1",
      session_context: [{ name: "log", content }],
    }),
  );
  // Cut inside a three-byte character, as the network may.
  const cut = bytes.indexOf(Buffer.from("灯")) + 1;
  const req = {
    ...request("/start", "POST"),
    on(event: string, cb: (chunk: Buffer | string) => void) {
      if (event === "data") {
        cb(bytes.subarray(0, cut));
        cb(bytes.subarray(cut));
      }
      if (event === "end") cb("");
    },
  };
  const res = reply();
  await route(req, res);
  assert.equal(res.statusCode, 201);
  const sent = calls.at(-1)?.body as { session_context: { content: string }[] } | undefined;
  assert.equal(sent?.session_context[0].content, content);
  assert.doesNotMatch(sent?.session_context[0].content ?? "", /\uFFFD/);
});

test("a start from another site is refused before anything is read or sent, and the page's own is not", async (t) => {
  const { route, calls } = await richRoute(t);
  const start = (headers: Record<string, string>) => ({
    ...request("/start", "POST", '{"agent_id":"plain","request_id":"r1"}'),
    headers: { "content-type": "application/json", host: "127.0.0.1:5173", ...headers },
  });
  const cases: [Record<string, string>, number, string][] = [
    [{ "sec-fetch-site": "cross-site" }, 403, "the browser says cross-site"],
    [{ "sec-fetch-site": "same-site" }, 403, "another port of this machine"],
    [{ origin: "https://evil.example" }, 403, "an origin that isn't the page's"],
    [{ origin: "http://127.0.0.1:9999" }, 403, "the page's host on another port"],
    [{ origin: "null" }, 403, "an opaque origin"],
    [{ origin: "http://127.0.0.1:5173", "sec-fetch-site": "same-origin" }, 201, "the page's own"],
    [{}, 201, "no origin, as a script sends"],
  ];
  for (const [headers, status, what] of cases) {
    const res = reply();
    await route(start(headers), res);
    assert.equal(res.statusCode, status, what);
  }
  assert.equal(
    calls.filter((c) => c.url.endsWith("/v1/sessions")).length,
    2,
    "only the two got through",
  );
});

test("the voices, the placeholder user's sessions and the documents a start would send are read with the key", async (t) => {
  const { route, calls } = await richRoute(t);
  const voices = reply();
  await route(request("/voices", "GET"), voices);
  assert.deepEqual(JSON.parse(voices.body), { voices: ["en_us", "ja_jp"] });
  const sessions = reply();
  await route(request("/sessions", "GET"), sessions);
  assert.equal(JSON.parse(sessions.body).sessions[0].id, "s1");
  assert.equal(calls.at(-1)?.url, "http://voice/v1/users/keeper-1/sessions?limit=20");
  const context = reply();
  await route(request("/context?agent=keeper", "GET"), context);
  assert.deepEqual(JSON.parse(context.body), {
    session_context: [{ name: "log", content: "Keeps the lamp lit." }],
  });
  for (const call of calls.filter((c) => !c.url.endsWith("/v1/agents"))) {
    assert.equal(call.auth, `Bearer ${key}`);
  }
  for (const res of [voices, sessions, context]) {
    assert.doesNotMatch(res.body, /the-product-key/);
  }
});

test("a voice that is down, or refuses, is told on each new route, and never with the key", async (t) => {
  const down = (async () => {
    throw new TypeError("fetch failed");
  }) as typeof fetch;
  const { route } = await richRoute(t, down);
  for (const url of ["/voices", "/sessions", "/context?agent=keeper"]) {
    const res = reply();
    await route(request(url, "GET"), res);
    assert.equal(res.statusCode, 502, url);
    assert.match(res.body, /fetch failed/, url);
    assert.doesNotMatch(res.body, /the-product-key/, url);
  }
  const refuses = (async () =>
    Response.json(
      { type: "about:blank", title: "Forbidden", status: 403 },
      { status: 403, headers: { "Content-Type": "application/problem+json" } },
    )) as typeof fetch;
  const refused = await richRoute(t, refuses);
  const res = reply();
  await refused.route(request("/sessions", "GET"), res);
  assert.equal(res.statusCode, 403, "voice's problem passes through");
  assert.equal(res.headers["Content-Type"], "application/problem+json");
});

test("behind an HTTPS proxy, the sessions panel reads and a start come through under the name set for it", async (t) => {
  const { route, calls } = await richRoute(t, undefined, ["mac.tail0.ts.net"]);
  const proxied = { "content-type": "application/json", host: "mac.tail0.ts.net" };
  const read = reply();
  await route({ ...request("/sessions", "GET"), headers: proxied }, read);
  assert.equal(read.statusCode, 200, read.body);
  const start = reply();
  const body = JSON.stringify({ agent_id: "keeper", request_id: "r1" });
  const fromPage = {
    ...proxied,
    origin: "https://mac.tail0.ts.net",
    "sec-fetch-site": "same-origin",
  };
  await route({ ...request("/start", "POST", body), headers: fromPage }, start);
  assert.equal(start.statusCode, 201, start.body);
  const paths = calls.map((c) => new URL(c.url).pathname);
  assert.ok(
    paths.some((p) => p.startsWith("/v1/users/")),
    `the read reached voice: ${paths}`,
  );
  assert.ok(paths.includes("/v1/sessions"), `the start reached voice: ${paths}`);
});

test("Vite gets the names without their ports, in an array of its own", () => {
  const hosts = Object.freeze(["mac.tail0.ts.net", "other.local:8443"]);
  const vite = viteAllowedHosts(hosts);
  // Vite compares a Host without its port, so a name with one would never match.
  assert.deepEqual(vite, ["mac.tail0.ts.net", "other.local"]);
  // Vite adds its own names to the array it gets; the routes' list must stay as set.
  vite.push("127.0.0.1");
  assert.deepEqual(hosts, ["mac.tail0.ts.net", "other.local:8443"]);
});

test("the read routes of the sessions panel refuse a Host that isn't this machine's", async (t) => {
  const { route, calls } = await richRoute(t);
  for (const url of ["/voices", "/sessions", "/context?agent=keeper"]) {
    for (const host of ["evil.example:5173", "127.0.0.1.evil.example:5173", undefined]) {
      const res = reply();
      const headers = host ? { host } : {};
      await route({ ...request(url, "GET"), headers }, res);
      assert.equal(res.statusCode, 403, `${url} for ${host}`);
    }
  }
  assert.equal(calls.length, 0, "voice heard of none");
});
