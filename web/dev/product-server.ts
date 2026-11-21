import { readFile } from "node:fs/promises";
import { relative, resolve } from "node:path";
import type { Plugin } from "vite";
import { type AgentFolders, agentFileRoutes, fromElsewhere, readJSON } from "./agent-files.ts";
import { seedFile } from "./seed.ts";
import {
  documentsFor,
  overrides,
  type Sessions,
  sessionRoutes,
  startWithDocumentsLimit,
  userReference,
} from "./sessions.ts";

const repo = resolve(import.meta.dirname, "../..");
const placeholderUser = resolve(repo, "seed/placeholder-user");
// The developer's own test data, gitignored: a file here is used instead of its sample.
const localUser = resolve(repo, "seed/local");

// Where voice is: VOICE_ADDR, which the other scripts find it by too.
export const voiceAddress = process.env.VOICE_ADDR ?? "127.0.0.1:8080";

// What the product's server needs to call voice: where it is, and the product key it starts sessions
// and lists agents with.
type Product = {
  voice: string;
  key: string;
  // The folder of what the product knows about the user: their reference, as reference.txt, and one
  // block of session context per file, as <name>.md.
  user: string;
  // A folder of the developer's own files that take the place of the same names in user.
  local?: string;
  // The agent files the page lists, edits and publishes; without them, it has no such routes.
  agents?: AgentFolders;
  fetcher?: typeof fetch;
  // How long a call to voice may take before the product's server gives up on it, defaultTimeoutMs
  // unless set.
  timeoutMs?: number;
};

// A call to voice here is a read or a write of a few rows, which takes milliseconds when voice is
// up; 10 s is a round guess, far past any healthy call, so a voice that is stuck ends in an error
// on the page instead of leaving it waiting.
export const defaultTimeoutMs = 10_000;

export type Reply = {
  statusCode: number;
  setHeader(name: string, value: string): unknown;
  end(body: string): unknown;
};

// What the routes read of a request: where it goes, and its body as it arrives.
export type Incoming = {
  url?: string;
  method?: string;
  headers?: Record<string, string | string[] | undefined>;
  destroy?(): unknown;
  on(event: string, cb: (chunk: Buffer | string) => void): unknown;
};

// The dev server plays the product's server, which a product embedding voice-guide runs itself: it
// lists agents and starts each session with the product key, naming the user by the reference the
// product keeps for them and sending the session context the agent asks for, here the invented
// placeholder user's files in seed/. It hands the page the session and the ticket for it, which the
// page talks to voice with; the key never leaves this server. Only the dev server has these routes.
export function devProduct(): Plugin {
  return {
    name: "dev-product",
    configureServer(server) {
      for (const file of ["resume.md", "reference.txt"]) {
        console.log(
          `dev product: ${file} from ${relative(repo, seedFile(placeholderUser, localUser, file))}`,
        );
      }
      server.middlewares.use(
        "/product",
        productRoutes({
          voice: `http://${voiceAddress}`,
          key: process.env.VOICE_DEV_PRODUCT_KEY ?? "",
          user: placeholderUser,
          local: localUser,
          agents: { local: resolve(localUser, "agents"), shipped: resolve(repo, "agents") },
        }),
      );
    },
  };
}

// The product's routes, mounted at /product: GET /agents, and POST /start with the agent and the
// request ID, which voice repeats as the same session. Voice's answers pass through as they are, so
// the page reads its problems as it reads voice's own.
export function productRoutes(product: Product) {
  const fetcher = product.fetcher ?? fetch;
  const call = (path: string, init?: RequestInit) =>
    fetcher(`${product.voice}${path}`, {
      ...init,
      signal: AbortSignal.timeout(product.timeoutMs ?? defaultTimeoutMs),
      headers: { ...init?.headers, Authorization: `Bearer ${product.key}` },
    });
  const pass = async (res: Response, to: Reply) => {
    to.statusCode = res.status;
    to.setHeader("Content-Type", res.headers.get("Content-Type") ?? "application/json");
    // A start's answer carries a credential, so nothing along the way may keep it.
    to.setHeader("Cache-Control", "no-store");
    to.end(await res.text());
  };
  const fail = (to: Reply, status: number, why: string) => {
    to.statusCode = status;
    to.setHeader("Content-Type", "text/plain; charset=utf-8");
    to.end(why);
  };
  const agentFiles = product.agents && agentFileRoutes(product.agents, call);

  const sessions: Sessions = {
    call,
    pass,
    fail,
    block: (name) => contextBlock(product.user, name, product.local),
    folder: relative(repo, product.user),
    user: product.user,
    local: product.local,
  };

  return async (req: Incoming, to: Reply) => {
    try {
      if (!product.key) {
        return fail(
          to,
          500,
          "VOICE_DEV_PRODUCT_KEY is not set; serve the page with scripts/web.sh",
        );
      }
      if (agentFiles && (await agentFiles(req, to))) return;
      if (req.url === "/agents" && req.method === "GET") {
        return await pass(await call("/v1/agents"), to);
      }
      if (await sessionRoutes(sessions, req, to)) return;
      if (req.url === "/start" && req.method === "POST") {
        const refusal = fromElsewhere(req);
        if (refusal) return fail(to, ...refusal);
        // Past the limit the connection is kept for this answer and closed after it, so the page
        // reads the reason instead of a dropped connection.
        const asked = await readJSON(req, startWithDocumentsLimit, false).catch(() => undefined);
        if (!asked) {
          to.setHeader("Connection", "close");
          return fail(to, 413, `the start is over ${startWithDocumentsLimit} bytes`);
        }
        if (typeof asked.agent_id !== "string" || typeof asked.request_id !== "string") {
          return fail(to, 400, "the body must be a small JSON object with agent_id and request_id");
        }
        const agent = asked.agent_id;
        const request = asked.request_id;
        const chosen = overrides(asked);
        if (typeof chosen === "string") return fail(to, 400, chosen);
        // What the agent asks the product for at its start, which its list says.
        const found = await documentsFor(sessions, agent, chosen.session_context);
        if ("why" in found) return fail(to, found.status, found.why);
        const body = JSON.stringify({
          agent_id: agent,
          request_id: request,
          user: await userReference(sessions),
          session_context: found.documents,
          voice: chosen.voice,
          speed: chosen.speed,
        });
        return await pass(
          await call("/v1/sessions", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body,
          }),
          to,
        );
      }
      return fail(to, 404, "the product has no such route");
    } catch (err) {
      // Voice not running, say: told plainly, with nothing of the key.
      return fail(
        to,
        502,
        `the product's server couldn't reach voice: ${err instanceof Error ? err.message : err}`,
      );
    }
  };
}

// One block of session context by name from folder, one file per name, or undefined when there is
// none. The name may hold only these characters, so it can't leave the folder.
export async function contextBlock(
  folder: string,
  name: string,
  local?: string,
): Promise<string | undefined> {
  const file = /^[a-z0-9_-]+$/.test(name) ? seedFile(folder, local, `${name}.md`) : "";
  const content = file && (await readFile(file, "utf8").catch(() => ""));
  return content || undefined;
}
