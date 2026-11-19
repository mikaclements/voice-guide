// The product's calls, served by the page's dev server: it lists the agents and starts sessions
// with the product key, which never reaches the page. Only this page has them; a product's page
// has its own server for the same.
import { problemFrom, type Session } from "../client/api.ts";

export type Agent = { id: string; name: string; description: string; session_context: string[] };
// A started session with the ticket for it: the product's server starts it and hands both over.
export type Started = Session & { ticket: string };

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
): Promise<Started> {
  return product<Started>(fetcher, "/product/start", { agent_id: agentId, request_id: requestId });
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
