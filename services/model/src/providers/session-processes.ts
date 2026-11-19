// The session processes kept between turns: at most `max`, the least recently used closed first,
// each closed after `idleMs` unused. A process is here only while idle: a reply takes it out and
// puts it back once answered, so a running reply's process is never evicted or shared.
export class SessionProcesses<P extends { close(): void }> {
  private readonly idle = new Map<string, { process: P; timer: NodeJS.Timeout }>();

  private readonly max: number;
  private readonly idleMs: number;

  constructor(max: number, idleMs: number) {
    this.max = max;
    this.idleMs = idleMs;
  }

  take(session: string): P | undefined {
    const entry = this.idle.get(session);
    if (!entry) return undefined;
    this.idle.delete(session);
    clearTimeout(entry.timer);
    return entry.process;
  }

  put(session: string, process: P) {
    this.take(session)?.close();
    if (this.max === 0) return process.close();
    while (this.idle.size >= this.max) {
      const oldest = this.idle.keys().next().value as string;
      this.take(oldest)?.close();
    }
    // Unref'd, so a waiting timer never keeps the service alive.
    const timer = setTimeout(() => this.take(session)?.close(), this.idleMs).unref();
    this.idle.set(session, { process, timer });
  }

  // Whether another process can be kept without closing one.
  hasRoom(): boolean {
    return this.idle.size < this.max;
  }

  closeAll() {
    for (const session of [...this.idle.keys()]) this.take(session)?.close();
  }
}
