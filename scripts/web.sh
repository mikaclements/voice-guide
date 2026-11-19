#!/usr/bin/env bash
# Serves the web page at http://127.0.0.1:5173 for development, reloading as its files change. The
# page calls voice through this server, at VOICE_ADDR (127.0.0.1:8080 unless .env or the
# environment says otherwise), and this server plays the product: it starts each session with the
# development product key for the placeholder user and their session context from seed/, and hands
# the page the session and its ticket. scripts/run.sh serves the page too; this serves it alone, as
# when voice runs elsewhere, or to restart the page by itself: started first, it keeps the port and
# scripts/run.sh leaves the page to it.
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/env.sh
exec npm run --silent dev --workspace web
