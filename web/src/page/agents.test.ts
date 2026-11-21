import assert from "node:assert/strict";
import { test } from "node:test";
import {
  type AgentSettings,
  listAgentFiles,
  publishAgent,
  publishAgentFile,
  readAgent,
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
    // Text-to-speech couldn't be asked, so a voice it doesn't offer could have been published.
    [
      Response.json({ version: 3, voice_checked: false }, { status: 201 }),
      "published version 3, voice not checked",
    ],
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

const settings: AgentSettings = {
  id: "01900000-0000-7000-8000-000000000001",
  name: "Night watch",
  description: "",
  reads: [],
  session_context: [],
  summary: null,
  opens: "user",
  model: null,
  voice: null,
  speed: null,
  persona: "You keep watch.",
};
const problem = (status: number, title: string, detail?: string) =>
  Response.json(
    { type: "about:blank", title, status, detail },
    { status, headers: { "Content-Type": "application/problem+json" } },
  );
const plain = (status: number, text: string) =>
  new Response(text, { status, headers: { "Content-Type": "text/plain; charset=utf-8" } });

test("listing the files and reading an agent ask the dev server, which holds the key; an agent never published reads as null", async () => {
  const dev = devServer(() => Response.json({ version: 2 }));
  await listAgentFiles(dev.fetcher);
  assert.deepEqual(await readAgent(settings.id, dev.fetcher), { version: 2 });
  assert.deepEqual(dev.asked, [
    { url: "/product/agent-files", method: "GET", body: undefined },
    { url: `/product/agent?id=${settings.id}`, method: undefined, body: undefined },
  ]);
  assert.equal(
    await readAgent(settings.id, devServer(() => problem(404, "Not Found")).fetcher),
    null,
  );
  await assert.rejects(
    readAgent(
      settings.id,
      devServer(() => plain(502, "the product's server couldn't reach voice: fetch failed"))
        .fetcher,
    ),
    /\(502\): the product's server couldn't reach voice: fetch failed/,
  );
});

test("publishing the form sends its settings, with when the list says its file was modified, and answers how it went with the version voice holds", async () => {
  for (const [status, line] of [
    [201, "published version 1"],
    [200, "unchanged, version 1"],
  ] as const) {
    const agent = { ...settings, version: 1, published_at: "2026-01-02T03:04:05Z" };
    const dev = devServer(() => Response.json(agent, { status }));
    assert.deepEqual(await publishAgent({ ...settings, modified: 12.5 }, dev.fetcher), {
      line,
      agent,
    });
    assert.deepEqual(dev.asked, [
      { url: "/product/agent", method: "POST", body: { ...settings, modified: 12.5 } },
    ]);
  }
});

test("what voice refuses in the form is told as its refusal, by its detail or else its title; voice down, with the reason; either way the file keeps what was sent", async () => {
  const kept = "\nThe agent file keeps what was sent; Revert fills only the form again.";
  const cases: [Response, string][] = [
    [
      problem(422, "Unprocessable Entity", "the agent file: name is missing"),
      `voice-guide refused it: the agent file: name is missing${kept}`,
    ],
    [problem(422, "Unknown voice"), `voice-guide refused it: Unknown voice${kept}`],
    [
      plain(502, "the product's server couldn't reach voice: fetch failed"),
      `Error (502): the product's server couldn't reach voice: fetch failed${kept}`,
    ],
    // The dev server refused it before writing anything.
    [
      plain(409, "an agent file named coach.md exists; give the agent another name"),
      "Error (409): an agent file named coach.md exists; give the agent another name",
    ],
  ];
  for (const [answer, why] of cases) {
    await assert.rejects(publishAgent(settings, devServer(() => answer).fetcher), { message: why });
  }
});
