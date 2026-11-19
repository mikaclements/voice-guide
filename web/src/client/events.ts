// Reads a server-sent events stream. A turn's reply comes back this way on the request that sent
// it, read with fetch, because the browser's EventSource can't send the ticket header.

export type ServerEvent = { event: string; data: string };

// Splits a stream into events as its chunks arrive. A chunk may end anywhere, even mid-line, so
// the unfinished line waits for the next one.
export class EventParser {
  private buffer = "";
  private event = "";
  private data: string[] = [];

  push(chunk: string): ServerEvent[] {
    this.buffer += chunk;
    const out: ServerEvent[] = [];
    for (;;) {
      const end = this.buffer.search(/\r\n|\r|\n/);
      // A lone \r at the end may be the first half of \r\n, so it waits for the next chunk.
      if (end < 0 || (this.buffer[end] === "\r" && end === this.buffer.length - 1)) break;
      const line = this.buffer.slice(0, end);
      this.buffer = this.buffer.slice(this.buffer.startsWith("\r\n", end) ? end + 2 : end + 1);
      const ev = this.line(line);
      if (ev) out.push(ev);
    }
    return out;
  }

  private line(line: string): ServerEvent | undefined {
    if (line === "") {
      // A blank line ends an event; one without data is no event, as the standard says.
      const ev = this.data.length
        ? { event: this.event || "message", data: this.data.join("\n") }
        : undefined;
      this.event = "";
      this.data = [];
      return ev;
    }
    // A comment, a line starting with a colon, has an empty field name, so it is skipped below.
    const colon = line.indexOf(":");
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    if (field === "event") this.event = value;
    else if (field === "data") this.data.push(value);
    return undefined;
  }
}
