import assert from "node:assert/strict";
import { mkdir, mkdtemp, readdir, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { readJSON, templateFile, uuidv7 } from "./agent-files.ts";
import { productRoutes } from "./product-server.ts";

const key = "vgk_the-product-key";
const templateId = "01900000-0000-7000-8000-000000000001";
const agentFile = (id: string, name: string) =>
  `---\nid: ${id}\nname: ${name}\nopens: agent\n---\nYou keep the lamp lit.\n`;
const template = agentFile(templateId, "Lamp keeper");
const uuidV7 = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

// Both agent folders in a fresh temp folder: the shipped one holds the template, and the local one
// is missing, as on a fresh clone.
async function folders(t: { after(fn: () => unknown): void }) {
  const dir = await mkdtemp(join(tmpdir(), "agent-files-"));
  t.after(() => rm(dir, { recursive: true }));
  const shipped = join(dir, "agents");
  await mkdir(shipped);
  await writeFile(join(shipped, templateFile), template);
  return { dir, local: join(dir, "local"), shipped };
}

// A local agent file, its folder made first.
async function localFile(f: { local: string }, path: string, text: string) {
  await mkdir(f.local, { recursive: true });
  await writeFile(join(f.local, path), text);
}

type Call = { url: string; method: string; auth: string | null; type: string | null; body: string };

// A voice that answers each call as answer says, recording what the product's server sent it.
function voice(answer: (c: Call) => Response | Promise<Response>) {
  const calls: Call[] = [];
  const fetcher = (async (url: string, init?: RequestInit) => {
    const headers = new Headers(init?.headers);
    const body = init?.body ? Buffer.from(init.body as Uint8Array).toString() : "";
    const call = {
      url,
      method: init?.method ?? "GET",
      auth: headers.get("Authorization"),
      type: headers.get("Content-Type"),
      body,
    };
    calls.push(call);
    return answer(call);
  }) as typeof fetch;
  return { fetcher, calls };
}
const notFound = () => new Response(null, { status: 404 });
const down = (async () => {
  throw new TypeError("fetch failed");
}) as typeof fetch;

// What a fetch from the page itself carries.
const fromPage = {
  host: "127.0.0.1:5173",
  origin: "http://127.0.0.1:5173",
  "sec-fetch-site": "same-origin",
  "content-type": "application/json",
};
type Headers_ = Record<string, string | undefined>;

function routes(f: { dir: string; local: string; shipped: string }, fetcher: typeof fetch) {
  return productRoutes({ voice: "http://voice", key, user: f.dir, agents: f, fetcher });
}

async function send(
  route: ReturnType<typeof productRoutes>,
  url: string,
  method: string,
  body?: unknown,
  headers: Headers_ = fromPage,
) {
  const res = {
    statusCode: 0,
    headers: {} as Record<string, string>,
    body: "",
    setHeader(name: string, value: string) {
      res.headers[name] = value;
    },
    end(text: string) {
      res.body = text;
    },
  };
  await route(
    {
      url,
      method,
      headers,
      on(event: string, cb: (chunk: string) => void) {
        if (event === "data" && body !== undefined) cb(JSON.stringify(body));
        if (event === "end") cb("");
      },
    },
    res,
  );
  return res;
}

test("the list shows each agent file once, a local file in place of a shipped one of its name, with its published version", async (t) => {
  const f = await folders(t);
  await localFile(f, "coach.md", agentFile("01900000-0000-7000-8000-000000000002", "Coach"));
  await localFile(f, "notes.txt", "not an agent file");
  await writeFile(join(f.shipped, "guide.md"), agentFile(templateId, "Guide"));
  await localFile(f, "guide.md", agentFile("01900000-0000-7000-8000-000000000003", "My guide"));
  const v = voice((c) =>
    c.url === `http://voice/v1/agents/${templateId}`
      ? Response.json({ version: 3, published_at: "2026-01-02T03:04:05Z" })
      : notFound(),
  );
  const res = await send(routes(f, v.fetcher), "/agent-files", "GET");
  assert.equal(res.statusCode, 200);
  const { files, problem } = JSON.parse(res.body);
  assert.equal(problem, undefined);
  assert.deepEqual(
    files.map((x: Record<string, unknown>) => [x.path, x.folder, x.name, x.version]),
    [
      ["coach.md", "local", "Coach", null],
      ["guide.md", "local", "My guide", null],
      [templateFile, "shipped", "Lamp keeper", 3],
    ],
  );
  assert.equal(files[2].published_at, "2026-01-02T03:04:05Z");
  assert.equal(typeof files[0].modified, "number");
  assert.ok(v.calls.every((c) => c.auth === `Bearer ${key}`));
});

test("with no agent files the list is empty", async (t) => {
  const f = await folders(t);
  await rm(join(f.shipped, templateFile));
  const res = await send(routes(f, voice(notFound).fetcher), "/agent-files", "GET");
  assert.deepEqual(JSON.parse(res.body), { files: [] });
});

test("with voice down the list still shows the files, each version unknown, and says why", async (t) => {
  const f = await folders(t);
  const res = await send(routes(f, down), "/agent-files", "GET");
  assert.equal(res.statusCode, 200);
  const { files, problem } = JSON.parse(res.body);
  assert.deepEqual(
    files.map((x: Record<string, unknown>) => [x.path, x.version]),
    [[templateFile, "unknown"]],
  );
  assert.match(problem, /fetch failed/);
});

test("a file is opened by its bare name, with its text and when it was modified", async (t) => {
  const f = await folders(t);
  const route = routes(f, voice(notFound).fetcher);
  const res = await send(route, `/agent-file?path=${templateFile}`, "GET");
  assert.equal(res.statusCode, 200);
  const opened = JSON.parse(res.body);
  assert.deepEqual(
    [opened.path, opened.folder, opened.content],
    [templateFile, "shipped", template],
  );
  assert.equal(typeof opened.modified, "number");
  assert.equal((await send(route, "/agent-file?path=gone.md", "GET")).statusCode, 404);
});

test("a path that isn't a bare agent file name is refused before the disk is touched", async (t) => {
  const f = await folders(t);
  // Where ../secret.md from the local folder leads.
  await writeFile(join(f.dir, "secret.md"), "outside");
  await localFile(f, "coach.md", agentFile(templateId, "Coach"));
  const v = voice(notFound);
  const route = routes(f, v.fetcher);
  const paths = [
    "../secret.md",
    join(f.dir, "secret.md"),
    "agents/lamp.md",
    "secret.txt",
    "Secret.md",
    ".secret.md",
    "-secret.md",
    "",
  ];
  for (const path of paths) {
    const asks: [string, string, unknown][] = [
      [`/agent-file?path=${encodeURIComponent(path)}`, "GET", undefined],
      ["/agent-file", "PUT", { path, content: "written", modified: 0 }],
      ["/publish", "POST", { path }],
    ];
    for (const [url, method, body] of asks) {
      const res = await send(route, url, method, body);
      assert.equal(res.statusCode, 400, `${method} ${JSON.stringify(path)}`);
    }
  }
  assert.equal(await readFile(join(f.dir, "secret.md"), "utf8"), "outside");
  assert.equal(v.calls.length, 0);
});

test("a link in an agent folder is never followed: not listed, read, saved over or published", async (t) => {
  const f = await folders(t);
  const outside = join(f.dir, "outside.md");
  await writeFile(outside, agentFile(templateId, "Outside"));
  await mkdir(f.local);
  await symlink(outside, join(f.local, "linked.md"));
  const v = voice(() => Response.json({ version: 1 }, { status: 201 }));
  const route = routes(f, v.fetcher);
  const listed = JSON.parse((await send(route, "/agent-files", "GET")).body).files;
  assert.deepEqual(
    listed.map((x: { path: string }) => x.path),
    [templateFile],
  );
  const asks: [string, string, unknown, number][] = [
    ["/agent-file?path=linked.md", "GET", undefined, 404],
    ["/agent-file", "PUT", { path: "linked.md", content: "written", modified: 0 }, 404],
    ["/publish", "POST", { path: "linked.md" }, 404],
    ["/agent-files", "POST", { name: "Linked" }, 409],
  ];
  for (const [url, method, body, status] of asks) {
    const res = await send(route, url, method, body);
    assert.equal(res.statusCode, status, `${method} ${url}`);
    assert.doesNotMatch(res.body, /Outside/, `${method} ${url}`);
  }
  assert.equal(await readFile(outside, "utf8"), agentFile(templateId, "Outside"));
  assert.equal(v.calls.filter((c) => c.method === "PUT").length, 0, "nothing published");
});

test("a save replaces the local file whole and answers when it was modified", async (t) => {
  const f = await folders(t);
  await localFile(f, "coach.md", agentFile(templateId, "Coach"));
  const route = routes(f, voice(notFound).fetcher);
  const { modified } = JSON.parse((await send(route, "/agent-file?path=coach.md", "GET")).body);
  const res = await send(route, "/agent-file", "PUT", {
    path: "coach.md",
    content: "saved",
    modified,
  });
  assert.equal(res.statusCode, 200);
  assert.equal(await readFile(join(f.local, "coach.md"), "utf8"), "saved");
  const reopened = JSON.parse((await send(route, "/agent-file?path=coach.md", "GET")).body);
  assert.equal(JSON.parse(res.body).modified, reopened.modified);
  assert.deepEqual(await readdir(f.local), ["coach.md"], "no temp file left behind");
});

test("a save of a file changed on disk since it was opened is refused, nothing written", async (t) => {
  const f = await folders(t);
  await localFile(f, "coach.md", "on disk");
  const route = routes(f, voice(notFound).fetcher);
  const { modified } = JSON.parse((await send(route, "/agent-file?path=coach.md", "GET")).body);
  const res = await send(route, "/agent-file", "PUT", {
    path: "coach.md",
    content: "from the page",
    modified: modified - 1000,
  });
  assert.equal(res.statusCode, 409);
  assert.match(res.body, /changed on disk/);
  assert.equal(await readFile(join(f.local, "coach.md"), "utf8"), "on disk");
});

test("a shipped file is never saved", async (t) => {
  const f = await folders(t);
  const route = routes(f, voice(notFound).fetcher);
  const { modified } = JSON.parse(
    (await send(route, `/agent-file?path=${templateFile}`, "GET")).body,
  );
  const res = await send(route, "/agent-file", "PUT", {
    path: templateFile,
    content: "x",
    modified,
  });
  assert.equal(res.statusCode, 403);
  assert.equal(await readFile(join(f.shipped, templateFile), "utf8"), template);
});

// Every route that writes a file or publishes one, with a body the page would send.
const writes: [string, string, unknown][] = [
  ["/agent-file", "PUT", { path: "coach.md", content: "written", modified: 0 }],
  ["/agent-files", "POST", { name: "Intruder" }],
  ["/publish", "POST", { path: "coach.md" }],
];

test("a write or publish sent from another site, from another port of this machine, or not as JSON is refused", async (t) => {
  const f = await folders(t);
  await localFile(f, "coach.md", agentFile(templateId, "Coach"));
  const v = voice(() => Response.json({ version: 1 }, { status: 201 }));
  const route = routes(f, v.fetcher);
  const refused: [Headers_, number][] = [
    [{ "sec-fetch-site": "cross-site", origin: "https://elsewhere.example" }, 403],
    [{ "sec-fetch-site": "same-site", origin: "http://127.0.0.1:8091" }, 403],
    // A browser that sends no Sec-Fetch-Site still names the page's origin.
    [{ "sec-fetch-site": undefined, origin: "https://elsewhere.example" }, 403],
    // A form or a text body, which a page on another site can send with no preflight.
    [{ "content-type": undefined }, 415],
    [{ "content-type": "text/plain" }, 415],
    [{ "content-type": "application/x-www-form-urlencoded" }, 415],
  ];
  for (const [from, status] of refused) {
    for (const [url, method, body] of writes) {
      const res = await send(route, url, method, body, { ...fromPage, ...from });
      assert.equal(res.statusCode, status, `${method} ${url} ${JSON.stringify(from)}`);
    }
  }
  assert.equal(v.calls.length, 0, "voice never heard of it");
  assert.deepEqual(await readdir(f.local), ["coach.md"]);
  assert.equal(await readFile(join(f.local, "coach.md"), "utf8"), agentFile(templateId, "Coach"));
});

test("a new agent is the template with a fresh UUIDv7 id and the name given, in a file named for it", async (t) => {
  const f = await folders(t);
  const name = "Night Watch: Ünïcode $1";
  const res = await send(routes(f, voice(notFound).fetcher), "/agent-files", "POST", { name });
  assert.equal(res.statusCode, 201);
  const made = JSON.parse(res.body);
  assert.equal(made.path, "night-watch-unicode-1.md");
  assert.match(made.id, uuidV7);
  const text = await readFile(join(f.local, made.path), "utf8");
  assert.equal(
    text,
    template
      .replace(templateId, made.id)
      .replace("name: Lamp keeper", `name: ${JSON.stringify(name)}`),
  );
});

test("a new agent is refused when its file exists in either folder, or its name gives no file name", async (t) => {
  const f = await folders(t);
  await writeFile(join(f.shipped, "guide.md"), agentFile(templateId, "Guide"));
  const route = routes(f, voice(notFound).fetcher);
  assert.equal((await send(route, "/agent-files", "POST", { name: "Coach" })).statusCode, 201);
  const cases: [unknown, number][] = [
    ["Coach", 409],
    ["Guide", 409],
    ["", 422],
    ["   ", 422],
    ["!!!", 422],
    ["面接官", 422],
    ["two\nlines", 422],
    [7, 422],
  ];
  for (const [name, status] of cases) {
    const res = await send(route, "/agent-files", "POST", { name });
    assert.equal(res.statusCode, status, JSON.stringify(name));
  }
  assert.deepEqual(await readdir(f.local), ["coach.md"]);
});

test("publishing sends the file on disk to voice as text/markdown with the key, and voice's answer passes through", async (t) => {
  const f = await folders(t);
  let newest = "";
  let version = 0;
  const v = voice((c) => {
    const changed = c.body !== newest;
    if (changed) [newest, version] = [c.body, version + 1];
    return Response.json({ version }, { status: changed ? 201 : 200 });
  });
  const route = routes(f, v.fetcher);
  const first = await send(route, "/publish", "POST", { path: templateFile });
  const again = await send(route, "/publish", "POST", { path: templateFile });
  assert.deepEqual(
    [first.statusCode, JSON.parse(first.body), again.statusCode, JSON.parse(again.body)],
    [201, { version: 1 }, 200, { version: 1 }],
  );
  assert.deepEqual(v.calls[0], {
    url: `http://voice/v1/agents/${templateId}`,
    method: "PUT",
    auth: `Bearer ${key}`,
    type: "text/markdown",
    body: template,
  });
});

test("voice's problem with a publish passes through as it is", async (t) => {
  const f = await folders(t);
  const problem = { type: "about:blank", title: "Invalid agent file", status: 422, detail: "d" };
  const v = voice(() =>
    Response.json(problem, {
      status: 422,
      headers: { "Content-Type": "application/problem+json" },
    }),
  );
  const res = await send(routes(f, v.fetcher), "/publish", "POST", { path: templateFile });
  assert.equal(res.statusCode, 422);
  assert.equal(res.headers["Content-Type"], "application/problem+json");
  assert.deepEqual(JSON.parse(res.body), problem);
});

test("a file voice would refuse unread is refused before it is sent: over 256 KiB, or an id that isn't a UUID", async (t) => {
  const f = await folders(t);
  await localFile(f, "big.md", agentFile(templateId, "Big") + "x".repeat(256 * 1024));
  await localFile(f, "lost.md", agentFile("../sessions", "Lost"));
  const v = voice(() => Response.json({ version: 1 }, { status: 201 }));
  const route = routes(f, v.fetcher);
  assert.equal((await send(route, "/publish", "POST", { path: "big.md" })).statusCode, 413);
  assert.equal((await send(route, "/publish", "POST", { path: "lost.md" })).statusCode, 422);
  assert.equal(v.calls.length, 0);
});

test("publishing with voice down or stuck answers 502 with the reason", async (t) => {
  const f = await folders(t);
  const hangs = ((_url: string, init?: RequestInit) =>
    new Promise((_resolve, reject) => {
      init?.signal?.addEventListener("abort", () => reject(init.signal?.reason));
    })) as typeof fetch;
  const stuck = productRoutes({
    voice: "http://voice",
    key,
    user: f.dir,
    agents: f,
    fetcher: hangs,
    timeoutMs: 20,
  });
  for (const [route, why] of [
    [routes(f, down), /fetch failed/],
    [stuck, /timeout/i],
  ] as const) {
    const res = await send(route, "/publish", "POST", { path: templateFile });
    assert.equal(res.statusCode, 502);
    assert.match(res.body, why);
  }
});

test("the product key never reaches a reply or a log line", async (t) => {
  const logged: unknown[] = [];
  for (const level of ["log", "info", "warn", "error"] as const) {
    t.mock.method(console, level, (...args: unknown[]) => logged.push(...args));
  }
  const f = await folders(t);
  await localFile(f, "coach.md", agentFile(templateId, "Coach"));
  const refusing = voice(() => Response.json({ title: "Forbidden", status: 403 }, { status: 403 }));
  for (const fetcher of [voice(notFound).fetcher, refusing.fetcher, down]) {
    const route = routes(f, fetcher);
    for (const [url, method, body] of [
      ["/agent-files", "GET", undefined],
      ["/agent-file?path=coach.md", "GET", undefined],
      ...writes,
    ] as [string, string, unknown][]) {
      const res = await send(route, url, method, body);
      assert.doesNotMatch(res.body, /the-product-key/, `${method} ${url}`);
    }
  }
  assert.doesNotMatch(JSON.stringify(logged), /the-product-key/);
});

test("a UUIDv7 holds its time in milliseconds, then version 7 and the RFC 9562 variant around random bits", () => {
  assert.match(
    uuidv7(0x0192_3456_789a),
    /^01923456-789a-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/,
  );
  assert.notEqual(uuidv7(0), uuidv7(0));
});

// A request whose body arrives in these chunks.
const sent = (chunks: (Buffer | string)[], dropped = () => {}) => ({
  on(event: string, cb: (chunk: Buffer | string) => void) {
    if (event === "data") for (const c of chunks) cb(c);
    if (event === "end") cb("");
  },
  destroy: dropped,
});

test("a JSON body is decoded once whole, so a character split between chunks stays whole", async () => {
  const bytes = Buffer.from(JSON.stringify({ name: "灯台守" }));
  assert.deepEqual(await readJSON(sent([bytes.subarray(0, 10), bytes.subarray(10)]), 100), {
    name: "灯台守",
  });
  assert.deepEqual(await readJSON(sent(["not JSON"]), 100), {}, "not JSON reads as no fields");
});

test("a body over the limit is dropped, not read to its end", async () => {
  let dropped = 0;
  const read = readJSON(
    sent(["x".repeat(60), "x".repeat(60), "x"], () => dropped++),
    100,
  );
  await assert.rejects(read, /over the limit/);
  assert.equal(dropped, 1, "the connection is dropped once, at the limit");
});
