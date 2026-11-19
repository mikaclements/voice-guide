#!/usr/bin/env bash
# Times the real page end to end: headless Chrome (check-page.mts) opens the test page, starts a
# session with the mock interviewer, hears its greeting, sends TURNS typed answers, each once the
# reply before it has finished sounding, and ends the session. For each reply it prints, in ms from
# the press (Start for the greeting, Send for the rest), when the words reached voice, voice's stage
# times (model first text, first phrase ready, first audio sent), when the first audio packet reached
# the browser, when its first sound came out of the browser's audio, and the page's own line; then
# the pauses inside each reply and the browser's jitter buffer. Kokoro runs as scripts/run.sh would
# start it (KOKORO=mlx natively on its own port, else the compose container); runs on its own
# database, model service, voice and page (ports 18091, 18080 and 15173 unless MODEL_PORT,
# VOICE_ADDR or PAGE_PORT is given), so a running scripts/run.sh is left alone. Typed turns only:
# speech detection is the fake, which hears nothing. Calls the real model with MODEL_PROVIDER
# (claude-subscription unless set), MODEL_PROVIDER=fake costs nothing. Run `npm ci` once first.
#   .claude/skills/measure/check-page.sh [TURNS] [MODEL]    defaults: 5, claude-sonnet-5-5
set -euo pipefail
cd "$(dirname "$0")/../../.."
. .claude/skills/measure/own-voice.sh check_page 127.0.0.1:18080 18091

turns="${1:-5}"
model="${2:-claude-sonnet-5-5}"
page_port="${PAGE_PORT:-15173}"
require_free_port "$page_port" "pick a free one: PAGE_PORT=$((page_port + 1)) .claude/skills/measure/check-page.sh ..."
docker compose up -d --wait postgres
case "${KOKORO:-}" in
"") docker compose --profile tts up -d --wait kokoro ;;
mlx)
  # Its own port, so a native Kokoro scripts/run.sh started is left alone.
  export KOKORO_MLX_PORT="${KOKORO_MLX_PORT:-18882}"
  export VOICE_TTS_URL="http://127.0.0.1:$KOKORO_MLX_PORT"
  ;;
*)
  echo "KOKORO must be unset (Docker) or mlx, not ${KOKORO}" >&2
  exit 1
  ;;
esac
own_voice_setup
if [ "${KOKORO:-}" = mlx ]; then
  scripts/kokoro-mlx.sh >/dev/null &
  kokoro=$!
  pids+=("$kokoro")
  # Up to 20 minutes, as scripts/run.sh waits: the first start installs about 0.6 GB of packages
  # and downloads 0.3 GB of model.
  wait_http -f -p "$kokoro" -n 6000 -i 0.2 "$VOICE_TTS_URL/health"
fi
VOICE_SPEECH_DETECTION=fake VOICE_MODEL="$model" start_voice
npm run --silent dev --workspace web -- --port "$page_port" >"$work/page.log" 2>&1 &
pids+=($!)
wait_http "http://127.0.0.1:$page_port/"

echo "== $MODEL_PROVIDER $model, kokoro ${KOKORO:-docker}, the greeting and $turns typed turns"
since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
# In the background and waited for, so the cleanup stops it, and its browser, if this script is
# stopped first.
node .claude/skills/measure/check-page.mts "http://127.0.0.1:$page_port/" "$turns" >"$work/page.jsonl" &
pids+=($!)
wait "${pids[${#pids[@]} - 1]}" || { tail -20 "$work/voice.log" "$work/page.log" >&2; exit 1; }
recorded_models "$since"
# Each turn's start (the words or the opening reaching voice) and the end of each stage, in ms since
# the epoch, in the order the turns were taken; then how each reply opens, its first 60 characters,
# enough for its first sentence.
psql -d "$db" -At -F ' ' >"$work/stages.txt" <<'SQL'
SELECT t.seq, round(extract(epoch FROM t.created_at) * 1000), st.stage,
       round(extract(epoch FROM st.started_at) * 1000) + st.duration_ms
FROM turns t JOIN turn_stages st ON st.turn_id = t.id AND st.workspace_id = t.workspace_id
ORDER BY t.seq;
SQL
psql -d "$db" -At -F '|' -c "SELECT seq, replace(left(agent_text, 60), E'\\n', ' ') FROM turns ORDER BY seq" >"$work/opens.txt"
python3 -c '
import json, sys
lines = [json.loads(l) for l in open(sys.argv[1])]
replies = [l for l in lines if "reply" in l]
other = [l for l in lines if "reply" not in l]
opens = dict(l.rstrip("\n").split("|", 1) for l in open(sys.argv[3]) if "|" in l)
opens = {int(k): v for k, v in opens.items()}
turns = {}
for line in open(sys.argv[2]):
    seq, start, stage, end = line.split()
    turns.setdefault(int(seq), {"start": int(start)})[stage] = int(end)
cols = [("at voice", "start"), ("first text", "model_first_text"), ("phrase ready", "first_phrase_ready"), ("audio sent", "first_audio_sent")]
print("  ms from the press: " + ", ".join(c for c, _ in cols) + ", packet at the browser, first sound | the page")
pauses = []
for r in replies:
    t = turns.get(r["reply"] + 1, {})
    at = lambda end: "-" if end is None else str(end - r["press"])
    cells = [at(t.get(k)) for _, k in cols] + [at(r.get("first_packet")), at(r.get("first_sound"))]
    label = "greeting" if r["reply"] == 0 else "turn %d" % r["reply"]
    print(f"  {label}: " + " / ".join(cells) + " | " + (r.get("page") or ""))
    print("    opens:", opens.get(r["reply"] + 1, ""))
    pauses += r["pauses_ms"]
    print("    pauses inside the reply, ms:", r["pauses_ms"])
    for piece in r.get("streamed", [])[:6]:  # past six pieces the first phrase has long been cut
        print("    text at", piece)
if pauses:
    s = sorted(pauses)
    print(f"  pauses: {len(s)}, median {s[len(s) // 2]} ms, longest {s[-1]} ms")
for o in other:
    print("  browser jitter buffer: average delay", o["jitter_buffer_ms"], "ms, target", o["jitter_target_ms"], "ms")
' "$work/page.jsonl" "$work/stages.txt" "$work/opens.txt"
