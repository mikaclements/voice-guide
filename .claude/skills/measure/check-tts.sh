#!/usr/bin/env bash
# Speaks one reply through the real Kokoro-FastAPI, streamed in a word at a time as the model
# sends it and played back in real time, and prints the time to first audio and any wait for a
# later phrase. Starts Kokoro in compose first (the first start pulls a 4.5 GB image), unless
# TEST_KOKORO_URL names a server already running, such as native Kokoro on Apple's MLX
# (scripts/kokoro-mlx.sh), so one script compares them.
#   .claude/skills/measure/check-tts.sh
#   TEST_KOKORO_URL=http://127.0.0.1:8882 .claude/skills/measure/check-tts.sh
set -euo pipefail
cd "$(dirname "$0")/../../.."
if [ -z "${TEST_KOKORO_URL:-}" ]; then
  docker compose up -d --wait kokoro
  TEST_KOKORO_URL=http://127.0.0.1:8880
fi
export TEST_KOKORO_URL
go test -count=1 -v -run TestLive ./internal/speech/tts/kokoro/
