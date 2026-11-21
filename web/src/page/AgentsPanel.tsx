import { type FormEvent, useEffect, useState } from "react";
import {
  type AgentFile,
  type AgentFiles,
  listAgentFiles,
  newAgent,
  type Opened,
  openAgentFile,
  publishAgentFile,
  saveAgentFile,
} from "./agents.ts";

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));
const folders = { local: "seed/local/agents/", shipped: "agents/" };

function load(show: (list: AgentFiles) => void, failed: (why: string) => void) {
  listAgentFiles().then(show, (e: unknown) => failed(message(e)));
}

// The agents panel: the agent files with their published versions, each published from its row, a
// new test agent from the template, and one file open to edit and save. The shipped files open
// read-only, so the page never edits a tracked file. onPublished is told after each publish, since
// a first one adds an agent a session can start with.
export function AgentsPanel({ onPublished }: { onPublished?: () => void }) {
  const [list, setList] = useState<AgentFiles>();
  const [name, setName] = useState("");
  const [open, setOpen] = useState<Opened>();
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  // How the last press went: its result line, or what it failed with.
  const [result, setResult] = useState("");
  const [error, setError] = useState("");

  useEffect(() => load(setList, setError), []);

  // One press at a time; a publish, a save or a new agent changes the list, so it is read again.
  async function run(press: () => Promise<string>, changes = true) {
    setBusy(true);
    setResult("");
    setError("");
    try {
      setResult(await press());
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
    if (changes) load(setList, setError);
  }
  async function show(path: string) {
    const opened = await openAgentFile(path);
    setOpen(opened);
    setText(opened.content);
  }

  return (
    <AgentsView
      {...{ list, name, open, text, busy, result, error }}
      on={{
        publish: (path) =>
          run(async () => {
            const line = `${path}: ${await publishAgentFile(path)}`;
            onPublished?.();
            return line;
          }),
        open: (path) => run(() => show(path).then(() => ""), false),
        name: setName,
        create: (e) => {
          e.preventDefault();
          run(async () => {
            const made = await newAgent(name.trim());
            setName("");
            await show(made.path);
            return `made ${folders.local}${made.path}`;
          });
        },
        text: setText,
        save: () =>
          run(async () => {
            if (!open) return "";
            const { modified } = await saveAgentFile(open.path, text, open.modified);
            setOpen({ ...open, content: text, modified });
            return "saved";
          }),
      }}
    />
  );
}

const version = (f: AgentFile) =>
  f.version === "unknown"
    ? "version unknown"
    : f.version === null
      ? "not published"
      : `version ${f.version}`;

export type AgentsViewProps = {
  list?: AgentFiles;
  name: string;
  open?: Opened;
  text: string;
  busy: boolean;
  result: string;
  error: string;
  on: Record<"publish" | "open", (path: string) => void> & {
    name(name: string): void;
    create(e: FormEvent): void;
    text(text: string): void;
    save(): void;
  };
};

export function AgentsView(props: AgentsViewProps) {
  const { list, name, open, text, busy, result, error, on } = props;
  const shipped = open?.folder === "shipped";
  // Publishing sends the file on disk, so the open file's unsaved text would not be what is
  // published; and opening another file or a new agent would throw that text away.
  const unsaved = open !== undefined && text !== open.content;
  return (
    <section>
      <h2>Agents</h2>
      {list?.problem && <div className="error">{list.problem}</div>}
      {!list && !error && <p className="meta">Loading agent files…</p>}
      {list && !list.files.length && <p className="meta">No agent files yet.</p>}
      {list?.files.map((f) => (
        <div className="row" key={f.path}>
          <button
            type="button"
            onClick={() => on.open(f.path)}
            disabled={busy || (unsaved && open?.path !== f.path)}
          >
            {f.name || f.path}
          </button>
          <span className="meta">
            {folders[f.folder]}
            {f.path} · {version(f)}
          </span>
          <button
            type="button"
            onClick={() => on.publish(f.path)}
            disabled={busy || (unsaved && open?.path === f.path)}
          >
            Publish
          </button>
        </div>
      ))}
      <form className="row" onSubmit={on.create}>
        <input
          value={name}
          onChange={(e) => on.name(e.target.value)}
          placeholder="The new agent's name"
          disabled={busy || unsaved}
        />
        <button type="submit" disabled={busy || unsaved || !name.trim()}>
          New agent
        </button>
      </form>
      {open && (
        <>
          <p className="meta">
            {folders[open.folder]}
            {open.path}
            {shipped && ", shipped: read-only here"}
          </p>
          <textarea
            aria-label={open.path}
            value={text}
            onChange={(e) => on.text(e.target.value)}
            readOnly={shipped}
            rows={24}
            style={{ width: "100%", font: "0.85rem/1.4 ui-monospace, monospace" }}
          />
          {!shipped && (
            <div className="row">
              <button type="button" onClick={on.save} disabled={busy || !unsaved}>
                Save
              </button>
              {unsaved && <span className="meta">Save before publishing</span>}
            </div>
          )}
        </>
      )}
      {result && <p className="meta">{result}</p>}
      {error && <div className="error">{error}</div>}
    </section>
  );
}
