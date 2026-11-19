#!/usr/bin/env bash
# Plays the product's server: starts a session with the mock interviewer for the placeholder user,
# named by their reference in seed/placeholder-user/reference.txt, sending their resume from
# seed/placeholder-user/resume.md as session context, with the development product key from .env.
# Prints the session; its ticket, which the user's browser would be handed, goes under .tickets/
# (git ignores it), where turn.sh and end-session.sh beside this script find it by the session's ID.
# Run scripts/run.sh first, in another terminal.
#   .claude/skills/drive-session/start-session.sh                  a new session each time
#   .claude/skills/drive-session/start-session.sh REQUEST_ID       that request again: the same
#     session comes back, with a new ticket, as long as the resume file is unchanged (otherwise a 422)
set -euo pipefail
cd "$(dirname "$0")/../../.."
. scripts/lib/env.sh

agent=01a1de84-fbfc-7758-9af4-3ced6d8a90cc
request="${1:-$(python3 -c 'import uuid; print(uuid.uuid4())')}"
# Python writes the JSON, so quotes and line breaks in the resume arrive intact.
body=$(python3 -c '
import json, sys
print(json.dumps({"agent_id": sys.argv[1], "request_id": sys.argv[2], "user": open(sys.argv[3]).read().strip(),
                  "session_context": [{"name": "resume", "content": open(sys.argv[4]).read()}]}))
' "$agent" "$request" seed/placeholder-user/reference.txt seed/placeholder-user/resume.md)

echo "request_id: $request" >&2
umask 077
mkdir -p .tickets
# The key goes to curl as a config on its standard input, so it is on no command line.
printf 'header = "Authorization: Bearer %s"\n' "$VOICE_DEV_PRODUCT_KEY" |
  curl -sS -K - -H 'Content-Type: application/json' --data "$body" "http://${VOICE_ADDR:-127.0.0.1:8080}/v1/sessions" |
  python3 -c '
import json, os, sys
raw = sys.stdin.read()
try:
    reply = json.loads(raw)
except ValueError:
    reply = None
if isinstance(reply, dict) and "ticket" in reply:
    with open(os.path.join(".tickets", reply["id"]), "w") as f:
        f.write(reply.pop("ticket"))
    print(json.dumps(reply))
else:
    print(raw, end="" if raw.endswith("\n") else "\n")
'
