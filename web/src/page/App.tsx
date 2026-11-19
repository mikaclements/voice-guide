import { type FormEvent, useEffect, useRef, useState } from "react";
import { type Agent, listAgents, type Session, startSession } from "../client/api.ts";
import { at, JoinedSession, type ReplyMeta, type Shown } from "../client/joined.ts";
import { Starter } from "./starting.ts";

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));
const seconds = (ms: number) => `${(ms / 1000).toFixed(2)}s`;

// voice-guide's own page: it lists the agents and has its dev server start a session, as a
// product's server would, then holds the session as the drop-in element does, with JoinedSession,
// and draws what that shows.
export function App() {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [agentId, setAgentId] = useState("");
  const [session, setSession] = useState<Session>();
  const [shown, setShown] = useState<Shown>();
  const [words, setWords] = useState("");
  const [starting, setStarting] = useState(false);
  // What listing the agents or a start failed with; the session's own notes are in shown.
  const [error, setError] = useState("");
  const joined = useRef<JoinedSession>(undefined);
  const starter = useRef(new Starter(startSession));

  useEffect(() => {
    listAgents().then(
      (list) => {
        setAgents(list);
        setAgentId((id) => id || list[0]?.id || "");
      },
      (e: unknown) => setError(message(e)),
    );
  }, []);
  // Leaving the page lets the session go, so voice lets its audio connection go at once.
  useEffect(() => () => joined.current?.leave(), []);

  async function start() {
    const agent = agents.find((a) => a.id === agentId);
    if (!agent) return;
    setStarting(true);
    setError("");
    try {
      const { ticket, ...s } = await starter.current.start(agent);
      // The session before is let go, not ended: it ends after it sits idle.
      joined.current?.leave();
      joined.current = new JoinedSession({
        session: s.id,
        ticket,
        agent: agent.name,
        // voice through the dev server's proxy, on the page's own origin.
        fetcher: at(""),
        show: setShown,
        giveBack: (said) => setWords((w) => (w.trim() ? w : said)),
        // What it shows says the session is over, and why.
        ended: () => {},
        refused: () => {},
      });
      setSession(s);
      setWords("");
      joined.current.join(window.isSecureContext);
    } catch (e) {
      setError(message(e));
    } finally {
      setStarting(false);
    }
  }

  // A start that failed is told until the user carries on in the session they have.
  const act = (then: () => unknown) => () => {
    setError("");
    then();
  };
  return (
    <SessionView
      {...{ agents, agentId, session, shown, words, starting, error }}
      on={{
        agent: setAgentId,
        start,
        // A failed End shows in the session's notes, and leaves it live to press again.
        end: act(() => joined.current?.end().catch(() => {})),
        mute: act(() => joined.current?.mute()),
        hear: act(() => joined.current?.hear()),
        words: setWords,
        send: (e) => {
          e.preventDefault();
          setError("");
          if (joined.current?.send(words)) setWords("");
        },
      }}
    />
  );
}

// What the page says beneath a reply.
function beneath(m: ReplyMeta): string {
  return [
    // What answered, so a fake model's fixed reply isn't taken for a broken agent.
    m.model,
    m.firstTextMs !== undefined && `first text ${seconds(m.firstTextMs)}`,
    m.firstSoundMs !== undefined && `first sound ${seconds(m.firstSoundMs)}`,
    m.finishedToSoundMs !== undefined &&
      `finished speaking to first sound ${seconds(m.finishedToSoundMs)}`,
    m.stoppedMs !== undefined && `stopped ${Math.round(m.stoppedMs)} ms after sending`,
    m.lost && "the reply was lost",
    m.audioFailed && "the audio stopped",
  ]
    .filter(Boolean)
    .join(" · ");
}

// What the page draws, and the presses it hands on.
export type SessionViewProps = {
  agents: Agent[];
  agentId: string;
  session?: Session;
  shown?: Shown;
  words: string;
  starting: boolean;
  error: string;
  on: Record<"start" | "end" | "mute" | "hear", () => void> & {
    agent(id: string): void;
    words(words: string): void;
    send(e: FormEvent): void;
  };
};

export function SessionView(props: SessionViewProps) {
  const { agents, agentId, session, shown, words, starting, error, on } = props;
  const busy = starting || Boolean(shown?.busy);
  const locked = shown?.locked ?? true;
  const agentName = (id: string) => agents.find((a) => a.id === id)?.name ?? id;
  return (
    <main>
      <h1>voice-guide</h1>
      <div className="row">
        <select value={agentId} onChange={(e) => on.agent(e.target.value)} disabled={busy}>
          {agents.map((a) => (
            <option key={a.id} value={a.id}>
              {a.name}
            </option>
          ))}
        </select>
        <button type="button" onClick={on.start} disabled={busy || !agentId}>
          {session ? "Start again" : "Start"}
        </button>
        {shown && !shown.over && (
          <button type="button" onClick={on.end} disabled={busy}>
            End
          </button>
        )}
      </div>
      {!agents.length && !error && <p className="meta">Loading agents…</p>}
      {error && <div className="error">{error}</div>}
      {shown?.notes.map((n) => (
        <p className="meta" key={n}>
          {n}
        </p>
      ))}
      {shown?.blocked && (
        <button type="button" onClick={on.hear}>
          Tap to hear the agent
        </button>
      )}
      {shown?.mute && (
        <div className="row">
          {!shown.blocked && <span className="meta">{shown.status}</span>}
          <button type="button" onClick={on.mute}>
            {shown.mute}
          </button>
        </div>
      )}
      {session && (
        <p className="meta">
          Session {session.id} with {agentName(session.agent_id)}, version {session.agent_version}
          {shown?.over && " (ended)"}
        </p>
      )}
      {shown?.over && <p className="meta">{shown.over}</p>}
      {shown?.lines.map((l) => (
        <div className="turn" key={l.id}>
          <span className="who">
            {l.who === "you" ? "You" : agentName(session?.agent_id ?? "")}
          </span>
          {l.text}
          {l.meta && <div className="meta">{beneath(l.meta)}</div>}
        </div>
      ))}
      {session && (
        <form className="row" onSubmit={on.send}>
          <input
            value={words}
            onChange={(e) => on.words(e.target.value)}
            placeholder="Hi, I'm ready. Please start the interview."
            disabled={locked}
          />
          <button type="submit" disabled={locked || !words.trim()}>
            Send
          </button>
        </form>
      )}
    </main>
  );
}
