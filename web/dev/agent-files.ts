import { randomBytes } from "node:crypto";
import { lstat, mkdir, readdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import type { Incoming, Reply } from "./product-server.ts";

// The two folders of agent files the page lists: local, the developer's own test agents, which the
// page writes, and shipped, the repository's agents, which it only reads and publishes, so the page
// never edits a tracked file.
export type AgentFolders = { local: string; shipped: string };

// Only a bare file name, so a path from the page can't name anything outside the two folders.
const fileName = /^[a-z0-9][a-z0-9-]*\.md$/;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
// A UUID names one agent in either case, and voice answers it lower-cased.
const sameUUID = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();
// Voice's cap on an agent file: a larger one is refused here rather than sent to be refused.
const fileLimit = 256 * 1024;
// A form carries the persona as a JSON string; four times the cap leaves room for its escapes.
const formBodyLimit = 4 * fileLimit;
// A path, with room to spare.
const smallBodyLimit = 4096;
// Long enough for a readable file name, far under any file system's limit; a round number.
const slugLength = 64;

type Voice = (path: string, init?: RequestInit) => Promise<Response>;
type Found = { path: string; folder: "local" | "shipped"; file: string; modified: number };
type Scanned = Found & { id: string; name: string };

// An agent's settings under its agent file's names and in its shapes, as voice answers an agent's
// version and the page's form sends one back.
type Settings = {
  id: string;
  name: string;
  description: string;
  reads: string[];
  session_context: string[];
  summary: {
    instruction: string;
    model: string | null;
    schema: Record<string, unknown> | null;
    documents: string[];
  } | null;
  opens: "agent" | "user";
  model: string | null;
  voice: string | null;
  speed: number | null;
  persona: string;
};

// The routes behind the page's Agents tab, mounted at /product beside the product's own: the files
// in both folders with their published versions, an agent's newest version as voice holds it, the
// form written to its file and published, and a file published as it is on disk. call reaches voice
// with the product key, which these routes never see. Answers true when the request was one of these
// routes.
export function agentFileRoutes(folders: AgentFolders, call: Voice) {
  return async (req: Incoming, to: Reply): Promise<boolean> => {
    const url = new URL(req.url ?? "/", "http://product");
    const route = `${req.method} ${url.pathname}`;
    const writes = ["POST /agent", "POST /publish"];
    if (!["GET /agent-files", "GET /agent", ...writes].includes(route)) return false;
    try {
      if (fromAnotherHost(req)) return fail(to, 403, hostRefused);
      if (writes.includes(route)) {
        const refusal = fromElsewhere(req);
        if (refusal) return fail(to, ...refusal);
      }
      if (route === "GET /agent-files") return json(to, 200, await list(folders, call));
      if (route === "GET /agent") {
        // The id goes in the path to voice, so nothing but a UUID may reach it.
        const id = url.searchParams.get("id") ?? "";
        if (!uuid.test(id)) return fail(to, 400, "the id must be a UUID");
        return await answer(to, () => call(`/v1/agents/${id}`));
      }
      const limit = route === "POST /agent" ? formBodyLimit : smallBodyLimit;
      const asked = await readJSON(req, limit).catch(() => undefined);
      if (!asked) return fail(to, 413, "the request is over the limit");
      if (route === "POST /agent") return await write(folders, asked, call, to);
      const path = typeof asked.path === "string" ? asked.path : "";
      if (!fileName.test(path)) return badPath(to);
      const found = await find(folders, path);
      if (!found) return fail(to, 404, `no agent file named ${path}`);
      return await publish(folders, found, call, to);
    } catch (err) {
      return fail(to, 500, `the agent files couldn't be read or written: ${message(err)}`);
    }
  };
}

// This machine's own names for the dev server, at its port. A site whose name an attacker points at
// this machine makes its page's requests same-origin to it, but they carry its name as the Host,
// so every route, reading or acting, refuses any other name itself, whatever Vite checks first.
const ownHost = /^(127\.0\.0\.1|localhost):\d+$/;
export const hostRefused = "only this machine's own names reach the dev server";

export function fromAnotherHost(req: Incoming): boolean {
  return !ownHost.test(String(req.headers?.host ?? ""));
}

// Whether a request that acts, rather than reads, must be refused, and how: every such route of the
// dev server takes this one rule. A browser sends a JSON body to another origin only after a
// preflight, which this server never answers, and names where a request came from; the Host is one
// of this machine's own names, so the page's own origin is the Host it asked for. A page on
// another site, or on another port of this machine, then can't act with the product key through the
// developer's browser.
export function fromElsewhere(req: Incoming): [number, string] | undefined {
  if (fromAnotherHost(req)) return [403, hostRefused];
  const site = req.headers?.["sec-fetch-site"];
  const origin = req.headers?.origin;
  if (
    (site !== undefined && site !== "same-origin" && site !== "none") ||
    (origin !== undefined && origin !== `http://${req.headers?.host}`)
  ) {
    return [403, "only the page itself may send this"];
  }
  if (!String(req.headers?.["content-type"] ?? "").startsWith("application/json")) {
    return [415, "this takes application/json"];
  }
}

async function list(folders: AgentFolders, call: Voice) {
  const all = await scan(folders);
  // One file per name, a local one in place of a shipped one.
  const listed = all
    .filter(
      (f) => f.folder === "local" || !all.some((o) => o.folder === "local" && o.path === f.path),
    )
    .sort((a, b) => (a.path < b.path ? -1 : 1));
  let problem: string | undefined;
  const files = await Promise.all(
    listed.map(async (f) => {
      const file = { ...shown(f), id: f.id, name: f.name, same_id_as: sameId(all, f) };
      try {
        return { ...file, ...(await published(call, f.id)) };
      } catch (err) {
        problem ??= message(err);
        return { ...file, version: "unknown", published_at: null };
      }
    }),
  );
  return { files, problem };
}

// Every agent file in both folders, a shipped one that a local one of its name takes the place of
// included, with its id and name. Only regular files, as find takes.
async function scan(folders: AgentFolders): Promise<Scanned[]> {
  const all: Scanned[] = [];
  for (const folder of ["local", "shipped"] as const) {
    for (const path of await readdir(folders[folder]).catch(() => [])) {
      if (!fileName.test(path)) continue;
      const file = join(folders[folder], path);
      const found = await lstat(file).catch(() => undefined);
      if (!found?.isFile()) continue;
      const text = await readFile(file, "utf8");
      const [id, name] = [setting(text, "id"), setting(text, "name")];
      all.push({ path, folder, file, modified: found.mtimeMs, id, name });
    }
  }
  return all;
}

// The other files with f's id: publishing either would replace the other's agent, as when a
// shipped agent file is copied with its id kept.
function sameId(all: Scanned[], f: { file: string; id: string }) {
  return all
    .filter((o) => o.id !== "" && sameUUID(o.id, f.id) && o.file !== f.file)
    .map((o) => ({ path: o.path, folder: o.folder }));
}

// The newest published version of the agent with this id, or nulls when there is none: voice
// answers 404 for an agent never published and for an id that isn't a UUID alike.
async function published(call: Voice, id: string) {
  const none = { version: null, published_at: null };
  if (!uuid.test(id)) return none;
  let res: Response;
  try {
    res = await call(`/v1/agents/${id}`);
  } catch (err) {
    throw new Error(`the product's server couldn't reach voice: ${message(err)}`);
  }
  if (res.status === 404) return none;
  if (!res.ok) throw new Error(`voice refused reading the published versions (${res.status})`);
  const agent: { version: number; published_at: string } = await res.json();
  return { version: agent.version, published_at: agent.published_at };
}

async function publish(folders: AgentFolders, found: Found, call: Voice, to: Reply) {
  const file = await readFile(found.file);
  if (file.length > fileLimit) return fail(to, 413, "the file is over 256 KiB, voice's limit");
  // The id goes in the path to voice, so nothing but a UUID may reach it.
  const id = setting(file.toString("utf8"), "id");
  if (!uuid.test(id)) return fail(to, 422, `${found.path} needs an id that is a UUID`);
  const others = sameId(await scan(folders), { file: found.file, id });
  if (others.length) {
    const named = others.map((o) => `${o.path} (${o.folder})`).join(", ");
    return fail(to, 409, `${found.path} has the id of ${named}; give one of them a new id`);
  }
  return await answer(to, () => put(call, id, file));
}

const put = (call: Voice, id: string, file: Uint8Array) =>
  call(`/v1/agents/${id}`, {
    method: "PUT",
    headers: { "Content-Type": "text/markdown" },
    body: new Uint8Array(file),
  });

// Writes the form's settings to the agent file holding its id, or to a new one named from the
// agent's name, then publishes that file, so the file on disk stays the record of every agent the
// page made or changed. The page never names the file. A shipped file is never written, and an id
// two files hold is refused, as a publish of either would replace the other's agent. The form
// carries when the file was modified as the page's list read it: a file changed since, in an
// editor say, is refused, so those edits aren't overwritten unseen.
async function write(
  folders: AgentFolders,
  asked: Record<string, unknown>,
  call: Voice,
  to: Reply,
) {
  const s = settingsOf(asked);
  if (!s) return fail(to, 400, "the agent must be its settings under the agent file's names");
  const text = agentFileText(s);
  if (Buffer.byteLength(text) > fileLimit) {
    return fail(to, 413, "the agent file would be over 256 KiB, voice's limit");
  }
  const holders = (await scan(folders)).filter((f) => sameUUID(f.id, s.id));
  if (holders.length > 1) {
    const named = holders.map((o) => `${o.path} (${o.folder})`).join(", ");
    return fail(to, 409, `${s.id} is the id of ${named}; give all but one of them a new id`);
  }
  const held = holders[0];
  if (held?.folder === "shipped") {
    return fail(to, 403, `${held.path} is shipped, so the page only reads it; Clone it`);
  }
  if (held && held.modified !== asked.modified) {
    return fail(to, 409, `${held.path} changed on disk since the list loaded; reload`);
  }
  if (held) {
    // Renamed into place, so a crash mid-write leaves the old file whole; the dot keeps it out of
    // the list.
    const temp = join(dirname(held.file), `.${held.path}.${randomBytes(4).toString("hex")}`);
    try {
      await writeFile(temp, text);
      await rename(temp, held.file);
    } finally {
      await rm(temp, { force: true });
    }
  } else {
    const path = `${slug(s.name)}.md`;
    if (!fileName.test(path)) {
      return fail(to, 422, "the name needs a letter a to z or a digit, to name its file");
    }
    if (await find(folders, path)) {
      return fail(to, 409, `an agent file named ${path} exists; give the agent another name`);
    }
    await mkdir(folders.local, { recursive: true });
    try {
      // wx: whatever has the name, a file made since the check above or a link, is kept.
      await writeFile(join(folders.local, path), text, { flag: "wx" });
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code === "EEXIST") {
        return fail(to, 409, `${path} exists in the local folder; give the agent another name`);
      }
      throw err;
    }
  }
  return await answer(to, () => put(call, s.id, Buffer.from(text)));
}

// Voice's answer as it is, problems included, so the page reads them as voice's own; a voice that
// can't be reached is the product's server's 502, with the reason.
async function answer(to: Reply, ask: () => Promise<Response>) {
  let res: Response;
  try {
    res = await ask();
  } catch (err) {
    return fail(to, 502, `the product's server couldn't reach voice: ${message(err)}`);
  }
  to.statusCode = res.status;
  to.setHeader("Content-Type", res.headers.get("Content-Type") ?? "application/json");
  to.end(await res.text());
  return true;
}

const isText = (v: unknown): v is string => typeof v === "string";
const isList = (v: unknown): v is string[] => Array.isArray(v) && v.every(isText);
const isObject = (v: unknown): v is Record<string, unknown> =>
  v !== null && typeof v === "object" && !Array.isArray(v);
const textOrNull = (v: unknown) => v === null || isText(v);

// The settings in a form the page sent, every one present and in its shape, or undefined. Only what
// the file can't hold is checked here; voice checks the rest when it reads the file, and its problem
// is shown as it is.
function settingsOf(a: Record<string, unknown>): Settings | undefined {
  const s = a.summary;
  const summary =
    s === null ||
    (isObject(s) &&
      isText(s.instruction) &&
      textOrNull(s.model) &&
      (s.schema === null || isObject(s.schema)) &&
      isList(s.documents));
  const speed = a.speed === null || (typeof a.speed === "number" && Number.isFinite(a.speed));
  const ok =
    isText(a.id) &&
    uuid.test(a.id) &&
    isText(a.name) &&
    isText(a.description) &&
    isList(a.reads) &&
    isList(a.session_context) &&
    summary &&
    (a.opens === "agent" || a.opens === "user") &&
    textOrNull(a.model) &&
    textOrNull(a.voice) &&
    speed &&
    isText(a.persona);
  return ok ? (a as Settings) : undefined;
}

// The agent file for the settings: each setting on one line as JSON, which YAML reads as a flow
// value, so every value comes back exactly as the page sent it with no YAML writer, and no line of
// the settings block can be the --- that closes it, whatever lines the persona holds. A setting left
// unset is left out, so the loader's default applies.
function agentFileText(s: Settings): string {
  const summary = s.summary && {
    instruction: s.summary.instruction,
    ...(s.summary.model ? { model: s.summary.model } : {}),
    ...(s.summary.schema ? { schema: s.summary.schema } : {}),
    ...(s.summary.documents.length ? { documents: s.summary.documents } : {}),
  };
  const settings: [string, unknown][] = [
    ["id", s.id],
    ["name", s.name],
    ["description", s.description || undefined],
    ["reads", s.reads.length ? s.reads : undefined],
    ["session_context", s.session_context.length ? s.session_context : undefined],
    ["summary", summary ?? undefined],
    ["opens", s.opens === "agent" ? "agent" : undefined],
    ["model", s.model || undefined],
    ["voice", s.voice || undefined],
    ["speed", s.speed ?? undefined],
  ];
  const head = settings
    .filter(([, value]) => value !== undefined)
    .map(([key, value]) => `${key}: ${asYAML(value)}\n`)
    .join("");
  return `---\n${head}---\n${s.persona.toWellFormed()}`;
}

// JSON leaves DEL, the C1 controls, U+FFFE and U+FFFF raw, which YAML refuses, or for U+0085 reads
// as a line break, so they are written as JSON's escapes, which YAML reads alike. JSON escapes a
// lone surrogate, which YAML refuses and no text holds, so it becomes U+FFFD first, as a UTF-8 file
// would hold it.
const asYAML = (value: unknown) =>
  JSON.stringify(wellFormed(value)).replace(
    /[\u007f-\u009f\ufffe\uffff]/g,
    (c) => `\\u${c.charCodeAt(0).toString(16).padStart(4, "0")}`,
  );

function wellFormed(value: unknown): unknown {
  if (typeof value === "string") return value.toWellFormed();
  if (Array.isArray(value)) return value.map(wellFormed);
  if (isObject(value)) {
    return Object.fromEntries(
      Object.entries(value).map(([k, v]) => [k.toWellFormed(), wellFormed(v)]),
    );
  }
  return value;
}

// The file a bare name names: the local folder's when it has one, since a local file takes the
// place of a shipped one of the same name, as the developer's own seed files do; else the shipped
// folder's. Only a regular file: a link is never followed, since it could lead out of the folder.
async function find(folders: AgentFolders, path: string): Promise<Found | undefined> {
  for (const folder of ["local", "shipped"] as const) {
    const file = join(folders[folder], path);
    const found = await lstat(file).catch(() => undefined);
    if (found?.isFile()) return { path, folder, file, modified: found.mtimeMs };
  }
}

const shown = (f: Found) => ({ path: f.path, folder: f.folder, modified: f.modified });

// A top-level setting of an agent file's settings block as written, enough to list and publish it;
// voice reads the whole file itself.
function setting(text: string, key: string): string {
  const block = /^---\r?\n([\s\S]*?)\r?\n---/.exec(text)?.[1] ?? "";
  const raw = new RegExp(`^${key}:[ \\t]*(.*)$`, "m").exec(block)?.[1].trim() ?? "";
  if (raw.startsWith('"')) {
    try {
      return String(JSON.parse(raw));
    } catch {
      return raw;
    }
  }
  if (raw.startsWith("'")) return raw.slice(1, -1).replaceAll("''", "'");
  return raw.replace(/\s+#.*$/, "");
}

// A file name from an agent's name: accents dropped, every other run of characters but a to z and
// digits one dash.
function slug(name: string): string {
  return name
    .normalize("NFKD")
    .replace(/\p{M}/gu, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, slugLength)
    .replace(/-+$/, "");
}

// Reads a JSON object from a request's body up to limit bytes, decoded once whole so a character
// split between chunks stays whole; past the limit it stops reading and, unless told to keep the
// connection (so the caller can answer the page before closing it), drops the connection.
export function readJSON(
  req: Incoming,
  limit: number,
  drop = true,
): Promise<Record<string, unknown>> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];
    let size = 0;
    req.on("data", (chunk: Buffer | string) => {
      if (size > limit) return;
      const bytes = typeof chunk === "string" ? Buffer.from(chunk) : chunk;
      size += bytes.length;
      if (size > limit) {
        if (drop) req.destroy?.();
        reject(new Error("the request is over the limit"));
      } else chunks.push(bytes);
    });
    req.on("end", () => {
      if (size > limit) return;
      try {
        const asked = JSON.parse(Buffer.concat(chunks).toString("utf8"));
        resolve(asked !== null && typeof asked === "object" ? asked : {});
      } catch {
        resolve({});
      }
    });
    req.on("error", reject);
  });
}

function json(to: Reply, status: number, body: unknown): true {
  to.statusCode = status;
  to.setHeader("Content-Type", "application/json");
  to.end(JSON.stringify(body));
  return true;
}

function fail(to: Reply, status: number, why: string): true {
  to.statusCode = status;
  to.setHeader("Content-Type", "text/plain; charset=utf-8");
  to.end(why);
  return true;
}

const badPath = (to: Reply) =>
  fail(to, 400, "the path must be an agent file's bare name, such as coach.md");

const message = (err: unknown) => (err instanceof Error ? err.message : String(err));
