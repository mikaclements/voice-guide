import { setTimeout as wait } from "node:timers/promises";
import type { Provider, ReplyRequest } from "../api.ts";

export interface FakeSettings {
  reply: string;
  firstTextMs: number;
  charsPerSecond: number;
}

// A free model for load tests: it behaves like a model in timing shape, not in content.
export function fakeProvider(
  settings: FakeSettings,
  sleep: (ms: number, signal: AbortSignal) => Promise<unknown> = (ms, signal) =>
    wait(ms, undefined, { signal }),
): Provider {
  const words = settings.reply.match(/\S+\s*/g) ?? [];
  return {
    name: "fake",
    async *reply(request, signal) {
      await sleep(settings.firstTextMs, signal);
      // An answer to a schema that voice's check always takes: the reply as the summary, no data.
      const answer = request.schema && JSON.stringify({ summary: settings.reply, data: {} });
      for (const [i, word] of (answer ? [answer] : words).entries()) {
        if (signal.aborted) return;
        if (i > 0) await sleep(Math.round((word.length * 1000) / settings.charsPerSecond), signal);
        yield { type: "text", text: word };
      }
      yield {
        type: "usage",
        usage: {
          inputTokens: estimateTokens(inputText(request)),
          cachedInputTokens: 0,
          cacheWriteTokens: 0,
          outputTokens: estimateTokens(settings.reply),
        },
      };
    },
  };
}

function inputText(r: ReplyRequest): string {
  return [...r.stableLayers, ...r.conversation.map((m) => m.text), r.newWords].join("");
}

// About four characters per token, the usual rule of thumb for English.
function estimateTokens(text: string): number {
  return Math.ceil(text.length / 4);
}
