---
name: verify
description: Run this project's full verification (format, lint with module boundaries, generated code, unit and database tests under the race detector, the TypeScript checks) and report the evidence. Use before calling any change done.
---

Run `scripts/check.sh`. It starts the compose database (Docker must be running) and runs, in order, stopping at the first failure, the checks CI runs. Run `npm ci` first if `node_modules` is missing; install sqlc if `scripts/sqlc.sh` asks for it.

1. `.githooks/pre-commit`: no committed file that git ignores, and the formatting of what is staged (Go, TypeScript, JSON). If formatting fails, run `scripts/format.sh` once, stage the result and re-check.
2. `python3 .claude/skills/doc-audit/check_docs.py`: links, labels, the decision index.
3. `golangci-lint run ./...`: static checks and module boundaries (depguard).
4. `scripts/sqlc.sh diff`: the generated code matches the queries and migrations. If not, run `scripts/sqlc.sh generate`.
5. `go test -race ./...` with `TEST_DATABASE_URL` set: unit tests and the store adapters' tests against PostgreSQL, under the race detector.
6. `scripts/lib/check-ts.sh`: TypeScript lint and format check, types and unit tests.

Report a table with each step, its command and pass or fail. For a failure, show the relevant tail of the output and the fix you propose. "All green" without the commands and their results doesn't count; say what was not run.
