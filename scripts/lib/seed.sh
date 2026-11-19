#!/usr/bin/env bash
# Adds the one workspace and the development product key in .env, if they aren't there yet; of the
# key, only its SHA-256 and first characters, as voice-guide keeps every key. The placeholder user
# isn't seeded: the first session a product starts for their reference adds them, and what a product
# knows about them (their resume) is sent as session context by .claude/skills/drive-session/start-session.sh and the
# web page's dev server, the way a product would. scripts/run.sh runs this, and the check scripts
# run it on their own databases.
#   scripts/lib/seed.sh              the database the compose service runs, voice
#   SEED_DB=name scripts/lib/seed.sh another database of it
set -euo pipefail
cd "$(dirname "$0")/../.."
. scripts/lib/env.sh
# Hashed here, fed through a pipe by the shell's own printf, so the key itself is on no command line
# and in no SQL; the first 12 characters are what voice-guide keeps to tell keys apart.
key_hash="$(printf %s "$VOICE_DEV_PRODUCT_KEY" | shasum -a 256 | cut -d' ' -f1)"
key_display="${VOICE_DEV_PRODUCT_KEY:0:12}"
docker compose exec -T postgres psql -q -1 -v ON_ERROR_STOP=1 -U voice -d "${SEED_DB:-voice}" \
  -v workspace="$VOICE_WORKSPACE_ID" -v key_hash="$key_hash" -v key_display="$key_display" <scripts/lib/seed.sql
