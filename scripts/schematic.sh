#!/usr/bin/env bash
# The schematic, a clickable map of voice-guide from its services down to the types in its code.
#   scripts/schematic.sh         regenerates the class views from the Go code, then serves the page
#                                at http://127.0.0.1:5180 until Ctrl-C, reloading as its files change
#   scripts/schematic.sh check   regenerates, builds, and screenshots every level at 1440x900 into
#                                schematic/.shots/, failing on cards or labels that overlap or text
#                                cut off (needs Google Chrome)
# Needs `npm ci` once. Nothing else needs to be running.
set -euo pipefail
cd "$(dirname "$0")/.."

if [ ! -x node_modules/.bin/vite ]; then
  echo "schematic: JavaScript dependencies are not installed; run npm ci first." >&2
  exit 1
fi

# Each class view in the data names its module's root package and the file it is drawn from; the
# code sqlc generates (each store's db folder) is left out, as its store stands for it, and so are
# the fakes, which the schematic leaves off as it shows what runs.
node -e '
  const s = require("./schematic/data/schematic.json");
  for (const v of s.views) if (v.type === "classes") console.log(v.root + " " + v.source);
' | while read -r root source; do
  go run ./cmd/classgen -dir . -exclude '(/store/postgres/db|/fake)$' -o "schematic/data/$source" "./$root/..."
done

case "${1:-}" in
  "") exec npm run --silent dev --workspace schematic ;;
  check)
    npm run --silent build --workspace schematic >/dev/null
    cd schematic && exec node check.ts
    ;;
  *)
    echo "usage: scripts/schematic.sh [check]" >&2
    exit 2
    ;;
esac
