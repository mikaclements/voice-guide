// The Agents tab's calls, served by the page's dev server: it lists the agent files, reads agents
// from voice, writes the form to an agent's file and publishes it, all with the product key, which
// never reaches the page.
import { problemFrom } from "../client/api.ts";

export type AgentFile = {
  path: string;
  folder: "local" | "shipped";
  id: string;
  name: string;
  modified: number;
  // The newest published version: null when never published, "unknown" when voice couldn't say.
  version: number | null | "unknown";
  published_at: string | null;
  // The other agent files with this one's id, which a publish refuses until one gets a new id.
  same_id_as: { path: string; folder: "local" | "shipped" }[];
};
// The files, and why voice couldn't say their versions when it couldn't.
export type AgentFiles = { files: AgentFile[]; problem?: string };

// An agent's settings under its agent file's names and in its shapes: null or empty where the file
// leaves a setting out.
export type AgentSettings = {
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
// An agent's newest version, as voice answers it.
export type AgentVersion = AgentSettings & { version: number; published_at: string };

export const listAgentFiles = (fetcher: typeof fetch = fetch) =>
  json<AgentFiles>(fetcher, "GET", "/product/agent-files");

// The agent's newest version as voice holds it, or null when it was never published.
export async function readAgent(
  id: string,
  fetcher: typeof fetch = fetch,
): Promise<AgentVersion | null> {
  const res = await fetcher(`/product/agent?id=${encodeURIComponent(id)}`);
  if (res.status === 404) return null;
  if (!res.ok) throw await problemFrom(res);
  return res.json();
}

// Has the dev server write the settings to the agent's file and publish it, and answers how it went
// with the version voice now holds. What voice refuses in the file is told as its refusal, in the
// loader's or the check's own words.
export async function publishAgent(settings: AgentSettings, fetcher: typeof fetch = fetch) {
  const res = await fetcher("/product/agent", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(settings),
  });
  if (res.status === 422 && res.headers.get("Content-Type")?.includes("application/problem+json")) {
    const p = await res.json();
    throw new Error(`voice-guide refused it: ${p.detail || p.title}`);
  }
  if (!res.ok) throw await problemFrom(res);
  const agent: AgentVersion & { voice_checked?: boolean } = await res.json();
  return { line: publishedLine(res.status, agent), agent };
}

// Publishes the file as it is on disk, and says how it went.
export async function publishAgentFile(path: string, fetcher: typeof fetch = fetch) {
  const res = await send(fetcher, "POST", "/product/publish", { path });
  return publishedLine(res.status, await res.json());
}

// Voice saved a new version, or had it; and when text-to-speech couldn't be asked, the agent's voice
// went unchecked, since one it doesn't offer could then have been published.
function publishedLine(status: number, p: { version: number; voice_checked?: boolean }) {
  const line =
    status === 201 ? `published version ${p.version}` : `unchanged, version ${p.version}`;
  return p.voice_checked === false ? `${line}, voice not checked` : line;
}

// Calls the dev server, which answers with problems the way voice does.
async function send(fetcher: typeof fetch, method: string, path: string, body?: unknown) {
  const res = await fetcher(path, {
    method,
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await problemFrom(res);
  return res;
}

async function json<T>(fetcher: typeof fetch, method: string, path: string, body?: unknown) {
  return (await send(fetcher, method, path, body)).json() as Promise<T>;
}
