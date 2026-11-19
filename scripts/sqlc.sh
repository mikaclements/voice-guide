#!/usr/bin/env bash
# Runs sqlc at the version in .sqlc-version: `scripts/sqlc.sh generate` after changing a query or a
# migration, `scripts/sqlc.sh diff` to check the generated code is current (CI does).
set -euo pipefail
cd "$(dirname "$0")/.."
want="v$(cat .sqlc-version)"
sqlc="$(command -v sqlc || echo "$(go env GOPATH)/bin/sqlc")"
if [ ! -x "$sqlc" ] || [ "$("$sqlc" version)" != "$want" ]; then
  echo "sqlc $want is required: go install github.com/sqlc-dev/sqlc/cmd/sqlc@$want" >&2
  exit 1
fi
exec "$sqlc" "$@"
