import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { contextBlock, defaultTimeoutMs, productRoutes, readBody } from "./product-server.ts";
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
    headers: { "content-type": "application/json" },
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
      headers: type ? { "content-type": type } : {},
    };
    await route(req, res);
    assert.equal(res.statusCode, 415, String(type));
  }
  assert.equal(v.calls.length, 0, "voice never heard of it");
});

test("a body over the limit is dropped, not read to its end", async () => {
  let destroyed = 0;
  let listener: ((chunk: string) => void) | undefined;
  const req = {
    on(event: string, cb: (chunk: string) => void) {
      if (event === "data") listener = cb;
    },
    destroy() {
      destroyed++;
    },
  };
  const read = readBody(req);
  listener?.("x".repeat(3000));
  listener?.("x".repeat(3000));
  listener?.("x".repeat(3000));
  await assert.rejects(read, /over the limit/);
  assert.equal(destroyed, 1, "the connection is dropped once, at the limit");
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
