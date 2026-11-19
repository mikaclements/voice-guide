#!/usr/bin/env bash
# Transcribes a sentence Kokoro speaks through the real speech-to-text service, streamed in at the
# pace it is spoken, and prints the text and the time from the audio's end to the final text; then
# asks the end-of-turn check about a finished and an unfinished sentence.
# Builds the service and starts it with Kokoro in compose first (Kokoro's first start pulls a
# 4.5 GB image); STT_MODEL picks Moonshine's size (small unless set).
#   .claude/skills/measure/check-stt.sh
set -euo pipefail
cd "$(dirname "$0")/../../.."
docker compose up -d --build --wait kokoro stt
cd services/stt
TEST_STT_URL=ws://127.0.0.1:8092 TEST_KOKORO_URL=http://127.0.0.1:8880 \
  uv run --locked pytest -q -s tests/test_live.py
