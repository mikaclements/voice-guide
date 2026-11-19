// What the product's screen shows, as the page hands it to the element: named text blocks that go
// to the agent with the turns. A block voice hasn't saved with a turn as it is now is pending. With
// audio up, each change goes at once as its own `screen` message on the data channel, which voice
// holds for the next turn taken there, spoken, typed or the opening; over HTTP, a typed turn carries
// the pending blocks itself. A connection that drops loses what voice held for it, so every change
// not yet saved goes again on the next.

export type ScreenBlock = { name: string; content: string };

// The blocks a page hands the element, copied, or a TypeError for a list voice could never take.
export function screenBlocks(blocks: readonly ScreenBlock[]): ScreenBlock[] {
  if (!Array.isArray(blocks)) throw new TypeError("screenInput takes a list of { name, content }");
  const names = new Set<string>();
  return blocks.map((b) => {
    if (typeof b?.name !== "string" || typeof b.content !== "string") {
      throw new TypeError("each screenInput block has a name and a content, both text");
    }
    if (names.has(b.name)) throw new TypeError(`screenInput names ${b.name} twice`);
    names.add(b.name);
    return { name: b.name, content: b.content };
  });
}

// What one data channel tells back about the screen messages sent on it: the last whose blocks
// went with a turn voice saved, and one refused, with why. It acts only while its channel is the
// session's, so a connection let go late can't mark another's messages.
export type ScreenReplies = {
  taken(n: number): void;
  refused(n: number, detail: string): void;
  closed(): void;
};

// One data channel's share: how many screen messages went on it, and the block each carried, by
// name, until voice says a turn took it or refused it.
type Channel = {
  send: (msg: string) => void;
  count: number;
  sent: Map<string, { content: string; n: number; refused?: true }>;
};

export class Screen {
  private current = new Map<string, string>();
  // What voice saved with a turn, by name.
  private readonly confirmed = new Map<string, string>();
  private channel: Channel | undefined;
  private readonly onRefused: (message: string) => void;

  constructor(onRefused: (message: string) => void = () => {}) {
    this.onRefused = onRefused;
  }

  // The screen as the page shows it now; a block left out keeps what voice last had of it.
  set(blocks: readonly ScreenBlock[]): void {
    this.current = new Map(blocks.map((b) => [b.name, b.content]));
    this.flush();
  }

  // The blocks voice hasn't saved with a turn as they are now: what an HTTP turn carries.
  pending(): ScreenBlock[] {
    return [...this.current]
      .filter(([name, content]) => content !== this.confirmed.get(name))
      .map(([name, content]) => ({ name, content }));
  }

  // An HTTP turn carrying blocks was saved: its stream starts only then.
  carried(blocks: readonly ScreenBlock[]): void {
    for (const b of blocks) this.confirmed.set(b.name, b.content);
  }

  // A data channel opened: changes go on it from now on, beginning with every one voice hasn't
  // saved with a turn, numbered afresh.
  connect(send: (msg: string) => void): ScreenReplies {
    const channel: Channel = { send, count: 0, sent: new Map() };
    this.channel = channel;
    this.flush();
    const live = () => this.channel === channel;
    return {
      taken: (n) => {
        if (!live()) return;
        for (const [name, s] of channel.sent) {
          if (s.n > n || s.refused) continue;
          this.confirmed.set(name, s.content);
          channel.sent.delete(name);
        }
      },
      refused: (n, detail) => {
        if (!live()) return;
        // Not sent again on this channel until it changes; voice refuses it as it is.
        for (const s of channel.sent.values()) if (s.n === n) s.refused = true;
        this.onRefused(detail);
      },
      closed: () => {
        if (live()) this.channel = undefined;
      },
    };
  }

  // Sends each block changed since it last went on the channel, or since voice saved it, one
  // message each, so voice refuses a block that can't be used without the others.
  private flush(): void {
    const ch = this.channel;
    if (!ch) return;
    for (const [name, content] of this.current) {
      if (content === (ch.sent.get(name)?.content ?? this.confirmed.get(name))) continue;
      const n = ch.count + 1;
      try {
        ch.send(JSON.stringify({ type: "screen", n, screen_input: [{ name, content }] }));
      } catch {
        return; // the channel is closing: what wasn't sent goes on the next
      }
      ch.count = n;
      ch.sent.set(name, { content, n });
    }
  }
}
