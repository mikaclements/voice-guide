#!/usr/bin/env bash
# Fails on Go functions nothing uses: none that no program or test reaches, and none in
# production code that only tests reach. The test fakes and the test database helper are
# reached only by tests by design, so they are left out of the second check.
# deadcode's version is the tool line in go.mod.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

status=0

# With the tests as roots too: a function here is called by nothing at all.
unused=$(go tool deadcode -test ./...)
if [ -n "$unused" ]; then
  echo "deadcode: functions nothing calls, not even a test:" >&2
  echo "$unused" >&2
  status=1
fi

# With only the programs as roots: a function here outside the fakes and the test helpers (pgtest, redacttest) is production
# code kept alive by its tests alone.
from_programs=$(go tool deadcode ./...)
test_only=$(grep -Ev '^internal/(.+/)?fake/|^internal/pgtest/|^internal/redacttest/' <<<"$from_programs" || true)
if [ -n "$test_only" ]; then
  echo "deadcode: production functions only tests call (delete them, or move them into a fake):" >&2
  echo "$test_only" >&2
  status=1
fi

exit "$status"
