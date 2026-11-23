import { type FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { at, type Session } from "../client/api.ts";
import { JoinedSession, type ReplyMeta, type Shown } from "../client/joined.ts";
import { AgentsPanel } from "./AgentsPanel.tsx";
import { StayAwake } from "./awake.ts";
import { useFollow } from "./follow.ts";
import { type Agent, listAgents, type StartOptions, startSession } from "./product.ts";
import { SessionsPanel } from "./SessionsPanel.tsx";
import { Starter } from "./starting.ts";

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));
const seconds = (ms: number) => `${(ms / 1000).toFixed(2)}s`;

export type Tab = "test" | "agents";

// The tab a URL's hash names, Test unless it names Agents, so a reload stays on its tab.
export const tabFrom = (hash: string): Tab => (hash === "#agents" ? "agents" : "test");

function useTab(): Tab {
  const [tab, setTab] = useState(() => tabFrom(window.location.hash));
  useEffect(() => {
    const changed = () => setTab(tabFrom(window.location.hash));
    window.addEventListener("hashchange", changed);
    return () => window.removeEventListener("hashchange", changed);
  }, []);
  return tab;
}

// The page's title and its two tabs: Test, to talk to an agent, and Agents, to edit them.
export function Tabs({ tab }: { tab: Tab }) {
  const current = (t: Tab) => (t === tab ? "page" : undefined);
  return (
    <header>
      <h1>voice-guide</h1>
      <nav className="tabs">
        <a href="#test" aria-current={current("test")}>
          Test
        </a>
        <a href="#agents" aria-current={current("agents")}>
          Agents
        </a>
      </nav>
    </header>
  );
}

// voice-guide's own page: it lists the agents and has its dev server start a session, as a
// product's server would, then holds the session as the drop-in element does, with JoinedSession,
// and draws what that shows. Both tabs stay mounted, so a session and the agent form outlive a
// switch between them.
export function App() {
  const tab = useTab();
  const [agents, setAgents] = useState<Agent[]>([]);
  const [agentId, setAgentId] = useState("");
  const [session, setSession] = useState<Session>();
  const [shown, setShown] = useState<Shown>();
  const [words, setWords] = useState("");
  const [starting, setStarting] = useState(false);
  // What listing the agents or a start failed with; the session's own notes are in shown.
  const [error, setError] = useState("");
  // Why the display may turn off during the session, when it may.
  const [wakeNote, setWakeNote] = useState("");
  const joined = useRef<JoinedSession>(undefined);
  // Made at the first session, since the page is also drawn where there is no browser.
  const awake = useRef<StayAwake>(undefined);
  const starter = useRef(new Starter((a, r, o) => startSession(a, r, fetch, o)));

  // Listed again after an agent is published, which may add one or change what it asks for.
  const loadAgents = useCallback(() => {
    listAgents().then(
      (list) => {
        setAgents(list);
        setAgentId((id) => id || list[0]?.id || "");
      },
      (e: unknown) => setError(message(e)),
    );
  }, []);
  useEffect(loadAgents, [loadAgents]);
  // The display stays on from the session's first show, and each change it shows after keeps it on
  // longer, until it is over; one ended idle, which the page hears of only at its next turn, goes
  // quiet, and the display is let go of after a while.
  useEffect(() => {
    if (!shown || shown.over) return;
    awake.current ??= new StayAwake(navigator, document, setWakeNote);
    awake.current.hold();
  }, [shown]);
  // Leaving the page lets the session go, so voice lets its audio connection go at once.
  useEffect(
    () => () => {
      joined.current?.leave();
      awake.current?.letGo();
    },
    [],
  );

  async function start(options?: StartOptions) {
    const agent = agents.find((a) => a.id === agentId);
    if (!agent) return;
    setStarting(true);
    setError("");
    try {
      const { ticket, ...s } = await starter.current.start(agent, options);
      // The session before is let go, not ended: it ends after it sits idle.
      joined.current?.leave();
      joined.current = new JoinedSession({
        session: s.id,
        ticket,
        // voice through the dev server's proxy, on the page's own origin.
        fetcher: at(""),
        show: setShown,
        giveBack: (said) => setWords((w) => (w.trim() ? w : said)),
        // What it shows says the session is over, and why; the display may turn off again.
        ended: () => awake.current?.letGo(),
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
    <main>
      <Tabs tab={tab} />
      <div hidden={tab !== "test"}>
        <SessionsPanel
          {...{ agents, agentId }}
          onAgent={setAgentId}
          started={Boolean(session)}
          busy={starting || Boolean(shown?.busy)}
          start={start}
          error={error}
        >
          <SessionView
            {...{ agents, session, shown, words, starting, error, wakeNote }}
            on={{
              // A failed End shows in the session's notes, and leaves it live to press again.
              end: act(() => joined.current?.end().catch(() => {})),
              mute: act(() => joined.current?.mute()),
              playAfterPress: act(() => joined.current?.playAfterPress()),
              words: setWords,
              send: (e) => {
                e.preventDefault();
                setError("");
                if (joined.current?.send(words)) setWords("");
              },
            }}
          />
        </SessionsPanel>
      </div>
      <div hidden={tab !== "agents"} className="agents">
        <AgentsPanel onPublished={loadAgents} />
      </div>
    </main>
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
  session?: Session;
  shown?: Shown;
  words: string;
  starting: boolean;
  error: string;
  // Why the display may turn off during the session, when it may.
  wakeNote?: string;
  on: Record<"end" | "mute" | "playAfterPress", () => void> & {
    words(words: string): void;
    send(e: FormEvent): void;
  };
};

export function SessionView(props: SessionViewProps) {
  const { agents, session, shown, words, starting, error, wakeNote, on } = props;
  const busy = starting || Boolean(shown?.busy);
  const locked = shown?.locked ?? true;
  const agentName = (id: string) => agents.find((a) => a.id === id)?.name ?? id;
  const bottom = useFollow(shown);
  return (
    <section>
      {shown && !shown.over && (
        <div className="row">
          <button type="button" onClick={on.end} disabled={busy}>
            End
          </button>
        </div>
      )}
      {!agents.length && !error && <p className="meta">Loading agents…</p>}
      {shown?.notes.map((n) => (
        <p className="meta" key={n}>
          {n}
        </p>
      ))}
      {wakeNote && <p className="meta">{wakeNote}</p>}
      {shown?.blocked && (
        <button type="button" onClick={on.playAfterPress}>
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
      {/* The session's bottom, kept in view while the user reads along. */}
      <div ref={bottom} />
    </section>
  );
}
