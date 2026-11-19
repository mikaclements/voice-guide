#!/usr/bin/env bash
# Sourced, in place of scripts/lib/env.sh, by the check scripts that run their own database, model
# service and voice, so a running scripts/run.sh is left alone:
#   . scripts/lib/own-voice.sh DATABASE VOICE_ADDR MODEL_PORT
# The addresses are the script's own defaults, never .env's, which are scripts/run.sh's; ones given
# in the environment win. Refuses a taken port, then the script calls own_voice_setup and
# start_voice; everything started is stopped, and the database dropped, when the script exits.
voice_addr="${VOICE_ADDR:-$2}"
model_port="${MODEL_PORT:-$3}"
. scripts/lib/env.sh

db="$1"
export DATABASE_URL="${DATABASE_URL%/*}/$db?sslmode=disable"
export VOICE_ADDR="$voice_addr" MODEL_PORT="$model_port"
export VOICE_MODEL_URL="http://$model_host:$MODEL_PORT"
export MODEL_PROVIDER="${MODEL_PROVIDER:-claude-subscription}"
# Checked first: a voice or model service already on a port, scripts/run.sh's say, would answer
# instead, and its turns would be saved on another database.
for port in "${VOICE_ADDR##*:}" "$MODEL_PORT"; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null; then
    echo "port $port is taken; stop what holds it (lsof -nP -iTCP:$port -sTCP:LISTEN) or pick free ones: VOICE_ADDR=127.0.0.1:$((${VOICE_ADDR##*:} + 10000)) MODEL_PORT=$((MODEL_PORT + 10000)) .claude/skills/measure/$(basename "$0") ..." >&2
    exit 1
  fi
done
unset voice_addr model_port port

psql() { docker compose exec -T postgres psql -q -v ON_ERROR_STOP=1 -U voice "$@"; }

# Makes the database fresh, migrates, seeds and publishes it, builds voice into $work and starts
# the model service. The compose database must be running. Publishes a copy of agents/ without any
# file's model: line, so the model each script measures (VOICE_MODEL) answers for every agent;
# left in, an agent's own model would answer whatever the script asked for. Removing the line,
# rather than setting it to the measured model, keeps one publish serving every model a script
# compares, since it restarts voice with another VOICE_MODEL and does not publish again.
own_voice_setup() {
  work=$(mktemp -d)
  pids=()
  trap own_voice_cleanup EXIT
  psql -d postgres -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" -c "CREATE DATABASE $db" >/dev/null 2>&1
  go run ./cmd/migrate >/dev/null
  SEED_DB="$db" scripts/lib/seed.sh
  mkdir "$work/agents"
  for file in "${VOICE_AGENTS_DIR:-agents}"/*.md; do
    grep -v '^model:' "$file" >"$work/agents/$(basename "$file")"
  done
  VOICE_AGENTS_DIR="$work/agents" go run ./cmd/publish >/dev/null
  go build -o "$work/voice" ./cmd/voice
  ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-}" node services/model/src/main.ts >"$work/model.log" 2>&1 &
  pids+=($!)
}

own_voice_cleanup() {
  # Bash 3.2, the one macOS ships, takes an empty array as unbound under set -u, which would
  # stop the cleanup before the database is dropped when a step fails before anything started.
  kill ${pids[@]+"${pids[@]}"} 2>/dev/null || true
  wait 2>/dev/null || true
  psql -d postgres -c "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null
  rm -rf "$work"
}

# Starts voice, logging to $work/voice.log or, given a name, $work/voice-NAME.log, and waits until
# it answers; its process ID is left in $voice.
start_voice() {
  "$work/voice" >"$work/voice${1:+-$1}.log" 2>&1 &
  voice=$!
  pids+=("$voice")
  for _ in $(seq 100); do curl -s -o /dev/null "http://$VOICE_ADDR/v1/agents" && break; sleep 0.2; done
}

# Prints the model each reply since a time was recorded as served by, from the turns' stages, so a
# run's label can be checked against what answered: recorded_models "$since" "$label".
# $since is a UTC time taken before the run, from `date -u +%Y-%m-%dT%H:%M:%SZ`.
recorded_models() {
  echo "  recorded model: $(echo "SELECT coalesce(string_agg(DISTINCT implementation, ', '), 'none recorded') FROM turn_stages WHERE stage = 'model_reply' AND started_at >= :'since'" | psql -d "$db" -At -v since="$1")"
}
