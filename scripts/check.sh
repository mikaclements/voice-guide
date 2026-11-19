#!/usr/bin/env bash
# Runs every check CI runs, including the tests that need PostgreSQL, against the compose database.
set -euo pipefail
cd "$(dirname "$0")/.."
docker compose up -d --wait postgres
export TEST_DATABASE_URL="${TEST_DATABASE_URL:-postgres://voice:voice@127.0.0.1:5432/postgres?sslmode=disable}"

# The hooks only run once turned on per clone; say so rather than let commits skip them unnoticed.
case "$(git config core.hooksPath)" in
  *.githooks) ;;
  *) echo "check: the git hooks are off in this clone; turn them on with: git config core.hooksPath .githooks" >&2 ;;
esac
echo "== commit checks" && .githooks/pre-commit
echo "== docs" && python3 scripts/lib/check_docs.py
echo "== lint" && golangci-lint run ./...
echo "== dead code" && scripts/lib/deadcode.sh
echo "== generated code is current" && scripts/sqlc.sh diff
echo "== Go tests, with the race detector and the database" && go test -race ./...
echo "== TypeScript" && scripts/lib/check-ts.sh
echo "== Python" && scripts/lib/check-py.sh
