#!/usr/bin/env bash
# The TypeScript checks, as the push hook and CI run them: Biome's lint and format check, the type
# check, the tests, then the builds of the page and the schematic, which the type check alone doesn't prove. A missing install fails loudly instead of skipping the checks.
set -euo pipefail
cd "$(dirname "$0")/../.."

if [ ! -x node_modules/.bin/biome ]; then
  echo "check-ts: JavaScript dependencies are not installed; run npm ci first." >&2
  exit 1
fi
node_modules/.bin/biome ci .
npm run --silent typecheck --workspaces
npm test --silent --workspaces
node --test examples/job-board/*.test.mjs
npm run --silent build --workspace web >/dev/null
npm run --silent build --workspace schematic >/dev/null
