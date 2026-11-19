#!/usr/bin/env bash
# The Python checks, as the push hook and CI run them: Ruff's lint and format check, then the
# tests. uv installs the Python in .python-version and the packages the lockfile holds; a missing
# uv fails loudly instead of skipping the checks.
set -euo pipefail
cd "$(dirname "$0")/../../services/stt"

if ! command -v uv >/dev/null; then
  echo "check-py: uv is not installed; it runs the Python checks." >&2
  exit 1
fi
# Kokoro on MLX (scripts/lib/kokoro-mlx) is linted here too, but neither installed nor run: MLX
# needs Apple silicon, and CI runs on Linux.
uv run --quiet --locked ruff check . ../../scripts/lib/kokoro-mlx
uv run --quiet --locked ruff format --check . ../../scripts/lib/kokoro-mlx
uv run --quiet --locked pytest -q
