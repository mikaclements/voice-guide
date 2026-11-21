import assert from "node:assert/strict";
import { mkdir, mkdtemp, readdir, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { readJSON } from "./agent-files.ts";
import { productRoutes } from "./product-server.ts";

const key = "vgk_the-product-key";
const templateId = "01900000-0000-7000-8000-000000000001";
const agentFile = (id: string, name: string) =>
  `---\nid: ${id}\nname: ${name}\nopens: agent\n---\nYou keep the lamp lit.\n`;
const template = agentFile(templateId, "Lamp keeper");
const templateFile = "lamp-keeper.md";
const otherId = "01900000-0000-7000-8000-000000000009";
const newId = "01900000-0000-7000-8000-00000000000a";

// An agent's settings as the page's form sends them, under the agent file's names: these, and the
// rest unset.
const form = (over: Record<string, unknown> = {}) => ({
  id: newId,
  name: "Night watch",
  description: "",
  reads: [],
  session_context: [],
  summary: null,
  opens: "user",
  model: null,
  voice: null,
  speed: null,
  persona: "You keep watch.\n",
  ...over,
});

// Both agent folders in a fresh temp folder: the shipped one holds one agent file, and the local one
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
    const res = await send(route, "/publish", "POST", { path });
    assert.equal(res.statusCode, 400, JSON.stringify(path));
  }
  assert.equal(await readFile(join(f.dir, "secret.md"), "utf8"), "outside");
  assert.equal(v.calls.length, 0);
});

test("a link in an agent folder is never followed: not listed, written over or published", async (t) => {
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
  // A new agent whose file name the link already has.
  const asks: [string, string, unknown, number][] = [
    ["/publish", "POST", { path: "linked.md" }, 404],
    ["/agent", "POST", form({ name: "Linked" }), 409],
  ];
  for (const [url, method, body, status] of asks) {
    const res = await send(route, url, method, body);
    assert.equal(res.statusCode, status, `${method} ${url}`);
    assert.doesNotMatch(res.body, /Outside/, `${method} ${url}`);
  }
  assert.equal(await readFile(outside, "utf8"), agentFile(templateId, "Outside"));
  assert.equal(v.calls.filter((c) => c.method === "PUT").length, 0, "nothing published");
});

// Every route that writes a file or publishes one, with a body the page would send.
const writes: [string, string, unknown][] = [
  ["/agent", "POST", form({ id: templateId, name: "Intruder" })],
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

test("a request naming a host other than this machine is refused on every route, reads included", async (t) => {
  const f = await folders(t);
  await localFile(f, "coach.md", agentFile(otherId, "Coach"));
  const v = voice(() => Response.json({ version: 1 }, { status: 201 }));
  const route = routes(f, v.fetcher);
  // A site whose name was pointed at this machine: its page's requests are same-origin to it.
  const rebound = {
    ...fromPage,
    host: "rebound.example:5173",
    origin: "http://rebound.example:5173",
  };
  const asks: [string, string, unknown][] = [
    ["/agent-files", "GET", undefined],
    [`/agent?id=${otherId}`, "GET", undefined],
    ...writes,
  ];
  for (const headers of [rebound, { ...fromPage, host: undefined }]) {
    for (const [url, method, body] of asks) {
      const res = await send(route, url, method, body, headers);
      assert.equal(res.statusCode, 403, `${method} ${url} ${headers.host}`);
    }
  }
  assert.equal(v.calls.length, 0, "voice never heard of it");
  assert.deepEqual(await readdir(f.local), ["coach.md"]);
  const local = { ...fromPage, host: "localhost:5173", origin: "http://localhost:5173" };
  assert.equal((await send(route, "/agent-files", "GET", undefined, local)).statusCode, 200);
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

test("two agent files with one id are marked in the list, and neither is published over the other's agent", async (t) => {
  const f = await folders(t);
  // A copy of a shipped agent file that kept its id, and one that takes a shipped file's place.
  await localFile(f, "copy.md", agentFile(templateId, "Copy"));
  await writeFile(join(f.shipped, "guide.md"), agentFile(otherId, "Guide"));
  await localFile(f, "guide.md", agentFile(otherId, "My guide"));
  const v = voice(notFound);
  const route = routes(f, v.fetcher);
  const listed = JSON.parse((await send(route, "/agent-files", "GET")).body).files;
  assert.deepEqual(
    listed.map((x: { path: string; same_id_as: unknown }) => [x.path, x.same_id_as]),
    [
      ["copy.md", [{ path: templateFile, folder: "shipped" }]],
      ["guide.md", [{ path: "guide.md", folder: "shipped" }]],
      [templateFile, [{ path: "copy.md", folder: "local" }]],
    ],
  );
  for (const [path, other] of [
    ["copy.md", templateFile],
    [templateFile, "copy.md"],
    ["guide.md", "guide.md"],
  ]) {
    const res = await send(route, "/publish", "POST", { path });
    assert.equal(res.statusCode, 409, path);
    assert.match(res.body, new RegExp(`id of ${other}`), path);
  }
  assert.equal(v.calls.filter((c) => c.method === "PUT").length, 0, "nothing published");
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
      [`/agent?id=${templateId}`, "GET", undefined],
      ["/agent", "POST", form()],
      ...writes,
    ] as [string, string, unknown][]) {
      const res = await send(route, url, method, body);
      assert.doesNotMatch(res.body, /the-product-key/, `${method} ${url}`);
    }
  }
  assert.doesNotMatch(JSON.stringify(logged), /the-product-key/);
});

// An agent with every setting set, its values holding what YAML would read as its own syntax, and
// the agent file written from it: the text the agent file loader's own test reads back value for
// value.
const keeperId = "01a1de8a-0000-7000-8000-0000000000a1";
const keeperForm = form({
  id: keeperId,
  name: 'Keeper: "the" #1 灯台守 🌊',
  description: "Line one\nline two: with # and 'quotes', a tab\t, a b\u0085c\u007fd",
  reads: ["profile", "session_summary"],
  session_context: ["resume"],
  summary: {
    instruction: "Note the ships.\nName any: aground",
    model: "claude-opus-5-5",
    schema: {
      type: "object",
      properties: { score: { type: "integer", minimum: 1 } },
      required: ["score"],
    },
    documents: ["ship_log"],
  },
  opens: "agent",
  model: "claude-sonnet-5-5",
  voice: "bf_emma",
  speed: 1.1,
  persona: "You answer every call from the sea.\n---\nname: not a setting\n",
});
const keeperFile = [
  "---",
  `id: "${keeperId}"`,
  String.raw`name: "Keeper: \"the\" #1 灯台守 🌊"`,
  `${String.raw`description: "Line one\nline two: with # and 'quotes', a tab\t, a`} ${String.raw`b\u0085c\u007fd"`}`,
  `reads: ["profile","session_summary"]`,
  `session_context: ["resume"]`,
  String.raw`summary: {"instruction":"Note the ships.\nName any: aground","model":"claude-opus-5-5","schema":{"type":"object","properties":{"score":{"type":"integer","minimum":1}},"required":["score"]},"documents":["ship_log"]}`,
  `opens: "agent"`,
  `model: "claude-sonnet-5-5"`,
  `voice: "bf_emma"`,
  "speed: 1.1",
  "---",
  "You answer every call from the sea.",
  "---",
  "name: not a setting",
  "",
].join("\n");

test("the form is written as an agent file, each setting as JSON on one line and the persona after, then published with the key, voice's answer passed through", async (t) => {
  const f = await folders(t);
  const v = voice(() => Response.json({ version: 1 }, { status: 201 }));
  const res = await send(routes(f, v.fetcher), "/agent", "POST", keeperForm);
  assert.equal(res.statusCode, 201);
  assert.deepEqual(JSON.parse(res.body), { version: 1 });
  assert.equal(await readFile(join(f.local, "keeper-the-1.md"), "utf8"), keeperFile);
  assert.deepEqual(v.calls, [
    {
      url: `http://voice/v1/agents/${keeperId}`,
      method: "PUT",
      auth: `Bearer ${key}`,
      type: "text/markdown",
      body: keeperFile,
    },
  ]);
});

test("a setting left unset is left out of the file, so the loader's default applies", async (t) => {
  const f = await folders(t);
  const v = voice(() => Response.json({ version: 1 }, { status: 201 }));
  const summary = { instruction: "Note it.", model: null, schema: null, documents: [] };
  await send(routes(f, v.fetcher), "/agent", "POST", form({ summary }));
  assert.equal(
    await readFile(join(f.local, "night-watch.md"), "utf8"),
    `---\nid: "${newId}"\nname: "Night watch"\nsummary: {"instruction":"Note it."}\n---\nYou keep watch.\n`,
  );
});

test("the form goes to the file holding its id, else to a new file named from the agent's name, never to one the page names", async (t) => {
  const f = await folders(t);
  await localFile(f, "coach.md", agentFile(otherId, "Coach"));
  const v = voice(() => Response.json({ version: 2 }, { status: 201 }));
  const route = routes(f, v.fetcher);
  const renamed = form({ id: otherId, name: "Head coach" });
  const res = await send(route, "/agent", "POST", { ...renamed, path: "../escaped.md" });
  assert.equal(res.statusCode, 201);
  assert.match(
    await readFile(join(f.local, "coach.md"), "utf8"),
    /^---\nid: .*\nname: "Head coach"\n/,
  );
  assert.equal((await send(route, "/agent", "POST", form())).statusCode, 201);
  assert.deepEqual((await readdir(f.local)).sort(), ["coach.md", "night-watch.md"]);
  assert.deepEqual((await readdir(f.dir)).sort(), ["agents", "local"]);
});

test("a form the page may not write is refused, nothing written or sent: a shipped file's id, an id two files hold, a new agent whose file name is taken or whose name gives none", async (t) => {
  const f = await folders(t);
  await localFile(f, "copy.md", agentFile(otherId, "Copy"));
  await writeFile(join(f.shipped, "guide.md"), agentFile(otherId, "Guide"));
  await localFile(f, "coach.md", agentFile("01900000-0000-7000-8000-000000000002", "Coach"));
  const v = voice(() => Response.json({ version: 1 }, { status: 201 }));
  const route = routes(f, v.fetcher);
  const cases: [unknown, number, RegExp][] = [
    [form({ id: templateId }), 403, /lamp-keeper\.md is shipped/],
    [form({ id: otherId }), 409, /copy\.md \(local\), guide\.md \(shipped\)/],
    [form({ name: "Coach" }), 409, /coach\.md exists/],
    [form({ name: "Lamp keeper" }), 409, /lamp-keeper\.md exists/],
    [form({ name: "面接官" }), 422, /a letter a to z or a digit/],
  ];
  for (const [body, status, why] of cases) {
    const res = await send(route, "/agent", "POST", body);
    assert.equal(res.statusCode, status, JSON.stringify(body));
    assert.match(res.body, why);
  }
  assert.equal(v.calls.length, 0, "nothing published");
  assert.deepEqual((await readdir(f.local)).sort(), ["coach.md", "copy.md"]);
  assert.equal(await readFile(join(f.local, "copy.md"), "utf8"), agentFile(otherId, "Copy"));
  assert.equal(await readFile(join(f.shipped, templateFile), "utf8"), template);
});

test("a form that isn't an agent's settings under the file's names, or is over 256 KiB as a file, is refused before anything is written", async (t) => {
  const f = await folders(t);
  const v = voice(() => Response.json({ version: 1 }, { status: 201 }));
  const route = routes(f, v.fetcher);
  const cases: [unknown, number][] = [
    [form({ id: "../sessions" }), 400],
    [form({ reads: "profile" }), 400],
    [form({ speed: "1.1" }), 400],
    [form({ opens: "nobody" }), 400],
    [form({ summary: { instruction: "Note it." } }), 400],
    [form({ summary: { instruction: "Note it.", model: null, schema: [], documents: [] } }), 400],
    [form({ persona: undefined }), 400],
    [form({ persona: "x".repeat(256 * 1024) }), 413],
  ];
  for (const [body, status] of cases) {
    const res = await send(route, "/agent", "POST", body);
    assert.equal(res.statusCode, status, JSON.stringify(body).slice(0, 200));
  }
  assert.equal(v.calls.length, 0);
  assert.deepEqual(await readdir(f.local).catch(() => []), []);
});

test("with voice down the form's file is still written, and the page told 502 with the reason", async (t) => {
  const f = await folders(t);
  const res = await send(routes(f, down), "/agent", "POST", form());
  assert.equal(res.statusCode, 502);
  assert.match(res.body, /couldn't reach voice: fetch failed/);
  assert.match(await readFile(join(f.local, "night-watch.md"), "utf8"), /name: "Night watch"/);
});

test("an agent's newest version is read from voice with the key and passed through, and an id that isn't a UUID is refused unasked", async (t) => {
  const f = await folders(t);
  const problem = { type: "about:blank", title: "no such agent", status: 404 };
  const v = voice((c) =>
    c.url === `http://voice/v1/agents/${templateId}`
      ? Response.json({ id: templateId, version: 2 })
      : Response.json(problem, {
          status: 404,
          headers: { "Content-Type": "application/problem+json" },
        }),
  );
  const route = routes(f, v.fetcher);
  const found = await send(route, `/agent?id=${templateId}`, "GET");
  assert.deepEqual(
    [found.statusCode, JSON.parse(found.body)],
    [200, { id: templateId, version: 2 }],
  );
  const never = await send(route, `/agent?id=${otherId}`, "GET");
  assert.deepEqual(
    [never.statusCode, never.headers["Content-Type"]],
    [404, "application/problem+json"],
  );
  assert.equal((await send(route, "/agent?id=../sessions", "GET")).statusCode, 400);
  assert.deepEqual(
    v.calls.map((c) => [c.method, c.auth]),
    [
      ["GET", `Bearer ${key}`],
      ["GET", `Bearer ${key}`],
    ],
  );
  const gone = await send(routes(f, down), `/agent?id=${templateId}`, "GET");
  assert.equal(gone.statusCode, 502);
  assert.match(gone.body, /fetch failed/);
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
