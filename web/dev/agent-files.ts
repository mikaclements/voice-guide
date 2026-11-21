import { randomBytes } from "node:crypto";
import { lstat, mkdir, readdir, readFile, rename, rm, writeFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import type { Incoming, Reply } from "./product-server.ts";

// The two folders of agent files the page lists: local, the developer's own test agents, which the
// page writes, and shipped, the repository's agents, which it only reads and publishes, so the page
// never edits a tracked file.
export type AgentFolders = { local: string; shipped: string };

// What a new agent starts from, in the shipped folder.
export const templateFile = "mock-interviewer.md";

// Only a bare file name, so a path from the page can't name anything outside the two folders.
const fileName = /^[a-z0-9][a-z0-9-]*\.md$/;
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
// Voice's cap on an agent file: a larger one is refused here rather than sent to be refused.
const fileLimit = 256 * 1024;
// A save carries the whole file as a JSON string; four times the cap leaves room for its escapes.
const saveBodyLimit = 4 * fileLimit;
// A name or a path, with room to spare.
const smallBodyLimit = 4096;
// Long enough for a readable file name, far under any file system's limit; a round number.
const slugLength = 64;

type Voice = (path: string, init?: RequestInit) => Promise<Response>;
type Found = { path: string; folder: "local" | "shipped"; file: string; modified: number };
type Scanned = Found & { id: string; name: string };

// The routes behind the page's agents panel, mounted at /product beside the product's own: the
// files in both folders with their published versions, one file's text, a save, a new agent and a
// publish. call reaches voice with the product key, which these routes never see. Answers true when
// the request was one of these routes.
export function agentFileRoutes(folders: AgentFolders, call: Voice) {
  return async (req: Incoming, to: Reply): Promise<boolean> => {
    const url = new URL(req.url ?? "/", "http://product");
    const route = `${req.method} ${url.pathname}`;
    const writes = ["PUT /agent-file", "POST /agent-files", "POST /publish"];
    if (!["GET /agent-files", "GET /agent-file", ...writes].includes(route)) return false;
    try {
      if (fromAnotherHost(req)) return fail(to, 403, hostRefused);
      if (writes.includes(route)) {
        const refusal = fromElsewhere(req);
        if (refusal) return fail(to, ...refusal);
      }
      if (route === "GET /agent-files") return json(to, 200, await list(folders, call));
      if (route === "GET /agent-file") {
        const path = url.searchParams.get("path") ?? "";
        if (!fileName.test(path)) return badPath(to);
        const found = await find(folders, path);
        if (!found) return fail(to, 404, `no agent file named ${path}`);
        const content = await readFile(found.file, "utf8");
        return json(to, 200, { ...shown(found), content });
      }
      const limit = route === "PUT /agent-file" ? saveBodyLimit : smallBodyLimit;
      const asked = await readJSON(req, limit).catch(() => undefined);
      if (!asked) return fail(to, 413, "the request is over the limit");
      if (route === "POST /agent-files") return await create(folders, asked.name, to);
      const path = typeof asked.path === "string" ? asked.path : "";
      if (!fileName.test(path)) return badPath(to);
      const found = await find(folders, path);
      if (!found) return fail(to, 404, `no agent file named ${path}`);
      if (route === "PUT /agent-file") return await save(found, asked, to);
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
    .filter((o) => o.id !== "" && o.id === f.id && o.file !== f.file)
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

async function save(found: Found, asked: Record<string, unknown>, to: Reply) {
  if (found.folder === "shipped") {
    return fail(to, 403, `${found.path} is shipped, so the page only reads it`);
  }
  if (typeof asked.content !== "string" || typeof asked.modified !== "number") {
    return fail(to, 400, "a save is {path, content, modified}");
  }
  if (Buffer.byteLength(asked.content) > fileLimit) {
    return fail(to, 413, "the file is over 256 KiB, voice's limit");
  }
  // Another editor saved it since the page opened it: writing now would lose that.
  if (found.modified !== asked.modified) {
    return fail(to, 409, `${found.path} changed on disk since it was opened; reload it`);
  }
  // Renamed into place, so a crash mid-write leaves the old file whole; the dot keeps it out of the
  // list.
  const temp = join(dirname(found.file), `.${found.path}.${randomBytes(4).toString("hex")}`);
  try {
    await writeFile(temp, asked.content);
    await rename(temp, found.file);
  } finally {
    await rm(temp, { force: true });
  }
  return json(to, 200, { modified: (await lstat(found.file)).mtimeMs });
}

async function create(folders: AgentFolders, name: unknown, to: Reply) {
  const given = typeof name === "string" ? name.trim() : "";
  if (/\p{Cc}/u.test(given)) return fail(to, 422, "the name must be one line");
  const path = `${slug(given)}.md`;
  if (!fileName.test(path)) {
    return fail(to, 422, "the name needs a letter a to z or a digit, to name its file");
  }
  if (await find(folders, path)) return fail(to, 409, `an agent file named ${path} exists`);
  const id = uuidv7();
  const text = fromTemplate(await readFile(join(folders.shipped, templateFile), "utf8"), id, given);
  await mkdir(folders.local, { recursive: true });
  try {
    // wx: whatever has the name, a file made since the check above or a link, is kept.
    await writeFile(join(folders.local, path), text, { flag: "wx" });
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === "EEXIST") {
      return fail(to, 409, `${path} exists in the local folder`);
    }
    throw err;
  }
  return json(to, 201, { path, id, name: given });
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
  let res: Response;
  try {
    res = await call(`/v1/agents/${id}`, {
      method: "PUT",
      headers: { "Content-Type": "text/markdown" },
      body: new Uint8Array(file),
    });
  } catch (err) {
    return fail(to, 502, `the product's server couldn't reach voice: ${message(err)}`);
  }
  // Voice's answer as it is, problems included, so the page reads them as voice's own.
  to.statusCode = res.status;
  to.setHeader("Content-Type", res.headers.get("Content-Type") ?? "application/json");
  to.end(await res.text());
  return true;
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

// The template with its id and name replaced; the name quoted, as JSON, which YAML reads, so a
// colon or a word YAML would take for true or a number stays the name.
function fromTemplate(template: string, id: string, name: string): string {
  const block = /^---\r?\n[\s\S]*?\r?\n---/.exec(template)?.[0] ?? "";
  if (!/^id:/m.test(block) || !/^name:/m.test(block)) {
    throw new Error(`${templateFile} has no id or name to replace`);
  }
  const replaced = block
    .replace(/^id:.*$/m, () => `id: ${id}`)
    .replace(/^name:.*$/m, () => `name: ${JSON.stringify(name)}`);
  return replaced + template.slice(block.length);
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

// A UUID version 7 (RFC 9562): the time in milliseconds, then random bits, so agents' IDs sort by
// when they were made, as every other ID in voice-guide does.
export function uuidv7(now = Date.now()): string {
  const bytes = randomBytes(16);
  bytes.writeUIntBE(now, 0, 6);
  bytes[6] = (bytes[6] & 0x0f) | 0x70;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = bytes.toString("hex");
  return hex.replace(/^(.{8})(.{4})(.{4})(.{4})/, "$1-$2-$3-$4-");
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
