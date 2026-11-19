#!/usr/bin/env bash
# Starts all of voice-guide in Docker, needing nothing else on this machine but bash and openssl:
# the database, Kokoro, speech-to-text, the model service, voice and the test page, each built from
# this checkout (compose's app profile). Makes .env first if it's missing, with fresh signing keys,
# and asks for a missing Claude API key at a terminal, as scripts/run.sh does. Runs in the
# foreground: Ctrl-C stops the containers.
#   scripts/up.sh        start, and say when the page is ready
#   scripts/up.sh down   remove the containers; the database is kept
# scripts/run.sh is the development path, and the way to the faster voice on Apple silicon.
set -euo pipefail
cd "$(dirname "$0")/.."
case "${1:-}" in
"") ;;
down) exec docker compose --profile app down ;;
*)
  echo "usage: scripts/up.sh [down]" >&2
  exit 2
  ;;
esac
if ! docker info >/dev/null 2>&1; then
  echo "Docker isn't running: start Docker Desktop or OrbStack, then run this again" >&2
  exit 1
fi
save_given_settings=1
. scripts/lib/env.sh

export MODEL_PROVIDER="${MODEL_PROVIDER:-claude-api}"
if [ "$MODEL_PROVIDER" = claude-subscription ]; then
  echo "MODEL_PROVIDER=claude-subscription uses Claude Code signed in on this Mac, which a container can't" >&2
  echo "reach: use claude-api or fake here, or scripts/run.sh" >&2
  exit 1
fi
. scripts/lib/api-key.sh
need_api_key

# voice's and the page's ports, which scripts/run.sh and scripts/web.sh hold when they run, as do
# containers this script started and left running. The audio port isn't checked: the container
# runtime keeps a flow through it open for a minute after the containers stop, and compose names
# the port if it can't take it.
for port in 8080 5173; do
  holder="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -Fc 2>/dev/null | sed -n 's/^c//p' | head -n 1 || true)"
  if [ -n "$holder" ]; then
    echo "port $port is taken by $holder: stop scripts/run.sh or scripts/web.sh if one runs, or run" >&2
    echo "scripts/up.sh down for containers left from before (lsof -nP -iTCP:$port -sTCP:LISTEN shows it)" >&2
    exit 1
  fi
done

# Says where to go once voice and the page answer, among the containers' logs.
(until curl -s -o /dev/null http://127.0.0.1:8080/ && curl -s -o /dev/null http://127.0.0.1:5173/; do sleep 1; done
  echo "ready: open http://127.0.0.1:5173" >&2) &
note=$!
trap 'kill "$note" 2>/dev/null || true' EXIT
# The API key on this one command, the way scripts/run.sh hands it to the model service: compose puts
# it in the model container only, and only for the provider that bills it. Given empty otherwise, so
# compose doesn't take it from .env.
key=""
[ "$MODEL_PROVIDER" != claude-api ] || key="${ANTHROPIC_API_KEY:-}"
ANTHROPIC_API_KEY="$key" docker compose --profile app up --build
