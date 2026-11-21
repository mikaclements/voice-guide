import { useCallback, useEffect, useState } from "react";
import {
  type Agent,
  contextFor,
  type Document,
  listSessions,
  listVoices,
  type PastSession,
  type StartOptions,
  startOptions,
} from "./product.ts";

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));
// Voice's range for a session's speed.
const slowest = 0.5;
const fastest = 2;

// A time as voice gives it, in UTC and to the second, so the list reads the same anywhere.
const when = (t: string) => `${t.replace("T", " ").slice(0, 19)} UTC`;

export type StartFormProps = {
  voices: string[];
  voice: string;
  speed: string;
  documents: Document[];
  busy: boolean;
  // What loading the voices or the documents failed with, and what the last start failed with.
  errors: string[];
  on: {
    voice(voice: string): void;
    speed(speed: string): void;
    document(name: string, content: string): void;
    start(): void;
  };
};

// The settings a start may carry beyond the agent: the voice and speed it speaks in, and the
// documents of session context, which start as the placeholder user's files.
export function StartForm(props: StartFormProps) {
  const { voices, voice, speed, documents, busy, errors, on } = props;
  const n = Number(speed);
  const badSpeed = speed.trim() !== "" && !(n >= slowest && n <= fastest);
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        on.start();
      }}
    >
      <h2>Start with settings</h2>
      <div className="row">
        <select value={voice} onChange={(e) => on.voice(e.target.value)} disabled={busy}>
          <option value="">The agent's voice</option>
          {voices.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </select>
        <input
          value={speed}
          onChange={(e) => on.speed(e.target.value)}
          placeholder={`Speed ${slowest} to ${fastest}, empty for the agent's`}
          inputMode="decimal"
          disabled={busy}
        />
      </div>
      {badSpeed && (
        <div className="error">
          Speed is a number from {slowest} to {fastest}.
        </div>
      )}
      {errors.filter(Boolean).map((e) => (
        <div className="error" key={e}>
          {e}
        </div>
      ))}
      {documents.map((d) => (
        <label key={d.name}>
          <span className="meta">{d.name}</span>
          <textarea
            value={d.content}
            onChange={(e) => on.document(d.name, e.target.value)}
            rows={4}
            style={{ display: "block", width: "100%", font: "inherit", boxSizing: "border-box" }}
            disabled={busy}
          />
        </label>
      ))}
      <div className="row">
        <button type="submit" disabled={busy || badSpeed}>
          Start with these
        </button>
      </div>
    </form>
  );
}

export type PastSessionsProps = {
  sessions: PastSession[];
  agentName(id: string): string;
  loading: boolean;
  // What listing the sessions failed with.
  error: string;
  refresh(): void;
};

// The placeholder user's past sessions, newest first.
export function PastSessions(props: PastSessionsProps) {
  const { sessions, agentName, loading, error, refresh } = props;
  return (
    <section>
      <h2>Past sessions</h2>
      <div className="row">
        <button type="button" onClick={refresh} disabled={loading}>
          Refresh
        </button>
      </div>
      {error && <div className="error">{error}</div>}
      {!error && !loading && !sessions.length && <p className="meta">No past sessions yet.</p>}
      {sessions.map((s) => (
        <div className="turn" key={s.id}>
          <span className="who">{agentName(s.agent_id)}</span>
          <span className="meta">
            started {when(s.started_at)}
            {s.ended_at ? `, ended ${when(s.ended_at)}` : ", still live"}
            {s.summary_status && ` · summary ${s.summary_status}`}
          </span>
          {s.summary && <div>{s.summary}</div>}
        </div>
      ))}
    </section>
  );
}

// The sessions panel: it loads the voices, the documents the chosen agent asks for and the past
// sessions from the page's dev server, and hands a start with the settings to the page's own start.
export function SessionsPanel(props: {
  agents: Agent[];
  agentId: string;
  busy: boolean;
  start(options: StartOptions): void;
  // What the last start failed with, shown here beside the form that sent it.
  error: string;
}) {
  const { agents, agentId, busy, start, error } = props;
  const [voices, setVoices] = useState<string[]>([]);
  const [voice, setVoice] = useState("");
  const [speed, setSpeed] = useState("");
  const [documents, setDocuments] = useState<Document[]>([]);
  const [voicesError, setVoicesError] = useState("");
  const [documentsError, setDocumentsError] = useState("");
  const [sessions, setSessions] = useState<PastSession[]>([]);
  const [loading, setLoading] = useState(true);
  const [listError, setListError] = useState("");

  const refresh = useCallback(() => {
    setLoading(true);
    listSessions()
      .then(
        (list) => {
          setSessions(list);
          setListError("");
        },
        (e: unknown) => setListError(message(e)),
      )
      .finally(() => setLoading(false));
  }, []);

  useEffect(refresh, [refresh]);
  useEffect(() => {
    listVoices().then(setVoices, (e: unknown) => setVoicesError(message(e)));
  }, []);
  // The documents are the agent's to ask for, so choosing another agent shows its own, and so does
  // a new version of it that asks for others.
  const asks = agents.find((a) => a.id === agentId)?.session_context.join("\n");
  useEffect(() => {
    if (!agentId) return;
    let current = true;
    setDocuments([]);
    contextFor(agentId).then(
      (docs) => {
        if (!current) return;
        setDocuments(docs);
        setDocumentsError("");
      },
      (e: unknown) => current && setDocumentsError(message(e)),
    );
    return () => {
      current = false;
    };
  }, [agentId, asks]);

  return (
    <>
      <StartForm
        {...{ voices, voice, speed, documents, busy, errors: [voicesError, documentsError, error] }}
        on={{
          voice: setVoice,
          speed: setSpeed,
          document: (name, content) =>
            setDocuments((docs) => docs.map((d) => (d.name === name ? { ...d, content } : d))),
          start: () => start(startOptions(voice, speed, documents)),
        }}
      />
      <PastSessions
        {...{ sessions, loading, error: listError, refresh }}
        agentName={(id) => agents.find((a) => a.id === id)?.name ?? id}
      />
    </>
  );
}
