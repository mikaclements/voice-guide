#!/usr/bin/env bash
# Makes, lists and revokes the workspace's product keys, which a product's server sends to start
# sessions; voice-guide keeps only each key's hash.
#   scripts/product-key.sh create       prints a new key, the only time it is shown
#   scripts/product-key.sh list         each key's ID, first characters, and when made and revoked
#   scripts/product-key.sh revoke ID    refuses the key from now on, warning when no working key is
#                                       left; a start already past its key check finishes, and
#                                       sessions already started go on
set -euo pipefail
cd "$(dirname "$0")/.."
. scripts/lib/env.sh
exec go run ./cmd/productkey "$@"
