// The agents panel's calls, served by the page's dev server: it reads and writes the agent files and
// publishes them with the product key, which never reaches the page.
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
};
// The files, and why voice couldn't say their versions when it couldn't.
export type AgentFiles = { files: AgentFile[]; problem?: string };
export type Opened = {
  path: string;
  folder: AgentFile["folder"];
  modified: number;
  content: string;
};

export const listAgentFiles = (fetcher: typeof fetch = fetch) =>
  json<AgentFiles>(fetcher, "GET", "/product/agent-files");

export const openAgentFile = (path: string, fetcher: typeof fetch = fetch) =>
  json<Opened>(fetcher, "GET", `/product/agent-file?path=${encodeURIComponent(path)}`);

// Saves the text over the file, refused when the file changed on disk since modified, when the page
// opened it.
export const saveAgentFile = (
  path: string,
  content: string,
  modified: number,
  fetcher: typeof fetch = fetch,
) => json<{ modified: number }>(fetcher, "PUT", "/product/agent-file", { path, content, modified });

// Makes a test agent from the template, named name, and answers its file's name.
export const newAgent = (name: string, fetcher: typeof fetch = fetch) =>
  json<{ path: string }>(fetcher, "POST", "/product/agent-files", { name });

// Publishes the file as it is on disk, and says how it went: voice saved a new version, or had it.
export async function publishAgentFile(path: string, fetcher: typeof fetch = fetch) {
  const res = await send(fetcher, "POST", "/product/publish", { path });
  const { version } = await res.json();
  return res.status === 201 ? `published version ${version}` : `unchanged, version ${version}`;
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
