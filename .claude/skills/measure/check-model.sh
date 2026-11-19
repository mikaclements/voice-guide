#!/usr/bin/env bash
# Sends scripted turns in one session through the model service, each with the conversation so far,
# and prints each reply as it streams; after each, a line with the turn's number, time to first
# text and token usage.
#   .claude/skills/measure/check-model.sh [MODEL] [PROVIDER] [TURNS]    defaults: claude-sonnet-5-5, claude-subscription, 3
# With claude-subscription this calls the real model on your Claude login and draws from its usage
# limits, with claude-api it bills the API key in .env; PROVIDER=fake costs nothing. It starts a model service of its own on MODEL_PORT (8091
# unless set), so it refuses a port another holds, scripts/run.sh's say: that one's replies, maybe
# on another provider, would be measured instead. Run `npm ci` once first; needs jq.
set -euo pipefail
cd "$(dirname "$0")/../../.."
. scripts/lib/env.sh
. scripts/lib/net.sh

model="${1:-claude-sonnet-5-5}"
provider="${2:-claude-subscription}"
turns="${3:-3}"
port="${MODEL_PORT:-8091}"

require_free_port "$port" "pick a free one: MODEL_PORT=18092 .claude/skills/measure/check-model.sh ..."

work=$(mktemp -d)
ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-}" MODEL_PROVIDER="$provider" MODEL_PORT="$port" \
  node services/model/src/main.ts >/dev/null &
pid=$!
# Waits for it to stop too, so the port is free once this script ends. Each step may fail, as on a
# process that already exited, without changing how the script exits.
trap 'kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; rm -rf "$work"' EXIT

# Whether the process started here is the one listening on the port, so no other answers for it.
ours() { lsof -nP -a -p "$pid" -iTCP:"$port" -sTCP:LISTEN >/dev/null; }
# It listens after 0.2 s on this Mac; 10 s allows a cold start on a slower machine, and a process
# that exits ends the wait at once.
for _ in $(seq 100); do
  kill -0 "$pid" 2>/dev/null || break
  ours && break
  sleep 0.1
done

layer="You are running a practice interview. Your words are spoken aloud, so answer in one or two short sentences and ask one question at a time."
said=(
  "Hi, I'm ready. Ask me about the last project I shipped."
  "A pricing service. The hard part was keeping quotes consistent while prices changed."
  "We versioned every price list, and each quote read one version, so it never mixed two."
  "About forty milliseconds at the ninety-ninth percentile, measured at the load balancer."
  "Mostly the cache: we filled it on deploy instead of letting the first requests pay."
)
conversation='[]'
for ((turn = 1; turn <= turns; turn++)); do
  if ! ours; then
    echo "the model service started here isn't listening on port $port; its log is above" >&2
    exit 1
  fi
  words="${said[(turn - 1) % ${#said[@]}]}"
  body=$(jq -n --arg model "$model" --arg layer "$layer" --argjson conversation "$conversation" \
    --arg words "$words" \
    '{model: $model, session_id: "check-model", stable_layers: [$layer], conversation: $conversation, new_words: $words}')
  echo "> $words"
  : >"$work/reply"
  # A pipe, so pipefail stops the script when curl fails: no connection, a cut stream, or a refusal
  # (whose problem prints first). The loop runs in a subshell, so the reply goes through a file.
  curl -sSN --fail-with-body -X POST "http://$model_host:$port/v1/replies" \
    -H 'content-type: application/json' --data "$body" |
    # A problem document ends without a line break; the second test reads it all the same.
    while IFS= read -r line || [ -n "$line" ]; do
      if [ "$(jq -r .type <<<"$line")" = text ]; then
        # The x keeps a trailing newline, which $(...) would drop: the reply sent back must be exact.
        text=$(jq -j .text <<<"$line" && printf x)
        printf '%s' "${text%x}" >>"$work/reply"
        printf '%s' "${text%x}"
      else
        printf '\n'
        jq -c --argjson turn "$turn" '{turn: $turn} + .' <<<"$line"
      fi
    done
  reply=$(cat "$work/reply" && printf x)
  conversation=$(jq -c --arg user "$words" --arg agent "${reply%x}" \
    '. + [{role: "user", text: $user}, {role: "agent", text: $agent}]' <<<"$conversation")
done
