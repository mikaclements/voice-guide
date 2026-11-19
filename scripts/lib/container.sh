#!/bin/sh
# The voice image's entry, which compose's app profile runs (scripts/up.sh):
#   container.sh setup   once, before voice: makes the database in PGDATABASE if it isn't there,
#                        migrates it, adds the workspace and the development product key as
#                        scripts/lib/seed.sh does, and publishes the agents in agents/
#   container.sh voice   voice, taking audio on this container's own address, where the audio port
#                        published on the host arrives
set -eu
case "${1:-}" in
setup)
  psql -q -v ON_ERROR_STOP=1 -d postgres -v db="$PGDATABASE" <<'SQL'
SELECT format('CREATE DATABASE %I', :'db')
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = :'db')\gexec
SQL
  migrate
  # Hashed by the shell's own printf, so the key is on no command line and in no SQL.
  key_hash="$(printf %s "$VOICE_DEV_PRODUCT_KEY" | sha256sum | cut -d' ' -f1)"
  key_display="$(printf %s "$VOICE_DEV_PRODUCT_KEY" | cut -c1-12)"
  psql -q -1 -v ON_ERROR_STOP=1 -v workspace="$VOICE_WORKSPACE_ID" -v key_hash="$key_hash" \
    -v key_display="$key_display" -f scripts/lib/seed.sql
  exec publish
  ;;
voice)
  # Its IPv4 address on compose's network; the answer names the host's loopback in its place.
  VOICE_AUDIO_IPS="$(getent ahostsv4 "$(cat /etc/hostname)" | awk 'NR == 1 { print $1 }')"
  export VOICE_AUDIO_IPS
  exec voice
  ;;
*)
  echo "usage: container.sh setup|voice" >&2
  exit 2
  ;;
esac
