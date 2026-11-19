#!/usr/bin/env bash
# Talks to the mock interviewer the way a browser does, with no one at the microphone: for each
# model, starts a session, opens its audio connection over WebRTC and says scripted sentences into
# it, Kokoro's speech at the pace it is spoken (cmd/checkspeak); prints what voice heard, each reply,
# the time from each sentence's last packet to the reply's first audio packet, and voice's own
# stage times for each spoken turn. Then it asks once more and cuts in on the reply as it begins,
# printing the time from the first packet said over it to the reply's last audio packet, and voice's
# own time from the interrupt to the reply stopping. Then it starts GREETINGS sessions (10 unless set,
# 0 for none), each with a line of its own in its context, so its session's layers are read afresh as
# in real use, and each asking for the interviewer's opening once its audio connection is up, as the
# page does; it prints the time to the greeting's first audio packet from the session's start and
# from the audio connection coming up, and voice's stage times for each opening: session 1, the
# coldest, apart, then the median and 90th percentile of the rest. Starts Kokoro and the speech-to-text service in
# compose (the first start pulls and builds them), and runs on its own database, model service and
# voice (ports 18091 and 18080 unless MODEL_PORT or VOICE_ADDR is given), so a running scripts/run.sh
# is left alone. Calls the real model on your Claude login and draws from its usage limits, unless
# MODEL_PROVIDER=fake. Run `npm ci` once first.
#   [GREETINGS=10] .claude/skills/measure/check-speak.sh [TURNS] [MODEL...]    defaults: 3, claude-sonnet-5-5 claude-haiku-5-5
set -euo pipefail
cd "$(dirname "$0")/../../.."
. .claude/skills/measure/own-voice.sh check_speak 127.0.0.1:18080 18091

turns="${1:-3}"
greetings="${GREETINGS:-10}"
shift || true
models=("$@")
[ ${#models[@]} -gt 0 ] || models=(claude-sonnet-5-5 claude-haiku-5-5)
docker compose up -d --wait postgres
docker compose up -d --build --wait kokoro stt
own_voice_setup
go build -o "$work/checkspeak" ./cmd/checkspeak

for model in "${models[@]}"; do
  since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  VOICE_MODEL="$model" start_voice "$model"
  echo "== $MODEL_PROVIDER $model, $turns spoken turns"
  "$work/checkspeak" -turns "$turns" || { tail -20 "$work/voice-$model.log" >&2; exit 1; }
  if [ "$greetings" -gt 0 ]; then
    echo "== $MODEL_PROVIDER $model, $greetings greetings"
    "$work/checkspeak" -greetings "$greetings" || { tail -20 "$work/voice-$model.log" >&2; exit 1; }
  fi
  kill "$voice" && wait "$voice" 2>/dev/null || true
  recorded_models "$since"
  # voice's stage times for each spoken turn, in ms from the end of the speech, then for each
  # opening, the only turns the check sends as text, in ms from its arrival.
  python3 -c '
import json, sys
stages = ["final_text_ms", "end_of_turn_check_ms", "taken_ms", "model_first_text_ms", "first_phrase_ready_ms", "first_audio_sent_ms"]
openings = []
for line in open(sys.argv[1]):
    r = json.loads(line) if line.startswith("{") else {}
    if r.get("msg") == "turn spoken" and r.get("spoken"):
        print("  voice, from speech end:", ", ".join(f"{s[:-3]} {r[s]}" for s in stages if s in r), "| ended by", r["turn_end"], "| talked on", r.get("talked_on", 0))
    if r.get("msg") == "turn interrupted" and "stopped_ms" in r:
        print("  voice, interrupted: the reply stopped", r["stopped_ms"], "ms after the interrupt")
    if r.get("msg") == "turn spoken" and not r.get("spoken"):
        openings.append(r)
        label = f"opening {len(openings)}" + (" (the coldest)" if len(openings) == 1 else "")
        print("  voice,", label, "in", r["session"], "from its arrival:", ", ".join(f"{s[:-3]} {r[s]}" for s in stages if s in r))
for s in ["model_first_text_ms", "first_audio_sent_ms"]:
    ms = sorted(r[s] for r in openings[1:] if s in r)
    if ms:  # nearest rank, as checkspeak reports its own
        print(f"  voice, openings 2 to {len(openings)}: {s[:-3]} median {ms[(len(ms) * 50 + 99) // 100 - 1]}, 90th percentile {ms[(len(ms) * 90 + 99) // 100 - 1]}")
' "$work/voice-$model.log"
done
