// voice and the browser's audio as the drop-in client's tests meet them: each audio offer waits for
// the test to open or fail it, each request is recorded with its credential, a typed turn is
// answered "Hello" and End with the session, unless the test answers them otherwise.
import { type Audio, type AudioOptions, ChannelTurns, type Closing, type listen } from "./audio.ts";

type Level = Parameters<typeof listen>[1];

type Opened = {
  audio: Audio;
  sent: { words: string; request_id: string }[];
  closed: number[];
  muted: boolean[];
  give(given: boolean): void;
};

const hello = () =>
  new Response('event: text\ndata: {"text":"Hello"}\n\nevent: done\ndata: {}\n\n', {
    headers: { "Content-Type": "text/event-stream" },
  });

export function fakeVoice() {
  const offers: {
    ticket: string;
    closing: (why: Closing) => void;
    options?: AudioOptions;
    fail(err: unknown): void;
    open(): Opened;
  }[] = [];
  const requests: string[] = [];
  // Each playback started, with how it tells the session the browser refused to play, and how it
  // passes on a reading of the agent's sound.
  const played: { blocked: (blocked: boolean) => void; level: Level; plays: number }[] = [];
  const v = {
    offers,
    requests,
    played,
    signals: [] as (AbortSignal | undefined)[],
    turn: async (): Promise<Response> => hello(),
    end: async (): Promise<Response> => Response.json({ id: "s1" }),
  };
  const fetcher = (async (url: string, init?: RequestInit) => {
    requests.push(`${init?.method} ${url} ${new Headers(init?.headers).get("Authorization")}`);
    if (url.endsWith("/end")) return v.end();
    v.signals.push(init?.signal ?? undefined);
    return v.turn();
  }) as typeof fetch;
  const open = ((
    _session: string,
    ticket: string,
    closing: (why: Closing) => void,
    options?: AudioOptions,
  ) =>
    new Promise<Audio>((resolve, reject) =>
      offers.push({
        ticket,
        closing,
        options,
        fail: reject,
        open() {
          const sent: Opened["sent"] = [];
          const closed: number[] = [];
          const muted: boolean[] = [];
          let give = (_given: boolean) => {};
          const audio: Audio = {
            turns: new ChannelTurns((m) => sent.push(JSON.parse(m))),
            stream: Promise.resolve({} as MediaStream),
            microphone: {
              stop() {},
              mute: (on) => muted.push(on),
              given: new Promise<boolean>((r) => {
                give = r;
              }),
            },
            // As the real connection does, closing settles the turns still waiting.
            close: async () => {
              closed.push(1);
              audio.turns.closed();
            },
          };
          resolve(audio);
          return { audio, sent, closed, muted, give: (given: boolean) => give(given) };
        },
      }),
    )) as never;
  const play = ((_stream: MediaStream, level: Level, blocked: (b: boolean) => void) => {
    const p = { blocked, level, plays: 0 };
    played.push(p);
    return {
      stop() {},
      mute() {},
      play() {
        p.plays++;
        blocked(false);
      },
    };
  }) as typeof listen;
  return Object.assign(v, { fetcher, open, play });
}
