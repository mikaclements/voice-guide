#!/usr/bin/env bash
# Sourced by scripts/run.sh and scripts/up.sh after scripts/lib/env.sh. need_api_key stops a start
# on the Claude API with no key before anything starts, so a first run stops at once instead of after
# the downloads. At a terminal the key is asked for, unseen and so kept out of the shell's history,
# and saved into .env; Enter alone picks free fake replies instead. Elsewhere it says what to
# do, ending with the words given, if any.
need_api_key() {
  if [ "$MODEL_PROVIDER" != claude-api ] || [ -n "${ANTHROPIC_API_KEY:-}" ]; then return 0; fi
  # Asked only when both the keyboard and the screen are this terminal: with output going to a
  # file, the prompt would land there and the run would look hung.
  if [ ! -t 0 ] || [ ! -t 2 ]; then
    echo "replies come from the Claude API, which needs a key: add ANTHROPIC_API_KEY=<your key> to .env" >&2
    echo "(a key from the Claude Console), or start with MODEL_PROVIDER=fake to try it free with fake" >&2
    echo "replies${1:+, $1}" >&2
    exit 1
  fi
  read -rsp "Claude API key (from the Claude Console; Enter for free fake replies): " ANTHROPIC_API_KEY
  echo >&2
  if [ -n "$ANTHROPIC_API_KEY" ]; then
    env_save ANTHROPIC_API_KEY
    echo "saved ANTHROPIC_API_KEY to .env" >&2
  else
    MODEL_PROVIDER=fake
    env_save MODEL_PROVIDER
    echo "saved MODEL_PROVIDER=fake to .env: every reply is a free fake line; for Claude, put" >&2
    echo "ANTHROPIC_API_KEY=<your key> in .env and delete the MODEL_PROVIDER=fake line" >&2
  fi
}
