// One press of Start: the product's server starts the session, with its request ID kept when voice
// may have started the session, so pressing Start again can't start two.
import { ProblemError, ticketExpiredType } from "../client/api.ts";
import { failure, type Kept, keepRequestId, requestIdFor } from "../client/retry.ts";
import type { Agent, Started, StartOptions } from "./product.ts";

export class Starter {
  private readonly startSession: (
    agentId: string,
    requestId: string,
    options?: StartOptions,
  ) => Promise<Started>;
  private kept: Kept | undefined;

  constructor(
    startSession: (agentId: string, requestId: string, options?: StartOptions) => Promise<Started>,
  ) {
    this.startSession = startSession;
  }

  // Options belong to the request ID: voice answers a repeat with the first start's session, keeping
  // its voice and speed, so changed settings only take effect under a new request ID.
  async start(agent: Agent, options?: StartOptions): Promise<Started> {
    const key = options ? `${agent.id}\n${JSON.stringify(options)}` : agent.id;
    const requestId = requestIdFor(key, this.kept);
    const retry = this.kept?.key === key;
    try {
      const session = await this.startSession(agent.id, requestId, options);
      this.kept = undefined;
      // A repeat of a start past its session's cap answers with the session and no ticket: there
      // is nothing to talk with, and this request ID has nothing more to give.
      if (!session.ticket) {
        throw new ProblemError(401, ticketExpiredType, "Ticket expired", "", true);
      }
      return session;
    } catch (err) {
      // Kept unless voice itself answered with a refusal: the product's server turning the page
      // away (a missing context block, a bad body) means voice never saw this try, so an earlier
      // one it may have taken is still the one to repeat.
      const voiceAnswered = err instanceof ProblemError && err.fromVoice;
      const keep = !voiceAnswered || keepRequestId(failure(err), retry);
      this.kept = keep ? { key, requestId } : undefined;
      throw err;
    }
  }
}
