#!/usr/bin/env bash
# Builds the drop-in client, which voice serves at /client/voice-guide.js, into web/dist-client/
# (git ignores it), when it is missing or older than anything it is built from: the page's code in
# web/ and the locked packages. scripts/run.sh runs it before starting voice, so a fresh clone
# serves the client; run it after changing the client while voice runs, which serves the new build
# at the next load. The build goes to a folder of its own first and its file is then moved into
# place in one step, so a load during a build gets the old script or the new one, whole, never a
# missing or half-written one.
set -euo pipefail
cd "$(dirname "$0")/.."

out=web/dist-client/voice-guide.js
if [ -f "$out" ] && [ -z "$(find web package-lock.json \( -path web/node_modules -o -path web/dist -o -path web/dist-client \) -prune -o -type f -newer "$out" -print -quit)" ]; then
  exit 0
fi
# Inside web/dist-client, so the move is a rename on one filesystem, and voice serves nothing else
# from there.
mkdir -p web/dist-client
build=$(mktemp -d "$PWD/web/dist-client/.build.XXXXXX")
trap 'rm -rf "$build"' EXIT
npm run --silent build:client --workspace web -- --outDir "$build"
mv -f "$build/voice-guide.js" "$out"
