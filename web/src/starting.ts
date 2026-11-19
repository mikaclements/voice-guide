// One press of Start: the product's server starts the session, with its request ID kept when voice
// may have started the session, so pressing Start again can't start two.
import {
  type Agent,
  failure,
  type Kept,
  keepRequestId,
  ProblemError,
  requestIdFor,
  type Started,
  ticketExpiredType,
} from "./api.ts";

export class Starter {
  private readonly startSession: (agentId: string, requestId: string) => Promise<Started>;
  private kept: Kept | undefined;

  constructor(startSession: (agentId: string, requestId: string) => Promise<Started>) {
    this.startSession = startSession;
  }

  async start(agent: Agent): Promise<Started> {
    const requestId = requestIdFor(agent.id, this.kept);
    const retry = this.kept?.key === agent.id;
    try {
      const session = await this.startSession(agent.id, requestId);
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
      this.kept = keep ? { key: agent.id, requestId } : undefined;
      throw err;
    }
  }
}
