#!/usr/bin/env bash
# Makes a workspace besides the one scripts/run.sh seeds, so each product on voice-guide has its own
# agents, product keys and webhook.
#   scripts/workspace.sh create NAME   adds the workspace and prints its ID and its first product
#                                      key, the only time the key is shown
# The other scripts act on the workspace in VOICE_WORKSPACE_ID, so give them its ID:
#   VOICE_AGENTS_DIR=<the product's agents folder> VOICE_WORKSPACE_ID=<id> scripts/publish.sh
# (the product's agent files live outside agents/, each with its own ID: an agent's ID is unique
# across workspaces); product-key.sh and webhook.sh take VOICE_WORKSPACE_ID too.
# It acts on the database scripts/run.sh uses, so start scripts/run.sh first.
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/env.sh
if [ "$#" -ne 2 ] || [ "$1" != create ] || [ -z "$2" ]; then
  echo "usage: scripts/workspace.sh create NAME" >&2
  exit 2
fi
# The name goes in as a psql variable, quoted by psql, never spliced into the SQL here.
id="$(docker compose exec -T postgres psql -qAt -v ON_ERROR_STOP=1 -U voice -d voice -v name="$2" \
  <<<"INSERT INTO workspaces (id, name) VALUES (uuidv7(), :'name') RETURNING id;")"
echo "workspace $id"
key="$(VOICE_WORKSPACE_ID="$id" go run ./cmd/productkey create)"
echo "product key $key"
