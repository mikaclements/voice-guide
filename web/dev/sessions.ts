import { readFile } from "node:fs/promises";
import { seedFile } from "./seed.ts";

// What the session routes need of the product's server: its calls to voice (which carry the key,
// so nothing here sees it), how it answers, and where the placeholder user's files are.
export type Sessions = {
  call(path: string, init?: RequestInit): Promise<Response>;
  pass(res: Response, to: Reply): Promise<unknown>;
  fail(to: Reply, status: number, why: string): unknown;
  // A block of session context by name, or undefined when the placeholder user has none.
  block(name: string): Promise<string | undefined>;
  // The folder the placeholder user's files are in, as the page may be told of it.
  folder: string;
  user: string;
  local?: string;
};

type Reply = {
  statusCode: number;
  setHeader(name: string, value: string): unknown;
  end(body: string): unknown;
};

type Request = {
  url?: string;
  method?: string;
  headers?: Record<string, string | string[] | undefined>;
};

type Document = { name: string; content: string };

// A start's body holds documents, which voice allows 280,000 characters of by default; JSON can
// double that when the text is mostly quotes or line breaks, so this leaves room for that and for
// the rest of the body. Voice's own limit still decides what it takes.
export const startWithDocumentsLimit = 600_000;

// Whether a request that changes something came from another site: a page on another origin can
// send a JSON-typed POST to 127.0.0.1 only with a preflight this server never answers, but the
// check doesn't lean on that alone. The browser says where a request came from, and a page of this
// server's own always says its own origin, so one from anywhere else is refused.
export function crossSite(req: Request): boolean {
  const headers = req.headers ?? {};
  if (headers["sec-fetch-site"] === "cross-site") return true;
  const origin = headers.origin;
  if (origin === undefined) return false;
  try {
    return new URL(String(origin)).host !== headers.host;
  } catch {
    return true;
  }
}

// What a start may override, as the page sent it, or why it can't be read. Only what voice
// wouldn't say plainly is checked here; voice checks the rest, and its problem is shown as it is.
export function overrides(
  asked: Record<string, unknown>,
): { voice?: string; speed?: number; session_context?: Document[] } | string {
  const out: { voice?: string; speed?: number; session_context?: Document[] } = {};
  if (asked.voice !== undefined && asked.voice !== "") {
    if (typeof asked.voice !== "string") return "voice must be a string";
    out.voice = asked.voice;
  }
  if (asked.speed !== undefined) {
    if (typeof asked.speed !== "number" || !Number.isFinite(asked.speed)) {
      return "speed must be a number";
    }
    out.speed = asked.speed;
  }
  if (asked.session_context !== undefined) {
    const docs = asked.session_context;
    const shaped =
      Array.isArray(docs) &&
      docs.every(
        (d) =>
          d !== null &&
          typeof d === "object" &&
          typeof d.name === "string" &&
          typeof d.content === "string",
      );
    if (!shaped) return "session_context must be a list of {name, content} strings";
    out.session_context = docs.map((d: Document) => ({ name: d.name, content: d.content }));
  }
  return out;
}

// The documents a start sends for the agent: the placeholder user's file for each name the agent
// asks for at its start, and a refusal that says why when one can't be had. A document the page
// gives takes the place of the file of its name, even empty, so voice says what it makes of it.
export async function documentsFor(
  s: Sessions,
  agent: string,
  given: Document[] = [],
): Promise<{ documents: Document[] } | { status: number; why: string }> {
  const list = await s.call("/v1/agents");
  if (!list.ok) {
    // Voice refused the list, not the start: the page must not take that for voice's answer to its
    // request ID, which voice hasn't seen.
    return { status: 502, why: `voice refused the agent list (${list.status}); try again` };
  }
  const offered: { id: string; session_context: string[] }[] = (await list.json()).agents;
  const wanted = offered.find((a) => a.id === agent)?.session_context ?? [];
  const documents: Document[] = [];
  for (const name of wanted) {
    const own = given.find((d) => d.name === name);
    if (own) {
      documents.push(own);
      continue;
    }
    const content = await s.block(name);
    if (content === undefined) {
      return { status: 404, why: `the placeholder user has no "${name}" in ${s.folder}/` };
    }
    documents.push({ name, content });
  }
  // Documents the agent doesn't ask at its start (a summary's, say) go as given.
  documents.push(...given.filter((d) => !wanted.includes(d.name)));
  return { documents };
}

// The placeholder user's reference, which a start names the user by.
export async function userReference(s: Sessions): Promise<string> {
  return (await readFile(seedFile(s.user, s.local, "reference.txt"), "utf8")).trim();
}

// The routes the sessions panel uses besides the start: GET /voices, GET /sessions and GET
// /context?agent=. Returns whether the request was one of them.
export async function sessionRoutes(s: Sessions, req: Request, to: Reply): Promise<boolean> {
  if (req.method !== "GET" || !req.url) return false;
  const url = new URL(req.url, "http://page");
  if (url.pathname === "/voices") {
    await s.pass(await s.call("/v1/voices"), to);
    return true;
  }
  if (url.pathname === "/sessions") {
    // The newest page, which is what the page is for; the user is read as a start reads it.
    const user = encodeURIComponent(await userReference(s));
    await s.pass(await s.call(`/v1/users/${user}/sessions?limit=20`), to);
    return true;
  }
  if (url.pathname === "/context") {
    const found = await documentsFor(s, url.searchParams.get("agent") ?? "");
    if ("why" in found) {
      s.fail(to, found.status, found.why);
    } else {
      to.statusCode = 200;
      to.setHeader("Content-Type", "application/json");
      to.end(JSON.stringify({ session_context: found.documents }));
    }
    return true;
  }
  return false;
}
