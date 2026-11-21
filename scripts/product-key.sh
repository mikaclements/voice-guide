#!/usr/bin/env bash
# Makes, lists and revokes the workspace's product keys, which a product's server sends as its
# credential; voice-guide keeps only each key's hash.
#   scripts/product-key.sh create [--permissions run_sessions,publish_agents]
#                                       prints a new key, the only time it is shown, holding the
#                                       permissions named, run_sessions unless any are; to change
#                                       them, make a new key and revoke the old one
#   scripts/product-key.sh list         each key's ID, first characters, permissions, and when made
#                                       and revoked
#   scripts/product-key.sh revoke ID    refuses the key from now on, warning when no working key is
#                                       left; a start already past its key check finishes, and
#                                       sessions already started go on
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/env.sh
exec go run ./cmd/productkey "$@"
