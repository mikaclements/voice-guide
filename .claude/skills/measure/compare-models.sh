#!/usr/bin/env bash
# Times typed turns per model. For each model, starts a session with the mock interviewer and the
# placeholder user's resume, takes the same scripted turns, then prints each stage's median and 90th
# percentile from the turns' recorded stage times, and the share of the prompt read from cache.
# Runs on its own database, model service and voice (ports 18091 and 18080 unless MODEL_PORT or
# VOICE_ADDR is given), so a running scripts/run.sh is left alone. Calls the real model on your
# Claude login and draws from its usage limits, unless MODEL_PROVIDER=fake. Run `npm ci` once first.
#   .claude/skills/measure/compare-models.sh [TURNS] [MODEL...]    defaults: 5, claude-sonnet-5-5 claude-haiku-5-5
set -euo pipefail
cd "$(dirname "$0")/../../.."
. scripts/lib/own-voice.sh compare_models 127.0.0.1:18080 18091

turns="${1:-5}"
shift || true
models=("$@")
[ ${#models[@]} -gt 0 ] || models=(claude-sonnet-5-5 claude-haiku-5-5)
own_voice_setup

said=(
  "Hi, I'm ready. Please start the interview."
  "I led the rebuild of shipment tracking at Harbourline: status updates moved from nightly batch files to an event stream."
  "The hardest part was keeping the read model consistent when events arrived out of order."
  "We gave every event a version per shipment and dropped any older than the one already applied."
  "Support tickets about wrong statuses fell by about two thirds in the first quarter."
)
for model in "${models[@]}"; do
  since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  VOICE_MODEL="$model" start_voice "$model"
  session=$(.claude/skills/drive-session/start-session.sh 2>/dev/null | python3 -c 'import json, sys; print(json.load(sys.stdin)["id"])')
  for ((turn = 0; turn < turns; turn++)); do
    .claude/skills/drive-session/turn.sh "$session" "${said[turn % ${#said[@]}]}" >/dev/null 2>&1
  done
  kill "$voice" && wait "$voice" 2>/dev/null || true
  echo "== $model, $turns turns: stage, median ms, 90th percentile ms"
  recorded_models "$since"
  psql -d "$db" -At -F ' ' -v s="$session" <<'SQL'
SELECT st.stage, round(percentile_cont(0.5) WITHIN GROUP (ORDER BY st.duration_ms)),
       round(percentile_cont(0.9) WITHIN GROUP (ORDER BY st.duration_ms))
FROM turn_stages st JOIN turns t ON t.id = st.turn_id AND t.workspace_id = st.workspace_id
WHERE t.session_id = :'s'
GROUP BY st.stage
ORDER BY array_position(ARRAY['prompt', 'model_first_text', 'model_reply', 'save'], st.stage);
SELECT 'read from cache: ' || coalesce(round(100.0 * sum((u->>'cached_input_tokens')::int)
  / nullif(sum((u->>'input_tokens')::int + (u->>'cached_input_tokens')::int + (u->>'cache_write_tokens')::int), 0)) || '%', 'no usage reported')
FROM (SELECT st.details->'usage' AS u FROM turn_stages st JOIN turns t ON t.id = st.turn_id AND t.workspace_id = st.workspace_id
      WHERE t.session_id = :'s' AND st.stage = 'model_reply') reported;
SQL
done
