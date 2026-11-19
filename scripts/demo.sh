#!/usr/bin/env bash
# Starts the demo product, a tiny job board on http://127.0.0.1:5190 that embeds the mock
# interviewer the way a company's own site would. Run scripts/run.sh first, in another terminal.
# Its server starts sessions with the development product key from .env, which the browser never
# sees, and hears each ended session at its webhook, whose secret it reads from .env too.
# Unlike the other scripts this one doesn't load .env into the board's process: the board reads the
# three settings it needs (VOICE_DEV_PRODUCT_KEY, VOICE_ADDR, JOB_BOARD_WEBHOOK_SECRET) itself and
# never sees the signing key or the database address.
set -euo pipefail
cd "$(dirname "$0")/.."

# Only to find voice; a value in the environment wins over .env's, as in the other scripts.
addr="${VOICE_ADDR:-$(sed -n 's/^\(export \)\{0,1\}VOICE_ADDR=//p' .env 2>/dev/null | tail -1 | tr -d "\"'")}"
addr="${addr:-127.0.0.1:8080}"
if ! curl -s -o /dev/null --max-time 2 "http://$addr/"; then
  echo "demo: voice is not answering on $addr; start it with scripts/run.sh in another terminal" >&2
  exit 1
fi
if ! curl -fsS -o /dev/null --max-time 5 "http://$addr/client/voice-guide.js"; then
  echo "demo: voice doesn't serve its drop-in client at http://$addr/client/voice-guide.js; run scripts/build-client.sh, or restart scripts/run.sh, which builds it" >&2
  exit 1
fi
if ! grep -q '^\(export \)\{0,1\}JOB_BOARD_WEBHOOK_SECRET=.' .env 2>/dev/null; then
  cat >&2 <<'MSG'
demo: the board needs the webhook's secret in .env, once:
  1. scripts/webhook.sh set http://127.0.0.1:5190/webhook   (prints the secret, only once;
     if the workspace already has a webhook, scripts/webhook.sh rotate prints a new one)
  2. add the line JOB_BOARD_WEBHOOK_SECRET=whsec_... to .env
  3. run scripts/demo.sh again
MSG
  exit 1
fi
echo "Job board: http://127.0.0.1:5190"
exec node examples/job-board/server.mjs
