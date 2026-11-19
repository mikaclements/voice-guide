#!/usr/bin/env bash
# The project's one formatting entry point, used by people, the git hooks and the Claude Code hook.
# Go goes through golangci-lint fmt; TypeScript and JSON through Biome (run `npm ci` once first);
# Python through Ruff, at the version services/stt's lockfile holds (needs uv).
#   scripts/format.sh            format everything
#   scripts/format.sh --check    fail if anything is not formatted (prints the diff)
#   scripts/format.sh FILE...    format the given files; files no formatter covers are skipped
set -euo pipefail

usage() {
  echo "usage: scripts/format.sh [--check | FILE...]" >&2
  exit 2
}

# A missing Biome fails loudly: skipping it would let unformatted TypeScript through the hooks and CI.
biome() {
  if [ ! -x node_modules/.bin/biome ]; then
    echo "scripts/format.sh: Biome is not installed; run npm ci first." >&2
    exit 1
  fi
  node_modules/.bin/biome "$@"
}

# The same for a missing uv, which runs Ruff.
ruff() {
  if ! command -v uv >/dev/null; then
    echo "scripts/format.sh: uv is not installed; it runs Ruff on the Python files." >&2
    exit 1
  fi
  # Ruff at the version uv.lock holds, in a tool environment of uv's own, so checking a commit
  # uses the version being committed and never syncs the developer's environment to a lockfile
  # half staged.
  local version
  version="$(awk '/^name = "ruff"$/ { getline; split($0, v, "\""); print v[2] }' services/stt/uv.lock)"
  uvx --quiet "ruff@$version" "$@"
}

if [ "${1:-}" = "--check" ]; then
  [ $# -eq 1 ] || usage
  cd "$(dirname "$0")/.."
  golangci-lint fmt --diff
  biome format .
  ruff format --diff services/stt scripts/lib/kokoro-mlx
  exit 0
fi

go_files=()
biome_files=()
python_files=()
for file in "$@"; do
  case "$file" in
    -*) usage ;;
    *.go) go_files+=("$(cd "$(dirname "$file")" && pwd)/$(basename "$file")") ;;
    *.ts | *.tsx | *.json) biome_files+=("$(cd "$(dirname "$file")" && pwd)/$(basename "$file")") ;;
    *.py) python_files+=("$(cd "$(dirname "$file")" && pwd)/$(basename "$file")") ;;
  esac
done
cd "$(dirname "$0")/.."

if [ $# -eq 0 ]; then
  golangci-lint fmt
  biome format --write .
  ruff format services/stt scripts/lib/kokoro-mlx
  exit 0
fi
if [ ${#go_files[@]} -gt 0 ]; then
  # golangci-lint fmt only warns on a syntax error; gofmt fails on one, so the editor hook can report it.
  gofmt -e -l "${go_files[@]}" >/dev/null
  golangci-lint fmt "${go_files[@]}"
fi
if [ ${#biome_files[@]} -gt 0 ]; then
  biome format --write --no-errors-on-unmatched "${biome_files[@]}"
fi
if [ ${#python_files[@]} -gt 0 ]; then
  ruff format "${python_files[@]}"
fi
