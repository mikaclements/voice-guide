import { readFile } from "node:fs/promises";
import { relative, resolve } from "node:path";
import { defineConfig, type Plugin } from "vite";
import { seedFile } from "./seed.ts";

const repo = resolve(import.meta.dirname, "..");
const placeholderUser = resolve(repo, "seed/placeholder-user");
// The developer's own test data, gitignored: a file here is used instead of its sample.
const localUser = resolve(repo, "seed/local");

// A start's body is two UUIDs; this leaves room, and stops a page that sends more from being read.
const startBodyLimit = 4096;

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
  fetcher?: typeof fetch;
  // How long a call to voice may take before the product's server gives up on it, defaultTimeoutMs
  // unless set.
  timeoutMs?: number;
};

// A call to voice here is a read or a write of a few rows, which takes milliseconds when voice is
// up; 10 s is a round guess, far past any healthy call, so a voice that is stuck ends in an error
// on the page instead of leaving it waiting.
export const defaultTimeoutMs = 10_000;

type Reply = {
  statusCode: number;
  setHeader(name: string, value: string): unknown;
  end(body: string): unknown;
};

// What the routes read of a request: where it goes, and its body as it arrives.
type Incoming = {
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
function devProduct(): Plugin {
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
          voice: `http://${process.env.VOICE_ADDR ?? "127.0.0.1:8080"}`,
          key: process.env.VOICE_DEV_PRODUCT_KEY ?? "",
          user: placeholderUser,
          local: localUser,
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

  return async (req: Incoming, to: Reply) => {
    try {
      if (!product.key) {
        return fail(
          to,
          500,
          "VOICE_DEV_PRODUCT_KEY is not set; serve the page with scripts/web.sh",
        );
      }
      if (req.url === "/agents" && req.method === "GET") {
        return await pass(await call("/v1/agents"), to);
      }
      if (req.url === "/start" && req.method === "POST") {
        // A page on another site can send a form-typed POST here with no preflight; JSON it can't.
        if (!String(req.headers?.["content-type"] ?? "").startsWith("application/json")) {
          return fail(to, 415, "the start takes application/json");
        }
        let agent: string;
        let request: string;
        try {
          const asked = JSON.parse(await readBody(req));
          agent = String(asked.agent_id);
          request = String(asked.request_id);
        } catch {
          return fail(to, 400, "the body must be a small JSON object with agent_id and request_id");
        }
        // What the agent asks the product for at its start, which its list says.
        const list = await call("/v1/agents");
        if (!list.ok) {
          // Voice refused the list, not the start: the page must not take that for voice's answer
          // to its request ID, which voice hasn't seen.
          return fail(to, 502, `voice refused the agent list (${list.status}); try again`);
        }
        const offered: { id: string; session_context: string[] }[] = (await list.json()).agents;
        const wanted = offered.find((a) => a.id === agent)?.session_context ?? [];
        const session_context = [];
        for (const name of wanted) {
          const content = await contextBlock(product.user, name, product.local);
          if (content === undefined) {
            return fail(
              to,
              404,
              `the placeholder user has no "${name}" in ${relative(repo, product.user)}/`,
            );
          }
          session_context.push({ name, content });
        }
        const user = (
          await readFile(seedFile(product.user, product.local, "reference.txt"), "utf8")
        ).trim();
        const body = JSON.stringify({
          agent_id: agent,
          request_id: request,
          user,
          session_context,
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

// Reads a request's body up to startBodyLimit characters; past that it stops reading, drops the
// connection and refuses.
export function readBody(req: Incoming): Promise<string> {
  return new Promise((resolve, reject) => {
    let body = "";
    let over = false;
    req.on("data", (chunk: Buffer | string) => {
      if (over) return;
      body += chunk;
      if (body.length > startBodyLimit) {
        over = true;
        body = "";
        req.destroy?.();
        reject(new Error("the request is over the limit"));
      }
    });
    req.on("end", () => resolve(body));
    req.on("error", reject);
  });
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

export default defineConfig({
  plugins: [devProduct()],
  server: {
    // 127.0.0.1 only: the product routes start sessions for the placeholder user with the
    // development product key, so nothing else on the network may reach them.
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
    // Vite would otherwise let any page served from this machine, on any port, start a session here
    // and take its ticket, on the developer's Claude login. This page needs no cross-origin reads,
    // since its own origin serves everything it calls.
    cors: false,
    // The API goes through the dev server, so the page and voice share an origin and voice needs
    // no cross-origin rules; VOICE_ADDR is where the other scripts find voice too.
    proxy: { "/v1": `http://${process.env.VOICE_ADDR ?? "127.0.0.1:8080"}` },
  },
});
