#!/usr/bin/env bash
# Checks that the mock interviewer remembers last time: starts a session with the placeholder user's
# resume, takes two typed turns, ends it, waits for the background job to save its summary, prints
# it and how long it took, then starts a new session and prints the interviewer's opening.
# Runs on its own database, model service and voice (ports 18093 and 18082 unless MODEL_PORT or
# VOICE_ADDR is given), so a running scripts/run.sh is left alone. Calls the real model on your
# Claude login and draws from its usage limits, unless MODEL_PROVIDER=fake. Run `npm ci` once first.
# SAID names a file of the user's lines, one a turn, to say in place of the two built in. An agent
# with a summary schema prints the session's summary data after the summary.
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
said_lines=("Hi, I'm ready. Please start the interview."
  "I led the rebuild of shipment tracking at Harbourline, moving status updates from nightly batch files to an event stream; I mostly kept the team aligned.")
if [ -n "${SAID:-}" ]; then
  said_lines=()
  while IFS= read -r line || [ -n "$line" ]; do said_lines+=("$line"); done <"$SAID"
  [ ${#said_lines[@]} -gt 0 ] || { echo "SAID names an empty file" >&2; exit 1; }
fi
for said in "${said_lines[@]}"; do
  echo "you: $said"
  printf 'agent: '
  .claude/skills/drive-session/turn.sh "$first" "$said" 2>/dev/null | head -n 1
done
.claude/skills/drive-session/end-session.sh "$first" >/dev/null
ended=$(date +%s)

summary=""
for _ in $(seq 300); do
  summary=$(echo "SELECT content FROM user_context.context_items WHERE kind = 'session_summary' AND source_session_id = :'s';" |
    psql -d "$db" -At -v s="$first")
  [ -n "$summary" ] && break
  sleep 1
done
if [ -z "$summary" ]; then
  echo "no summary within 300 s; the job:" >&2
  psql -d "$db" -At -c "SELECT state, attempts, coalesce(last_error, '') FROM jobs" >&2
  exit 1
fi
echo "== summary, saved $(($(date +%s) - ended)) s after the end"
echo "$summary"
echo "SELECT coalesce(summary_status, 'null') || ' ' || coalesce(summary_data::text, 'no data') FROM sessions WHERE id = :'s';" | psql -d "$db" -At -v s="$first"

second=$(.claude/skills/drive-session/start-session.sh 2>/dev/null | id)
echo "== second session $second"
echo "you: Hi, I'm ready. Please start the interview."
printf 'agent: '
.claude/skills/drive-session/turn.sh "$second" "Hi, I'm ready. Please start the interview." 2>/dev/null | head -n 1
recorded_models "$since"
