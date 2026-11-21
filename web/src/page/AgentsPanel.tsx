import { type ChangeEvent, type ReactNode, useEffect, useState } from "react";
import {
  type AgentForm,
  agentModels,
  cloneForm,
  droppedDocuments,
  formFrom,
  isSpeed,
  modelChoice,
  newForm,
  settingsFrom,
  summaryModels,
} from "./agentForm.ts";
import {
  type AgentFile,
  type AgentFiles,
  type AgentVersion,
  listAgentFiles,
  publishAgent,
  publishAgentFile,
  readAgent,
} from "./agents.ts";
import { listVoices } from "./product.ts";

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));
const folders = { local: "seed/local/agents/", shipped: "agents/" };
// What the dropdown picks for a new agent; a file's name always ends in .md, so none is this.
export const newAgent = "new";
// A time as voice gives it, in UTC and to the second, so it reads the same anywhere.
const when = (t: string) => `${t.replace("T", " ").slice(0, 19)} UTC`;

// The newest version voice holds of the picked agent: null when it has none.
type Published = { version: number; published_at: string } | null;

// The Agents tab: a dropdown of every agent file, and a form of the picked agent's settings as voice
// holds them, which Publish writes to the agent's file and publishes. A shipped agent shows
// read-only, to clone into a test agent, so the page never edits a tracked file. onPublished is told
// after each publish, since a first one adds an agent a session can start with.
export function AgentsPanel({ onPublished }: { onPublished?: () => void }) {
  const [list, setList] = useState<AgentFiles>();
  const [voices, setVoices] = useState<string[]>([]);
  const [picked, setPicked] = useState("");
  // The form as it was filled, and as edited since.
  const [base, setBase] = useState<AgentForm>();
  const [form, setForm] = useState<AgentForm>();
  // Undefined until voice has said.
  const [published, setPublished] = useState<Published>();
  const [busy, setBusy] = useState(false);
  // How the last press went: its result line, or what it failed with.
  const [result, setResult] = useState("");
  const [error, setError] = useState("");

  async function loadList() {
    try {
      const loaded = await listAgentFiles();
      setList(loaded);
      return loaded;
    } catch (e) {
      setError(message(e));
    }
  }
  useEffect(() => {
    loadList();
    // Without the list the voice field still shows the agent's own voice.
    listVoices().then(setVoices, () => {});
  }, []);

  // Counts each time the form is filled from outside the fields, so a field's own choices, such as
  // Other for a model, start afresh with what was filled.
  const [fills, setFills] = useState(0);
  function show(filled: AgentForm | undefined, voiceHolds: Published | undefined) {
    setFills((n) => n + 1);
    setBase(filled);
    setForm(filled);
    setPublished(voiceHolds);
  }
  const fill = (agent: AgentVersion | null) => show(agent ? formFrom(agent) : undefined, agent);
  // One press at a time; what it failed with shows beside the form, which keeps its edits.
  async function run(press: () => Promise<string>) {
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
  }
  const file = list?.files.find((f) => f.path === picked);

  return (
    <AgentsView
      {...{ list, voices, picked, form, published, busy, result, error, fills }}
      dirty={JSON.stringify(form) !== JSON.stringify(base)}
      on={{
        pick: (path) => {
          setPicked(path);
          show(undefined, undefined);
          if (path === newAgent) {
            setResult("");
            setError("");
            return show(newForm(), null);
          }
          const f = list?.files.find((x) => x.path === path);
          if (!f) return;
          run(async () => {
            fill(await readAgent(f.id));
            return "";
          });
        },
        form: setForm,
        publish: () =>
          run(async () => {
            const settings = form && settingsFrom(form);
            if (typeof settings === "string") throw new Error(settings);
            if (!settings) return "";
            try {
              const { line, agent } = await publishAgent({ ...settings, modified: file?.modified });
              fill(agent);
              onPublished?.();
              return line;
            } finally {
              // The file is written before voice is asked, so it changed even when voice refused,
              // and a new agent's first try made it, which the agent is picked as from now on.
              const id = settings.id.toLowerCase();
              const written = (await loadList())?.files.find(
                (f) => f.folder === "local" && f.id.toLowerCase() === id,
              );
              if (written) setPicked(written.path);
            }
          }),
        // Refilled from voice, so a refused publish's edits give way to what voice holds.
        revert: () =>
          run(async () => {
            if (file) fill(await readAgent(file.id));
            else show(base, null);
            return "";
          }),
        clone: () => {
          if (!form) return;
          setPicked(newAgent);
          show(cloneForm(form), null);
          setResult("");
        },
        publishFile: () =>
          run(async () => {
            if (!file) return "";
            const line = await publishAgentFile(file.path);
            fill(await readAgent(file.id));
            onPublished?.();
            await loadList();
            return line;
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
  voices: string[];
  // The picked file's name, newAgent, or empty before one is picked.
  picked: string;
  form?: AgentForm;
  // Whether the form holds edits not yet published.
  dirty: boolean;
  // How many times the form was filled from outside its fields.
  fills?: number;
  published?: Published;
  busy: boolean;
  result: string;
  error: string;
  on: Record<"publish" | "revert" | "clone" | "publishFile", () => void> & {
    pick(path: string): void;
    form(form: AgentForm): void;
  };
};

export function AgentsView(props: AgentsViewProps) {
  const { list, voices, picked, form, dirty, fills, published, busy, result, error, on } = props;
  const file = list?.files.find((f) => f.path === picked);
  const shipped = file?.folder === "shipped";
  return (
    <section>
      {list?.problem && <div className="error">{list.problem}</div>}
      <div className="row">
        <select
          aria-label="Agent"
          value={picked}
          onChange={(e) => on.pick(e.target.value)}
          disabled={busy || dirty}
        >
          <option value="" disabled>
            Pick an agent
          </option>
          {list?.files.map((f) => (
            <option key={f.path} value={f.path}>
              {`${f.name || f.path} · ${f.folder === "shipped" ? "shipped, " : ""}${version(f)}`}
            </option>
          ))}
          <option value={newAgent}>New agent</option>
        </select>
      </div>
      {dirty && <p className="meta">Publish or Revert the edits to pick another agent.</p>}
      {file && (
        <p className="meta">
          {`${folders[file.folder]}${file.path}`}
          {file.same_id_as.length > 0 &&
            ` · same id as ${file.same_id_as.map((o) => folders[o.folder] + o.path).join(", ")}, so neither is published until one gets a new id`}
        </p>
      )}
      {published && (
        <p>{`Version ${published.version} is published, since ${when(published.published_at)}.`}</p>
      )}
      {published === null && (
        <p>
          {!form
            ? "Not published yet, so voice-guide has nothing to fill the form from: publish the file as it is first."
            : picked === newAgent
              ? `Not published yet: Publish writes its file in ${folders.local} and version 1.`
              : "Not published yet: Publish writes the form to its file and publishes version 1."}
        </p>
      )}
      {published &&
        file?.folder === "local" &&
        file.modified > Date.parse(published.published_at) && (
          <p className="meta">
            {`The file changed after version ${published.version} was published: to keep edits made there, use Publish the file as it is, or Publish overwrites them.`}
          </p>
        )}
      {form && shipped && (
        <p>
          <strong>Shipped agent: read-only here.</strong> Clone it to make your own.
        </p>
      )}
      {form && (
        <Fields
          key={fills}
          form={form}
          voices={voices}
          readOnly={shipped}
          busy={busy}
          on={on.form}
        />
      )}
      {(form || file) && (
        <div className="row">
          {form && !shipped && (
            <>
              <button type="button" onClick={on.publish} disabled={busy}>
                Publish
              </button>
              <button type="button" onClick={on.revert} disabled={busy}>
                Revert
              </button>
            </>
          )}
          {form && file && (
            <button type="button" onClick={on.clone} disabled={busy || dirty}>
              Clone
            </button>
          )}
          {file && (
            <button type="button" onClick={on.publishFile} disabled={busy || dirty}>
              Publish the file as it is
            </button>
          )}
        </div>
      )}
      {result && <p className="meta">{result}</p>}
      {error && <div className="error">{error}</div>}
    </section>
  );
}

// One labelled row of the form.
function Row({ id, label, children }: { id: string; label: string; children: ReactNode }) {
  return (
    <>
      <label htmlFor={id}>{label}</label>
      {children}
    </>
  );
}

// The agent's settings, one field each, in the agent file's order, the persona last.
function Fields(props: {
  form: AgentForm;
  voices: string[];
  readOnly: boolean;
  busy: boolean;
  on(form: AgentForm): void;
}) {
  const { form, voices, readOnly, busy, on } = props;
  type Field = HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement;
  const field = (key: Exclude<keyof AgentForm, "summaryDocuments">) => ({
    id: `agent-${key}`,
    value: form[key],
    onChange: (e: ChangeEvent<Field>) => on({ ...form, [key]: e.target.value }),
  });
  // Typed as text, so a half-typed number is said rather than read as empty, the server's speed.
  const badSpeed = form.speed.trim() !== "" && !isSpeed(Number(form.speed));
  // The agent's own voice stays a choice when voice-guide's list lacks it or couldn't be read.
  const offered = [...new Set([form.voice, ...voices])].filter(Boolean);
  const context = form.sessionContext
    .split(",")
    .map((n) => n.trim())
    .filter(Boolean);
  const dropped = droppedDocuments(form);
  const check = (name: string, checked: boolean) =>
    on({
      ...form,
      summaryDocuments: checked
        ? [...form.summaryDocuments, name]
        : form.summaryDocuments.filter((d) => d !== name),
    });
  return (
    // A read-only form disables every field, so none looks editable.
    <fieldset className="fields" disabled={busy || readOnly}>
      <Row id="agent-name" label="Name">
        <input {...field("name")} />
      </Row>
      <Row id="agent-description" label="Description">
        <textarea {...field("description")} rows={2} />
      </Row>
      <Row id="agent-reads" label="Reads">
        <input {...field("reads")} placeholder="Context kinds, comma-separated" />
      </Row>
      <Row id="agent-sessionContext" label="Session context">
        <input
          {...field("sessionContext")}
          placeholder="Documents a start sends, comma-separated"
        />
      </Row>
      <fieldset className="group">
        <legend>Summary</legend>
        <Row id="agent-summaryInstruction" label="Instruction">
          <textarea {...field("summaryInstruction")} rows={4} placeholder="Empty for no summary" />
        </Row>
        <Row id="agent-summaryModel" label="Model">
          <ModelField
            id="agent-summaryModel"
            value={form.summaryModel}
            listed={summaryModels}
            empty="The agent's model"
            label="The summary's model name"
            on={(summaryModel) => on({ ...form, summaryModel })}
          />
        </Row>
        <span className="label">Documents</span>
        <div className="choices">
          {context.map((name) => (
            <label key={name}>
              <input
                type="checkbox"
                checked={form.summaryDocuments.includes(name)}
                onChange={(e) => check(name, e.target.checked)}
              />
              {name}
            </label>
          ))}
          {!context.length && <span className="meta">No session context names to read</span>}
        </div>
        {dropped.length > 0 && (
          <p className="wide meta">
            {`Dropped, as no longer session context names: ${dropped.join(", ")}`}
          </p>
        )}
        <Row id="agent-summaryOnly" label="Summary only">
          <input
            {...field("summaryOnly")}
            placeholder="Documents the agent never sees, comma-separated"
          />
        </Row>
        <Row id="agent-summarySchema" label="Schema">
          <textarea
            {...field("summarySchema")}
            className="code"
            rows={5}
            placeholder="A JSON Schema object, empty for none"
          />
        </Row>
      </fieldset>
      <Row id="agent-opens" label="Opens">
        <select {...field("opens")}>
          <option value="user">The user speaks first</option>
          <option value="agent">The agent speaks first</option>
        </select>
      </Row>
      <Row id="agent-model" label="Model">
        <ModelField
          id="agent-model"
          value={form.model}
          listed={agentModels}
          empty="The server's model"
          label="The agent's model name"
          on={(model) => on({ ...form, model })}
        />
      </Row>
      <Row id="agent-voice" label="Voice">
        <select {...field("voice")}>
          <option value="">The server's</option>
          {offered.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </select>
      </Row>
      <Row id="agent-speed" label="Speed">
        <input
          {...field("speed")}
          inputMode="decimal"
          placeholder="0.5 to 2, empty for the server's"
        />
      </Row>
      {badSpeed && <p className="wide error">Speed is a number from 0.5 to 2.</p>}
      <label className="wide" htmlFor="agent-persona">
        Persona
      </label>
      <textarea {...field("persona")} className="wide" rows={18} />
    </fieldset>
  );
}

// A model by name: one of listed, the default when empty, or Other, which shows a field for any name.
function ModelField(props: {
  id: string;
  value: string;
  listed: string[];
  empty: string;
  // What the field for another name is called, to a screen reader.
  label: string;
  on(value: string): void;
}) {
  const { id, value, listed, empty, label, on } = props;
  const [other, setOther] = useState(false);
  const choice = modelChoice(listed, value, other);
  return (
    <div className="model">
      <select
        id={id}
        value={choice}
        onChange={(e) => {
          setOther(e.target.value === "other");
          if (e.target.value !== "other") on(e.target.value);
        }}
      >
        <option value="">{empty}</option>
        {listed.map((m) => (
          <option key={m} value={m}>
            {m}
          </option>
        ))}
        <option value="other">Other…</option>
      </select>
      {choice === "other" && (
        <input
          aria-label={label}
          value={value}
          onChange={(e) => on(e.target.value)}
          placeholder="Any model name voice-guide accepts"
        />
      )}
    </div>
  );
}
