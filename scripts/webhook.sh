#!/usr/bin/env bash
# Sets, rotates and shows the workspace's webhook: where its product's server hears that its
# sessions ended, each delivery signed with the webhook's secret.
#   scripts/webhook.sh set URL    posts each ended session to URL (https, or http on this machine)
#                                 from now on, starting deliveries again if a 410 Gone stopped them;
#                                 the first time, prints the secret, the only time it is shown
#   scripts/webhook.sh rotate     prints a new secret, the only time it is shown; the old one signs
#                                 nothing more
#   scripts/webhook.sh show       the URL, and whether deliveries are stopped; never the secret
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/env.sh
exec go run ./cmd/webhook "$@"
