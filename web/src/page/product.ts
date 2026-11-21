// The product's calls, served by the page's dev server: it lists the agents and starts sessions
// with the product key, which never reaches the page. Only this page has them; a product's page
// has its own server for the same.
import { problemFrom, type Session } from "../client/api.ts";

export type Agent = { id: string; name: string; description: string; session_context: string[] };
// A started session with the ticket for it: the product's server starts it and hands both over.
export type Started = Session & { ticket: string };

// A document of session context: what the product knows about the user, by name.
export type Document = { name: string; content: string };
// What a start may set beyond the agent: the voice and speed the replies are spoken in, and the
// documents to send in place of the placeholder user's files of the same names.
export type StartOptions = { voice?: string; speed?: number; session_context?: Document[] };
// A session as voice lists it afterwards.
export type PastSession = {
  id: string;
  agent_id: string;
  agent_version: number;
  started_at: string;
  ended_at: string | null;
  summary: string | null;
  summary_status: string | null;
};

export async function listAgents(fetcher: typeof fetch = fetch): Promise<Agent[]> {
  return (await product<{ agents: Agent[] }>(fetcher, "/product/agents")).agents;
}

// Asks the product's server to start a session with the agent for the user; it sends voice the
// user's reference and session context, with its key. A start repeated with the same request ID
// gets the same session back, with a new ticket.
export function startSession(
  agentId: string,
  requestId: string,
  fetcher: typeof fetch = fetch,
  options: StartOptions = {},
): Promise<Started> {
  return product<Started>(fetcher, "/product/start", {
    agent_id: agentId,
    request_id: requestId,
    ...options,
  });
}

// The voices a session may speak in.
export async function listVoices(fetcher: typeof fetch = fetch): Promise<string[]> {
  return (await product<{ voices: string[] }>(fetcher, "/product/voices")).voices;
}

// The documents a start for the agent would send, so the page can show them to change.
export async function contextFor(agentId: string, fetcher: typeof fetch = fetch) {
  const path = `/product/context?agent=${encodeURIComponent(agentId)}`;
  return (await product<{ session_context: Document[] }>(fetcher, path)).session_context;
}

// The placeholder user's newest past sessions.
export async function listSessions(fetcher: typeof fetch = fetch): Promise<PastSession[]> {
  return (await product<{ sessions: PastSession[] }>(fetcher, "/product/sessions")).sessions;
}

// Calls the product's server, which answers with problems the way voice does.
async function product<T>(fetcher: typeof fetch, path: string, body?: unknown): Promise<T> {
  const res = await fetcher(path, {
    method: body === undefined ? "GET" : "POST",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await problemFrom(res);
  return res.json();
}
