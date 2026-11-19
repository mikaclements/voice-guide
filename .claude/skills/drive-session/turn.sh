#!/usr/bin/env bash
# Plays the user's browser for one typed turn: sends the words to a session with the ticket
# start-session.sh beside it kept and a fresh request ID, prints the reply's text as it streams, then
# the final event (done with the turn's ID and number, or lost) and, on standard error, the time to
# the first text. A refused turn prints its problem.
# Run scripts/run.sh first, and .claude/skills/drive-session/start-session.sh for a session's ID.
#   .claude/skills/drive-session/turn.sh SESSION_ID "words"
set -euo pipefail
cd "$(dirname "$0")/../../.."
. scripts/lib/env.sh

session="$1"
body=$(python3 -c 'import json, sys, uuid; print(json.dumps({"request_id": str(uuid.uuid4()), "words": sys.argv[1]}))' "$2")
ticket=".tickets/$session"
[ -f "$ticket" ] || { echo "no ticket for session $session: .claude/skills/drive-session/start-session.sh keeps it under .tickets/" >&2; exit 1; }
# The ticket goes to curl as a config on its standard input, so it is on no command line.
printf 'header = "Authorization: Bearer %s"\n' "$(cat "$ticket")" |
  curl -sSN -K - -H 'Content-Type: application/json' --data "$body" \
    "http://${VOICE_ADDR:-127.0.0.1:8080}/v1/sessions/$session/turns" | python3 -u -c '
import json, sys, time
start, first, event = time.monotonic(), None, None
for line in sys.stdin:
    line = line.rstrip("\n")
    if line.startswith("event: "):
        event = line[7:]
    elif line.startswith("data: ") and event == "text":
        if first is None:
            first = time.monotonic()
        print(json.loads(line[6:])["text"], end="", flush=True)
    elif line.startswith("data: "):
        print(f"\n{event} {line[6:]}")
    elif line:
        print(line)
if first is not None:
    print(f"first text after {first - start:.2f}s", file=sys.stderr)
'
