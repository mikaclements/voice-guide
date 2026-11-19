// A demo product: a tiny job board that embeds a voice-guide agent the way a company's own site
// would. Its server holds the product key and starts sessions; its page only ever gets a session's
// ID and ticket. Run it with scripts/demo.sh, with scripts/run.sh running.
import { createHmac, timingSafeEqual } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { createServer } from "node:http";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..", "..");

// The mock interviewer, as .claude/skills/drive-session/start-session.sh names it.
const AGENT_ID = "01a1de84-fbfc-7758-9af4-3ced6d8a90cc";
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
// A delivery whose clock is further off than this is a replay or a broken sender.
const TOLERANCE_SECONDS = 5 * 60;
// Voice's own cap on a delivery, so no genuine one is refused; a start request is a UUID in a
// small object, so 4 KiB is generous. Anything larger is answered 413 without being read through.
const WEBHOOK_LIMIT = 256 * 1024;
const PRACTICE_LIMIT = 4 * 1024;
// A start checks the key, reads the agent, looks up the user and loads user context, a few quick
// queries on voice's side. A round guess, to be measured in the demo.
const VOICE_TIMEOUT_MS = 10_000;
// The only settings this process takes from .env: it never sees the signing key or the database.
const SECRET_NAME = "JOB_BOARD_WEBHOOK_SECRET";
const ENV_NAMES = ["VOICE_DEV_PRODUCT_KEY", "VOICE_ADDR", SECRET_NAME];

// The three settings from the repo's .env, read as the shell reads it (comments, quotes), a value
// already in the environment winning, as in voice's own scripts, except the webhook secret, which
// comes from .env alone so it is never on a command line or in a shell's history.
export function readEnv(file = join(root, ".env"), env = process.env) {
  const out = {};
  let text = "";
  try {
    text = readFileSync(file, "utf8");
  } catch {
    // No .env yet: scripts/run.sh makes it.
  }
  for (const line of text.split("\n")) {
    const m = line.trim().match(/^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
    if (!m || !ENV_NAMES.includes(m[1])) continue;
    const quoted = m[2].match(/^(["'])(.*?)\1/);
    out[m[1]] = quoted ? quoted[2] : m[2].replace(/\s+#.*$/, "").trim();
  }
  for (const name of ENV_NAMES) {
    if (name !== SECRET_NAME && env[name] !== undefined) out[name] = env[name];
  }
  return out;
}

// The webhook secret is "whsec_" and 32 random bytes in base64. An empty or short one would make
// forged deliveries easy to sign, so anything else is refused.
function webhookKey(secret) {
  const key = Buffer.from((secret ?? "").replace(/^whsec_/, ""), "base64");
  if (key.length !== 32) throw new Error("the webhook secret is not whsec_ and 32 bytes in base64");
  return key;
}

// True when the delivery was signed with the secret and its clock is within five minutes. Voice
// signs "id.timestamp.body" with HMAC-SHA256 and sends "v1,<base64>" in webhook-signature; the
// header may hold several, space-separated, while a secret is being rotated, and any v1 one that
// matches counts.
export function verifyWebhook(secret, headers, body, nowSeconds = Date.now() / 1000) {
  const id = headers["webhook-id"];
  const timestamp = headers["webhook-timestamp"];
  const signatures = headers["webhook-signature"];
  if (!secret || !id || !timestamp || !signatures) return false;
  if (!/^\d+$/.test(timestamp)) return false;
  if (Math.abs(nowSeconds - Number(timestamp)) > TOLERANCE_SECONDS) return false;
  const key = Buffer.from(secret.replace(/^whsec_/, ""), "base64");
  if (key.length === 0) return false;
  const expected = createHmac("sha256", key).update(`${id}.${timestamp}.${body}`).digest();
  return signatures.split(" ").some((part) => {
    const [version, sig] = part.split(",");
    if (version !== "v1" || !sig) return false;
    const given = Buffer.from(sig, "base64");
    return given.length === expected.length && timingSafeEqual(given, expected);
  });
}

class TooLarge extends Error {}

function readBody(req, limit) {
  return new Promise((resolve, reject) => {
    let size = 0;
    const chunks = [];
    req.on("data", (c) => {
      size += c.length;
      if (size > limit) {
        reject(new TooLarge());
        req.removeAllListeners("data");
        req.resume();
      } else chunks.push(c);
    });
    req.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
    req.on("error", reject);
  });
}

const JSON_TYPE = { "Content-Type": "application/json" };
const RESTART = JSON.stringify({ restart: true });

const escapeHtml = (s) =>
  s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);

export function createJobBoard({
  voiceUrl,
  productKey,
  webhookSecret,
  user,
  resume,
  fetchImpl = fetch,
  voiceTimeoutMs = VOICE_TIMEOUT_MS,
  now = () => Date.now() / 1000,
  log = console.log,
}) {
  webhookKey(webhookSecret);
  // The page learns where voice is from the server, so one setting (VOICE_ADDR) moves both.
  const page = readFileSync(join(here, "index.html"), "utf8").replaceAll(
    "{{VOICE_URL}}",
    escapeHtml(voiceUrl),
  );
  // Delivery IDs already processed. A real product records the ID in the same transaction as the
  // work, with a unique insert, so two deliveries at once can't both do it, and keeps IDs at least
  // as long as voice retries a delivery (about 15 hours). This in-memory set suits one sitting.
  const seen = new Set();
  // Ended sessions by ID, the latest 20, which the page shows as feedback; a real product stores
  // them per user. Keyed by session, so two tabs and a late retry can't overwrite each other.
  const ended = new Map();

  function send(res, status, body, headers = {}) {
    res.writeHead(status, { "Cache-Control": "no-store", ...headers });
    res.end(body);
  }

  async function readLimited(req, res, limit) {
    try {
      return await readBody(req, limit);
    } catch (err) {
      if (!(err instanceof TooLarge)) throw err;
      send(res, 413, "too large", { Connection: "close" });
      return null;
    }
  }

  async function practice(req, res) {
    if (!(req.headers["content-type"] ?? "").startsWith("application/json")) {
      return send(res, 415, "JSON only");
    }
    const raw = await readLimited(req, res, PRACTICE_LIMIT);
    if (raw === null) return;
    let input = null;
    try {
      input = JSON.parse(raw);
    } catch {
      // Answered below.
    }
    // The page makes one request ID per click and repeats it until a start succeeds, and voice
    // answers a repeated ID with the same session. So a reply lost on the way back makes a retry,
    // never a second session.
    if (!UUID.test(input?.request_id ?? "")) return send(res, 400, "request_id must be a UUID");
    let id;
    let ticket;
    try {
      const upstream = await fetchImpl(`${voiceUrl}/v1/sessions`, {
        method: "POST",
        headers: { Authorization: `Bearer ${productKey}`, "Content-Type": "application/json" },
        signal: AbortSignal.timeout(voiceTimeoutMs),
        body: JSON.stringify({
          agent_id: AGENT_ID,
          request_id: input.request_id,
          // A real product takes the user reference from its logged-in user and refuses a visitor
          // with none. This demo has no accounts: every visitor is the placeholder user.
          user,
          session_context: [{ name: "resume", content: resume }],
        }),
      });
      // The page keeps its request ID and retries only on 502 and 504 (voice down, failing or
      // slow: the same ID is safe to send again). Any answer a retry can't change is 409: the
      // page drops the ID and starts fresh.
      if (upstream.status >= 400 && upstream.status < 500) {
        log(`voice refused the session: ${upstream.status}`);
        return send(res, 409, RESTART, JSON_TYPE);
      }
      if (!upstream.ok) {
        log(`voice answered ${upstream.status}`);
        return send(res, 502, "could not start the session");
      }
      const reply = await upstream.json();
      // A repeated ID past voice's window, or of a session the idle sweep ended, comes back as a
      // session that has ended or has no ticket: a retry can't use it.
      if (reply.ended_at || typeof reply.ticket !== "string" || typeof reply.id !== "string") {
        log("voice answered with a session that cannot be joined");
        return send(res, 409, RESTART, JSON_TYPE);
      }
      ({ id, ticket } = reply);
    } catch (err) {
      // Never err.message: the failed call carried the key.
      const timedOut = err?.name === "TimeoutError";
      log(`voice call failed: ${timedOut ? "timeout" : (err?.cause?.code ?? err?.name)}`);
      return send(res, timedOut ? 504 : 502, "could not start the session");
    }
    // Only the session and its ticket reach the browser, never the key or the rest of the reply.
    send(res, 200, JSON.stringify({ id, ticket }), { "Content-Type": "application/json" });
  }

  async function webhook(req, res) {
    const body = await readLimited(req, res, WEBHOOK_LIMIT);
    if (body === null) return;
    if (!verifyWebhook(webhookSecret, req.headers, body, now())) {
      // A fixed reason and the ID quoted and cut, so a wrong secret isn't silent and a sender
      // can't write what it likes into the log.
      const refused = JSON.stringify(String(req.headers["webhook-id"]).slice(0, 80));
      log(`webhook refused: bad signature or clock (webhook-id ${refused})`);
      return send(res, 401, "bad signature");
    }
    const id = req.headers["webhook-id"];
    if (seen.has(id)) return send(res, 200, "already handled");
    const event = JSON.parse(body);
    // Other event types are answered 2xx so voice stops sending them.
    if (event.type === "session.ended") {
      const { data } = event;
      if (!Array.isArray(data.turns)) throw new Error("no turns in the delivery");
      // Turns left out of the delivery to fit voice's cap still count.
      const turns = data.turns.length + (data.turns_left_out ?? 0);
      ended.set(data.id, { session: data.id, turns, summary: data.summary ?? null });
      if (ended.size > 20) ended.delete(ended.keys().next().value);
      // Only the session and a count: what the user said stays out of the logs.
      log(`session ${data.id} ended: ${turns} turns, summary ${data.summary ? "written" : "none"}`);
    }
    // Recorded once the work is done: a failure above answers 500 and voice sends it again.
    seen.add(id);
    send(res, 200, "ok");
  }

  const server = createServer(async (req, res) => {
    try {
      // Refusing other Host names stops a page on another site, pointed by DNS at this address,
      // from reading a session's ticket.
      const port = server.address()?.port;
      if (req.headers.host !== `127.0.0.1:${port}` && req.headers.host !== `localhost:${port}`) {
        return send(res, 421, "misdirected request");
      }
      const path = new URL(req.url, "http://localhost").pathname;
      if (req.method === "GET" && path === "/") {
        return send(res, 200, page, { "Content-Type": "text/html; charset=utf-8" });
      }
      if (req.method === "GET" && path === "/api/last-session") {
        // 200 either way, so the page's poll before the delivery has come isn't a red error in
        // its console: not ready until that session's delivery has arrived.
        const session = new URL(req.url, "http://localhost").searchParams.get("session");
        if (!session) return send(res, 400, "session is required");
        const found = ended.get(session);
        const reply = found ? { ready: true, ...found } : { ready: false };
        return send(res, 200, JSON.stringify(reply), JSON_TYPE);
      }
      if (req.method === "POST" && path === "/api/practice") return await practice(req, res);
      if (req.method === "POST" && path === "/webhook") return await webhook(req, res);
      // No CORS headers anywhere: only this server's own page may call it.
      send(res, req.method === "OPTIONS" ? 405 : 404, "not found");
    } catch (err) {
      log(`request failed: ${err?.code ?? err?.name}`);
      if (!res.headersSent) send(res, 500, "error");
    }
  });
  return server;
}

// The path of a user file: the developer's own in seed/local/ (gitignored) when it exists, else the
// tracked sample in seed/placeholder-user/. Each file is chosen on its own.
export function seedFile(seedDir, file) {
  const own = join(seedDir, "local", file);
  return existsSync(own) ? own : join(seedDir, "placeholder-user", file);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const env = readEnv();
  const productKey = env.VOICE_DEV_PRODUCT_KEY;
  if (!productKey) {
    console.error("job board: VOICE_DEV_PRODUCT_KEY is not in .env; run scripts/run.sh once");
    process.exit(1);
  }
  const resumeFile = seedFile(join(root, "seed"), "resume.md");
  const referenceFile = seedFile(join(root, "seed"), "reference.txt");
  console.log(`job board: resume from ${relative(root, resumeFile)}`);
  console.log(`job board: reference from ${relative(root, referenceFile)}`);
  let server;
  try {
    server = createJobBoard({
      voiceUrl: `http://${env.VOICE_ADDR || "127.0.0.1:8080"}`,
      productKey,
      webhookSecret: env.JOB_BOARD_WEBHOOK_SECRET,
      user: readFileSync(referenceFile, "utf8").trim(),
      resume: readFileSync(resumeFile, "utf8"),
    });
  } catch (err) {
    console.error(
      `job board: ${err.message}; set JOB_BOARD_WEBHOOK_SECRET in .env (see the README)`,
    );
    process.exit(1);
  }
  server.listen(5190, "127.0.0.1", () => console.log("job board: http://127.0.0.1:5190"));
}
