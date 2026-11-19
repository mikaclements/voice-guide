#!/usr/bin/env bash
# Publishes the agent files in agents/ to the workspace in .env: each changed file becomes its
# agent's next version. Run it whenever you change an agent; scripts/run.sh runs it at start.
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/env.sh
exec go run ./cmd/publish
