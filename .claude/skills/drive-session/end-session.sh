#!/usr/bin/env bash
# Plays the user's browser ending a session with its ticket, which start-session.sh beside it kept:
# voice takes no more turns in it, and prints the session with when it ended. Ending it again
# prints the same.
# Run scripts/run.sh first, and .claude/skills/drive-session/start-session.sh for a session's ID.
#   .claude/skills/drive-session/end-session.sh SESSION_ID
set -euo pipefail
cd "$(dirname "$0")/../../.."
. scripts/lib/env.sh

ticket=".tickets/$1"
[ -f "$ticket" ] || { echo "no ticket for session $1: .claude/skills/drive-session/start-session.sh keeps it under .tickets/" >&2; exit 1; }
# The ticket goes to curl as a config on its standard input, so it is on no command line.
printf 'header = "Authorization: Bearer %s"\n' "$(cat "$ticket")" |
  curl -sS -K - -w '\n' -X POST "http://${VOICE_ADDR:-127.0.0.1:8080}/v1/sessions/$1/end"
