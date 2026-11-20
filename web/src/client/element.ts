// <voice-guide-session>, the element the drop-in client defines, which a product puts on its own
// page so its user can talk to an agent. The product's server starts the session with its product
// key and hands the page the session's ID and ticket, which the page passes to join(); the element
// never holds a product key and never starts a session. A thin shell: what it does and shows comes
// from JoinedSession, and this draws it and turns calls and presses into its calls.
import { at } from "./api.ts";
import type { openAudio, playAgent } from "./audio.ts";
import { JoinedSession, type Shown } from "./joined.ts";
import { type ScreenBlock, screenBlocks } from "./screen.ts";

// A plain default a product restyles with the custom properties and parts. The box is drawn inside,
// on the frame, where the product's own rules can't reach: a page rule matching the host, as a
// reset of every element's padding and border does, outranks the host's own. The frame inherits
// nothing from the page, so only what the product sets on purpose reaches inside.
export const css = `:host { display: block; }
:host([hidden]), [hidden] { display: none !important; }
[part~="frame"] { all: initial; display: block; box-sizing: border-box; padding: 1rem;
  font: var(--voice-guide-font, 16px/1.5 system-ui, sans-serif);
  color: var(--voice-guide-color, #222); background: var(--voice-guide-background, #fff);
  border: var(--voice-guide-border, 1px solid #ddd); border-radius: var(--voice-guide-radius, 8px); }
[part~="transcript"] { max-height: var(--voice-guide-height, 24rem); overflow-y: auto; }
[part~="message"] { margin: 0.5rem 0; white-space: pre-wrap; }
[part~="who"] { font-weight: 600; margin-right: 0.4rem; }
[part~="status"], [part~="note"] { margin: 0.5rem 0; font-size: 0.875em;
  color: var(--voice-guide-quiet-color, #666); }
form { display: flex; flex-wrap: wrap; gap: 0.5rem; margin-top: 0.75rem; }
input { flex: 1; min-width: 8rem; font: inherit; color: inherit; padding: 0.4rem 0.6rem;
  border: var(--voice-guide-border, 1px solid #ddd); border-radius: var(--voice-guide-radius, 8px); }
button { font: inherit; padding: 0.4rem 1rem; border: 0; cursor: pointer;
  border-radius: var(--voice-guide-radius, 8px);
  background: var(--voice-guide-accent, #2b5fd9); color: var(--voice-guide-accent-text, #fff); }
button:disabled { opacity: 0.5; cursor: default; }`;

// How near the end, in pixels, still counts as at the bottom, so a transcript scrolled to the end
// keeps following though a layout in fractions of a pixel leaves it a little short: a round guess,
// a few pixels, to be checked by hand on the demo page at 100% and 125% zoom before the element
// is offered to a product outside this repository.
const bottomSlack = 4;

// Said once a ticket is set where page recorders copy it from; the element takes it only by a call.
export const ticketAttributeWarning =
  "voice-guide-session: the ticket goes to join({ session, ticket }), never an attribute, where page recorders copy it; it was removed";

// What the element joins, if anything: the session and ticket handed to it, once it is on the page.
// Voice is at the api attribute, else where the element's script came from, else the page's origin.
// The key names the join: the same key keeps the session the element has.
export function wanted(
  onPage: boolean,
  given: { session: string; ticket: string } | undefined,
  api: string | null,
  loadedFrom: string,
): { session: string; ticket: string; api: string; key: string } | undefined {
  const session = given?.session.trim();
  const ticket = given?.ticket.trim();
  if (!onPage || !session || !ticket) return undefined;
  const base = (api?.trim() || loadedFrom).replace(/\/+$/, "");
  return { session, ticket, api: base, key: `${base} ${session} ${ticket}` };
}

// Defines <voice-guide-session>, once, however often the script is loaded; open and play reach
// the browser's audio unless a test passes its own.
export function define(open?: typeof openAudio, play?: typeof playAgent): void {
  if (customElements.get("voice-guide-session")) return;
  // Where this script came from: voice serves it, so that is where voice is. A script with no
  // origin of its own, loaded from a data: or blob: address, calls the page's.
  const loadedFrom = new URL(import.meta.url).origin.replace(/^null$/, "");

  class VoiceGuideSession extends HTMLElement {
    static observedAttributes = ["api", "ticket"];
    // Closed, so other scripts on the page can't read the transcript or what the user types.
    readonly #root = this.attachShadow({ mode: "closed" });
    readonly #parts = new Map<string, HTMLElement>();
    #given: { session: string; ticket: string } | undefined;
    #joined: JoinedSession | undefined;
    #key = "";
    #syncing: Promise<void> | undefined;
    #screen: ScreenBlock[] = [];

    constructor() {
      super();
      const named = (tag: string, part: string, text = "") => {
        const el = this.#make(tag, part, text);
        this.#parts.set(part, el);
        return el;
      };
      const style = document.createElement("style");
      style.textContent = css;
      const form = named("form", "form");
      form.append(
        named("input", "input"),
        named("button", "send", "Send"),
        named("button", "mute"),
        named("button", "end", "End"),
      );
      const frame = named("div", "frame");
      frame.append(
        named("p", "status"),
        named("button", "hear", "Tap to hear the agent"),
        named("div", "transcript"),
        named("div", "notes"),
        form,
      );
      this.#root.append(style, frame);
      this.#part("transcript").setAttribute("role", "log");
      this.#part("notes").setAttribute("role", "status");
      this.#part("input").setAttribute("aria-label", "Message");
      for (const name of ["hear", "mute", "end"]) this.#part(name).setAttribute("type", "button");
      form.addEventListener("submit", (e) => {
        e.preventDefault();
        const input = this.#input();
        if (this.#joined?.send(input.value)) input.value = "";
      });
      this.#part("hear").addEventListener("click", () => this.#joined?.playAfterPress());
      this.#part("mute").addEventListener("click", () => this.#joined?.mute());
      this.#part("end").addEventListener("click", () => this.end().catch(() => {}));
      this.#draw(undefined);
    }

    // The session the element holds, or "": not a secret, unlike its ticket.
    get session(): string {
      return this.#given?.session ?? "";
    }

    // What the product's screen shows, as named text blocks: each one changed goes to the agent with
    // the user's next turn, spoken or typed, or the agent's opening, and carries over to a session
    // joined later.
    get screenInput(): ScreenBlock[] {
      return this.#screen.map((b) => ({ ...b }));
    }
    set screenInput(blocks: ScreenBlock[]) {
      this.#screen = screenBlocks(blocks);
      this.#joined?.showScreen(this.#screen);
    }

    // Joins the session the product's server started, with the ticket its start answered with,
    // letting go of any session before without ending it.
    join(given: { session: string; ticket: string }): void {
      if (!given?.session || !given.ticket) {
        throw new TypeError("join takes the session ID and ticket a start answered with");
      }
      this.#given = { session: given.session, ticket: given.ticket };
      this.#sync();
    }

    // Ends the session, as End does, once a join just asked for is under way; resolves once voice
    // has ended it, and rejects when End didn't reach voice, leaving the session live.
    async end(): Promise<void> {
      await this.#syncing;
      return this.#joined?.end();
    }

    connectedCallback() {
      this.#sync();
    }
    disconnectedCallback() {
      this.#sync();
    }
    attributeChangedCallback(name: string, _old: string | null, value: string | null) {
      if (name === "ticket") {
        if (value === null) return;
        this.removeAttribute("ticket");
        console.warn(ticketAttributeWarning);
        return;
      }
      this.#sync();
    }

    // Waits for the current task to finish, then joins once. A join and a move on the page, or two
    // joins, in the same task count as one: the last wins.
    #sync() {
      this.#syncing ??= Promise.resolve().then(() => {
        this.#syncing = undefined;
        const join = wanted(this.isConnected, this.#given, this.getAttribute("api"), loadedFrom);
        if ((join?.key ?? "") === this.#key) return;
        this.#joined?.leave();
        this.#joined = undefined;
        this.#key = join?.key ?? "";
        this.#input().value = "";
        this.#draw(undefined);
        if (!join) return;
        const emit = (type: string, detail: object) =>
          this.dispatchEvent(
            new CustomEvent(type, {
              detail: { ...detail, session: join.session },
              bubbles: true,
              composed: true,
            }),
          );
        const joined = new JoinedSession(
          {
            session: join.session,
            ticket: join.ticket,
            fetcher: at(join.api),
            show: (shown) => this.#draw(shown),
            giveBack: (words) => {
              const input = this.#input();
              if (!input.value.trim()) input.value = words;
            },
            ended: (reason) => emit("voice-guide-ended", { reason }),
            refused: ({ message, status, type }) =>
              emit("voice-guide-error", { message, status, type }),
            screenRefused: (refusal) => emit("voice-guide-screen-refused", refusal),
          },
          open,
          play,
        );
        joined.showScreen(this.#screen);
        this.#joined = joined;
        joined.join(window.isSecureContext);
      });
    }

    #draw(shown: Shown | undefined) {
      const live = shown !== undefined && !shown.over;
      const status = this.#part("status");
      status.textContent = shown?.blocked ? "" : (shown?.status ?? "");
      status.hidden = !status.textContent;
      this.#part("hear").hidden = !shown?.blocked;
      const transcript = this.#part("transcript");
      // Checked before the new words go in: a user at the end follows the conversation, and one
      // who scrolled up to read stays put until they scroll back down.
      const following =
        transcript.scrollHeight - transcript.scrollTop - transcript.clientHeight <= bottomSlack;
      transcript.replaceChildren(
        ...(shown?.lines ?? []).map((l) => {
          const line = this.#make("div", `message ${l.who}`);
          line.append(this.#make("span", "who", l.who === "you" ? "You" : "Agent"), l.text);
          return line;
        }),
      );
      if (following) transcript.scrollTop = transcript.scrollHeight;
      const notes = [...(shown?.notes ?? []), shown?.over ?? ""].filter(Boolean);
      this.#part("notes").replaceChildren(...notes.map((n) => this.#make("p", "note", n)));
      this.#part("form").hidden = !live;
      (this.#part("send") as HTMLButtonElement).disabled = !live || shown.locked;
      const mute = this.#part("mute") as HTMLButtonElement;
      mute.textContent = shown?.mute ?? "";
      mute.hidden = !mute.textContent;
      (this.#part("end") as HTMLButtonElement).disabled = !live || shown.busy;
    }

    #make(tag: string, part: string, text = "") {
      const el = document.createElement(tag);
      el.setAttribute("part", part);
      el.textContent = text;
      return el;
    }

    #part(name: string): HTMLElement {
      return this.#parts.get(name) as HTMLElement;
    }

    #input(): HTMLInputElement {
      return this.#part("input") as HTMLInputElement;
    }
  }

  customElements.define("voice-guide-session", VoiceGuideSession);
}
