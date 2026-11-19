#!/usr/bin/env bash
# Checks that the mock interviewer remembers last time: starts a session with the placeholder user's
# resume, takes two typed turns, ends it, waits for the background job to save its summary, prints
# it and how long it took, then starts a new session and prints the interviewer's opening.
# Runs on its own database, model service and voice (ports 18093 and 18082 unless MODEL_PORT or
# VOICE_ADDR is given), so a running scripts/run.sh is left alone. Calls the real model on your
# Claude login and draws from its usage limits, unless MODEL_PROVIDER=fake. Run `npm ci` once first.
#   .claude/skills/measure/check-summary.sh [MODEL]    default: claude-haiku-5-5
set -euo pipefail
cd "$(dirname "$0")/../../.."
. .claude/skills/measure/own-voice.sh check_summary 127.0.0.1:18082 18093
export VOICE_MODEL="${1:-claude-haiku-5-5}"
own_voice_setup
since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
start_voice

id() { python3 -c 'import json, sys; print(json.load(sys.stdin)["id"])'; }
first=$(.claude/skills/drive-session/start-session.sh 2>/dev/null | id)
echo "== first session $first"
for said in "Hi, I'm ready. Please start the interview." \
  "I led the rebuild of shipment tracking at Harbourline, moving status updates from nightly batch files to an event stream; I mostly kept the team aligned."; do
  echo "you: $said"
  printf 'agent: '
  .claude/skills/drive-session/turn.sh "$first" "$said" 2>/dev/null | head -n 1
done
.claude/skills/drive-session/end-session.sh "$first" >/dev/null
ended=$(date +%s)

summary=""
for _ in $(seq 120); do
  summary=$(echo "SELECT content FROM user_context.context_items WHERE kind = 'session_summary' AND source_session_id = :'s';" |
    psql -d "$db" -At -v s="$first")
  [ -n "$summary" ] && break
  sleep 1
done
if [ -z "$summary" ]; then
  echo "no summary within 120 s; the job:" >&2
  psql -d "$db" -At -c "SELECT state, attempts, coalesce(last_error, '') FROM jobs" >&2
  exit 1
fi
echo "== summary, saved $(($(date +%s) - ended)) s after the end"
echo "$summary"

second=$(.claude/skills/drive-session/start-session.sh 2>/dev/null | id)
echo "== second session $second"
echo "you: Hi, I'm ready. Please start the interview."
printf 'agent: '
.claude/skills/drive-session/turn.sh "$second" "Hi, I'm ready. Please start the interview." 2>/dev/null | head -n 1
recorded_models "$since"
