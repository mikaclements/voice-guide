#!/usr/bin/env bash
# PostToolUse hook: after Claude edits a file, format it with the shared project formatter.
# Thin adapter: scripts/format.sh decides which files it covers and is used by people and the git hooks too.
set -uo pipefail

file=$(jq -r '.tool_input.file_path // empty')
[[ -n "$file" && -f "$file" ]] || exit 0
script="${CLAUDE_PROJECT_DIR:-.}/scripts/format.sh"
[ -x "$script" ] || exit 0

if ! out=$("$script" "$file" 2>&1); then
  echo "Formatting failed for $file (usually a syntax error):" >&2
  echo "$out" | tail -20 >&2
  exit 2
fi
