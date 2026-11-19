#!/usr/bin/env bash
# Applies the migrations the database in .env hasn't run (the compose database unless .env names
# another). scripts/run.sh runs it at start; a deploy runs the same command once, before new
# instances take traffic.
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/env.sh
exec go run ./cmd/migrate
