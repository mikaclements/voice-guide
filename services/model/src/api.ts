// The provider-neutral contract between voice and the model service. Field names on the
// wire are snake_case; every provider maps this same request to its own vendor's API.

export interface Message {
  role: "user" | "agent";
  text: string;
}

export interface ReplyRequest {
  model: string;
  // Left out for an ask outside any session, such as the summary written once one ends: nothing is
  // kept for it, so no session's kept state is taken, replaced or pushed out by it.
  sessionId?: string;
  // The layers that stay the same for the whole session, in order; providers mark them cacheable.
  // The first is the agent's, shared by every session of its version; the rest are this session's.
  stableLayers: string[];
  conversation: Message[];
  // The new message: the newest screen input of each name, then the user's words.
  newWords: string;
  // The new message as later requests' conversations will hold it, its screen input as markers
  // where it changed; left out when it is the same as newWords. A provider that keeps the
  // conversation between replies records this, so it matches the next request.
  newWordsSaved?: string;
  // The new message with only the screen input this turn carried, what changed since the turn
  // before; left out when it is the same as newWords. A provider that keeps the conversation sends
  // this to what it kept, which already holds every earlier block, rather than repeating them.
  newWordsOwn?: string;
  // The JSON schema the reply answers in, for an ask wanting data rather than words, such as a
  // summary with data; left out, the reply is plain text.
  schema?: Record<string, unknown>;
}

// Every provider reports the same way: inputTokens are those read fresh and exclude the cached
// ones; cachedInputTokens were read from the cache; cacheWriteTokens were written to it. A reply
// whose provider reported no usage ends with usage null.
export interface Usage {
  inputTokens: number;
  cachedInputTokens: number;
  cacheWriteTokens: number;
  outputTokens: number;
}

// What a provider yields: text as it arrives, then its usage once, with the model that answered
// when the provider knows it.
export type ProviderEvent =
  | { type: "text"; text: string }
  | { type: "usage"; usage: Usage; model?: string };

// Why a reply failed, from a closed set, so voice can tell an error worth a brief retry from one no
// retry fixes. The provider's own message is never passed on: it may echo what the user said.
export type ErrorKind =
  | "rate_limited"
  | "quota"
  | "overloaded"
  | "unavailable"
  | "auth"
  | "bad_request"
  | "too_large"
  | "unknown";

export class ProviderError extends Error {
  readonly kind: ErrorKind;
  // What can be said of the failure without its message, logged with it: fixed names and numbers,
  // such as a process's exit code, never text that could quote the user.
  readonly detail?: Record<string, string | number>;
  constructor(kind: ErrorKind, message: string, detail?: Record<string, string | number>) {
    super(message);
    this.kind = kind;
    this.detail = detail;
  }
}

export interface Provider {
  name: string;
  reply(request: ReplyRequest, signal: AbortSignal): AsyncIterable<ProviderEvent>;
  // Stops whatever the provider keeps between replies, settling once it has; called once, at
  // shutdown.
  close?(): Promise<void>;
}

export function parseRequest(body: unknown): ReplyRequest | string {
  if (typeof body !== "object" || body === null) return "body must be a JSON object";
  const b = body as Record<string, unknown>;
  const isText = (v: unknown) => typeof v === "string" && v.length > 0;
  if (!isText(b.model)) return "model is required";
  if (b.session_id !== undefined && !isText(b.session_id)) {
    return "session_id, when sent, must be text";
  }
  if (!isText(b.new_words)) return "new_words is required";
  for (const field of ["new_words_saved", "new_words_own"]) {
    if (b[field] !== undefined && !isText(b[field])) return `${field}, when sent, must be text`;
  }
  const layers = b.stable_layers;
  if (!Array.isArray(layers) || !layers.every((l) => typeof l === "string")) {
    return "stable_layers must be a list of strings";
  }
  const conversation = b.conversation ?? [];
  if (!Array.isArray(conversation)) return "conversation must be a list";
  const schema = b.schema;
  if (
    schema !== undefined &&
    (typeof schema !== "object" || schema === null || Array.isArray(schema))
  )
    return "schema, when sent, must be a JSON schema object";
  for (const m of conversation) {
    if (typeof m?.text !== "string" || (m.role !== "user" && m.role !== "agent")) {
      return "each conversation message needs a role (user or agent) and text";
    }
  }
  return {
    model: b.model as string,
    sessionId: b.session_id as string | undefined,
    stableLayers: layers,
    conversation: conversation as Message[],
    newWords: b.new_words as string,
    ...(b.new_words_saved !== undefined && { newWordsSaved: b.new_words_saved as string }),
    ...(b.new_words_own !== undefined && { newWordsOwn: b.new_words_own as string }),
    ...(schema !== undefined && { schema: schema as Record<string, unknown> }),
  };
}
