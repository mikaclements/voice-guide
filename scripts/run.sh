#!/usr/bin/env bash
# Starts voice-guide for local development with the settings in .env: the database, Kokoro, the
# speech-to-text service, migrations, the workspace, the agents published from agents/, the drop-in
# client's build, the model service, the web page (as scripts/web.sh serves it), then the voice
# service. Stopping it (Ctrl-C) stops what it runs outside Docker.
#
# KOKORO=mlx runs Kokoro natively on this Mac's GPU through Apple's MLX (scripts/kokoro-mlx.sh)
# instead of in compose, which is CPU only; it needs macOS 14 or later on Apple silicon and uv.
#
# Replies come from the Claude API by default, billed to ANTHROPIC_API_KEY in .env.
# MODEL_PROVIDER=claude-subscription uses Claude Code signed in on this Mac instead: the model service
# runs on this Mac, not in a container, so Claude Code finds the login in the macOS keychain.
# MODEL_PROVIDER=fake costs nothing.
set -euo pipefail
cd "$(dirname "$0")/.."
save_given_settings=1
. scripts/lib/env.sh
# A fresh clone has no packages yet: install them here, so getting started is the clone and this script.
[ -d node_modules ] || npm ci

export MODEL_PROVIDER="${MODEL_PROVIDER:-claude-api}"
. scripts/lib/api-key.sh
need_api_key "or MODEL_PROVIDER=claude-subscription to use Claude Code signed in on this Mac"

export MODEL_PORT="${MODEL_PORT:-8091}"
export VOICE_MODEL_URL="${VOICE_MODEL_URL:-http://$model_host:$MODEL_PORT}"
# Checked first: voice would otherwise talk to whatever already holds the port, an older model
# service on another provider say.
if lsof -nP -iTCP:"$MODEL_PORT" -sTCP:LISTEN >/dev/null; then
  echo "port $MODEL_PORT is taken; stop what holds it (lsof -nP -iTCP:$MODEL_PORT -sTCP:LISTEN) or set MODEL_PORT" >&2
  exit 1
fi

# Stops what this script started: native Kokoro, the model service and the page. Waits for each too,
# so their ports are free once this script ends. Each step may fail, as on a process that already
# exited, without changing how the script exits.
kokoro="" model="" watcher="" web="" note=""
stop_children() {
  local p
  for p in $note $web $watcher $model $kokoro; do kill "$p" 2>/dev/null || true; done
  for p in $note $web $watcher $model $kokoro; do wait "$p" 2>/dev/null || true; done
}
trap stop_children EXIT

# Kokoro speaks the replies; it is started here by name, since its image is too large for every
# compose command to pull.
if [ "${VOICE_TTS:-kokoro}" = kokoro ]; then
  case "${KOKORO:-}" in
  "")
    docker compose --profile tts up -d --wait kokoro
    ;;
  mlx)
    port="${KOKORO_MLX_PORT:-8882}"
    # Refused before anything slow starts, Docker included; the script repeats the checks for people
    # running it alone. Checked here, not only in the script, which runs in the background and would
    # report late.
    if [ "$(uname -s)" != Darwin ] || [ "$(sysctl -n hw.optional.arm64 2>/dev/null)" != 1 ]; then
      echo "KOKORO=mlx needs macOS on Apple silicon; leave KOKORO unset (remove it from .env) to use Docker" >&2
      exit 1
    fi
    if [ "$(sw_vers -productVersion | cut -d. -f1)" -lt 14 ]; then
      echo "KOKORO=mlx needs macOS 14 or later; leave KOKORO unset (remove it from .env) to use Docker" >&2
      exit 1
    fi
    if ! command -v uv >/dev/null; then
      echo "KOKORO=mlx needs uv; install it (brew install uv) or leave KOKORO unset (remove it from .env) to use Docker" >&2
      exit 1
    fi
    if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null; then
      echo "port $port is taken; stop what holds it (lsof -nP -iTCP:$port -sTCP:LISTEN) or set KOKORO_MLX_PORT" >&2
      exit 1
    fi
    # Started now and waited for before voice: the first start downloads packages and the model.
    scripts/kokoro-mlx.sh &
    kokoro=$!
    kokoro_url="http://127.0.0.1:$port"
    export VOICE_TTS_URL="$kokoro_url"
    ;;
  mac)
    echo "KOKORO=mac was removed; use KOKORO=mlx (Apple's MLX, faster) or leave KOKORO unset (remove it from .env) for Docker" >&2
    exit 1
    ;;
  *)
    echo "KOKORO must be unset (Docker) or mlx, not ${KOKORO}" >&2
    exit 1
    ;;
  esac
fi
docker compose up -d --wait postgres
# The speech-to-text service hears what the user says once speech detection notices it; built here,
# so it is rebuilt when its files change (cached otherwise).
if [ "${VOICE_SPEECH_DETECTION:-silero}" = silero ]; then
  docker compose --profile stt up -d --build --wait stt
fi
scripts/migrate.sh
scripts/lib/seed.sh
scripts/publish.sh
scripts/build-client.sh

ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-}" node services/model/src/main.ts &
model=$!
# Voice starts once the model service answers, and only if the process started here is alive.
ready=""
for _ in $(seq 100); do
  kill -0 "$model" 2>/dev/null || break
  curl -s -o /dev/null "http://$model_host:$MODEL_PORT/" && ready=1 && break
  sleep 0.1
done
if [ -z "$ready" ] || ! kill -0 "$model" 2>/dev/null; then
  echo "the model service didn't start on port $MODEL_PORT; its log is above" >&2
  exit 1
fi
if [ -n "$kokoro" ]; then
  ready=""
  # Up to 20 minutes, a round guess: the first start installs about 0.6 GB of packages and downloads
  # 0.3 GB of model before it loads the model. It ends sooner when the script exits.
  for _ in $(seq 6000); do
    kill -0 "$kokoro" 2>/dev/null || break
    curl -sf -o /dev/null "$kokoro_url/health" && ready=1 && break
    sleep 0.2
  done
  if [ -z "$ready" ]; then
    echo "native Kokoro didn't start; its message is above" >&2
    exit 1
  fi
  # Voice goes on speaking text only when Kokoro dies, so this only says why the audio stopped.
  (while kill -0 "$kokoro" 2>/dev/null; do sleep 1; done
    echo "native Kokoro stopped: replies come as text only until you restart (its log is named above)" >&2) &
  watcher=$!
fi
# The page, so this one command starts everything; left alone when something already holds its port,
# as scripts/web.sh started first does.
ready_note="ready: open http://127.0.0.1:5173"
if lsof -nP -iTCP:5173 -sTCP:LISTEN >/dev/null; then
  ready_note="voice is ready; the page at :5173 is someone else's: stop it, or run scripts/web.sh after"
else
  npm run --silent dev --workspace web &
  web=$!
fi
# Says where to go once voice answers, after its own start-up lines.
(until curl -s -o /dev/null "http://${VOICE_ADDR:-127.0.0.1:8080}/"; do sleep 0.5; done
  echo "$ready_note" >&2) &
note=$!
go run ./cmd/voice
