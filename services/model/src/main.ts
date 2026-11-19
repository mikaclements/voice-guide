import type { Provider } from "./api.ts";
import { claudeApiProvider } from "./providers/claude-api.ts";
import { claudeSubscriptionProvider } from "./providers/claude-subscription.ts";
import { fakeProvider } from "./providers/fake.ts";
import { createServer, shutdown } from "./server.ts";
import { type Settings, settingsFromEnv } from "./settings.ts";

let settings: Settings;
try {
  settings = settingsFromEnv(process.env);
} catch (err) {
  console.error(JSON.stringify({ msg: "invalid settings", error: (err as Error).message }));
  process.exit(1);
}

function providerFor(s: Settings): Provider {
  switch (s.provider) {
    case "claude-api":
      return claudeApiProvider({
        apiKey: s.apiKey ?? "",
        maxTokens: s.maxTokens,
        timeoutMs: s.replyTimeoutMs,
      });
    case "claude-subscription":
      return claudeSubscriptionProvider(undefined, process.env, {
        apiTimeoutMs: s.replyTimeoutMs,
        maxSessionProcesses: s.maxSessionProcesses,
        sessionProcessIdleMs: s.sessionProcessIdleMs,
      });
    case "fake":
      return fakeProvider({
        reply: "That makes sense. What did you try first, and why that?",
        firstTextMs: s.fakeFirstTextMs,
        charsPerSecond: s.fakeCharsPerSecond,
      });
  }
}

const provider = providerFor(settings);
const server = createServer(provider, settings);
server.listen(settings.port, settings.host, () =>
  console.log(
    JSON.stringify({
      msg: "listening",
      host: settings.host,
      port: settings.port,
      provider: provider.name,
    }),
  ),
);

// SIGHUP comes when the terminal running the service closes. SIGKILL can't be caught, so a killed
// service leaves its folder behind.
for (const signal of ["SIGINT", "SIGTERM", "SIGHUP"]) {
  process.on(signal, () => shutdown(server, provider).then(() => process.exit(0)));
}
