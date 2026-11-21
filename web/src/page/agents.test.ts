import assert from "node:assert/strict";
import { test } from "node:test";
import {
  listAgentFiles,
  newAgent,
  openAgentFile,
  publishAgentFile,
  saveAgentFile,
} from "./agents.ts";

// A dev server that answers every call with answer, recording what the page asked.
function devServer(answer: () => Response) {
  const asked: { url: string; method?: string; body?: unknown }[] = [];
  const fetcher = (async (url: string, init?: RequestInit) => {
    const body = init?.body ? JSON.parse(init.body as string) : undefined;
    asked.push({ url, method: init?.method, body });
    return answer();
  }) as typeof fetch;
  return { fetcher, asked };
}

test("publishing says whether voice saved a new version or had it already, and tells a problem by its title and detail", async () => {
  const cases: [Response, string][] = [
    [Response.json({ version: 2 }, { status: 201 }), "published version 2"],
    [Response.json({ version: 2 }, { status: 200 }), "unchanged, version 2"],
  ];
  for (const [answer, line] of cases) {
    const dev = devServer(() => answer);
    assert.equal(await publishAgentFile("coach.md", dev.fetcher), line);
    assert.deepEqual(dev.asked, [
      { url: "/product/publish", method: "POST", body: { path: "coach.md" } },
    ]);
  }
  const problem = Response.json(
    { type: "about:blank", title: "Invalid agent file", status: 422, detail: "name is missing" },
    { status: 422, headers: { "Content-Type": "application/problem+json" } },
  );
  await assert.rejects(
    publishAgentFile("coach.md", devServer(() => problem).fetcher),
    /Invalid agent file \(422\): name is missing/,
  );
});

test("a save sends the file's text with when it was modified as the page opened it", async () => {
  const dev = devServer(() => Response.json({ modified: 2 }));
  assert.deepEqual(await saveAgentFile("coach.md", "text", 1, dev.fetcher), { modified: 2 });
  assert.deepEqual(dev.asked, [
    {
      url: "/product/agent-file",
      method: "PUT",
      body: { path: "coach.md", content: "text", modified: 1 },
    },
  ]);
  const stale = new Response("coach.md changed on disk since it was opened; reload it", {
    status: 409,
    headers: { "Content-Type": "text/plain; charset=utf-8" },
  });
  await assert.rejects(
    saveAgentFile("coach.md", "text", 1, devServer(() => stale).fetcher),
    /changed on disk/,
  );
});

test("listing, opening and making agent files ask the dev server, which holds the key", async () => {
  const dev = devServer(() => Response.json({}));
  await listAgentFiles(dev.fetcher);
  await openAgentFile("night watch.md", dev.fetcher);
  await newAgent("Night watch", dev.fetcher);
  assert.deepEqual(dev.asked, [
    { url: "/product/agent-files", method: "GET", body: undefined },
    { url: "/product/agent-file?path=night%20watch.md", method: "GET", body: undefined },
    { url: "/product/agent-files", method: "POST", body: { name: "Night watch" } },
  ]);
});
