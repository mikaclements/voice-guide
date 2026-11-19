#!/usr/bin/env bash
# Sourced by the development scripts: loads the settings in .env, first creating it from
# .env.example with a fresh random signing key. Git ignores .env, so the key never leaves the machine.

# env_save NAME...: saves each named setting from this shell's variables into .env. It appends the
# setting and drops any uncommented line for the same name; comments stay, as one may mention the
# name. The file is readable only by this user and is written whole or not at all. Values are quoted
# for the shell and never go through sed, since a key may hold any character.
env_save() {
  local name line
  # A leftover .env.tmp keeps its old mode when written over, so it goes first.
  rm -f .env.tmp
  (set -o pipefail && umask 077 && {
    while IFS= read -r line || [ -n "$line" ]; do
      for name in "$@"; do
        case "$line" in "$name="* | "export $name="*) continue 2 ;; esac
      done
      printf '%s\n' "$line"
    done <.env
    for name in "$@"; do printf '%s=%q\n' "$name" "${!name}"; done
  } >.env.tmp) && mv .env.tmp .env || { rm -f .env.tmp; return 1; }
}
if [ ! -f .env ]; then
  key="$(openssl rand -hex 32)"
  if [ -z "$key" ]; then
    echo "could not generate a signing key (is openssl installed?)" >&2
    return 1
  fi
  # Readable only by this user, and written whole or not at all. Nothing after this runs on a
  # failure, so no step leaves a .env that holds less.
  rm -f .env.tmp
  if ! (umask 077 && sed "s/^VOICE_TICKET_KEY=.*/VOICE_TICKET_KEY=$key/" .env.example >.env.tmp && mv .env.tmp .env); then
    rm -f .env.tmp
    echo "could not create .env from .env.example" >&2
    return 1
  fi
  echo "created .env with a new signing key" >&2
  # A script sets save_given_settings (scripts/run.sh and scripts/up.sh do) to keep, on a first run,
  # the settings given in the shell, as in KOKORO=mlx scripts/run.sh. Later runs need none.
  if [ -n "${save_given_settings:-}" ]; then
    given=()
    for env_name in ANTHROPIC_API_KEY KOKORO MODEL_PROVIDER; do
      if printenv "$env_name" >/dev/null; then given+=("$env_name"); fi
    done
    if [ "${#given[@]}" -gt 0 ]; then
      env_save "${given[@]}" || return 1
      echo "saved to .env: ${given[*]}" >&2
    fi
    unset given
  fi
fi
# The development product key, which scripts/lib/seed.sh registers for the workspace. It is added
# once, also to a .env made before keys existed, in the form voice-guide makes keys: vgk_ and 32
# random bytes in base64url.
if ! grep -q '^VOICE_DEV_PRODUCT_KEY=' .env; then
  product_key="$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')"
  if [ "${#product_key}" -ne 43 ]; then
    echo "could not generate a product key (is openssl installed?)" >&2
    return 1
  fi
  echo "VOICE_DEV_PRODUCT_KEY=vgk_$product_key" >> .env
  unset product_key
  echo "added a development product key to .env" >&2
fi
# A value already in the environment wins over .env's. So a setting given on the command line, such
# as a port moved off a taken one, holds in this script and in every script it runs.
env_given=""
for env_name in $(sed -n 's/^\(export \)\{0,1\}\([A-Za-z_][A-Za-z0-9_]*\)=.*/\2/p' .env); do
  if printenv "$env_name" >/dev/null; then env_given+="export $env_name=$(printf %q "${!env_name}");"; fi
done
set -a
shell_key=""
printenv ANTHROPIC_API_KEY >/dev/null && shell_key="$ANTHROPIC_API_KEY"
. ./.env
set +a
# A key exported in the shell, for other tools say, wins over the one in .env like any setting. Say
# so when the two differ, or the wrong account is billed without any sign.
if [ -n "$shell_key" ] && [ -n "${ANTHROPIC_API_KEY:-}" ] && [ "$shell_key" != "$ANTHROPIC_API_KEY" ]; then
  echo "ANTHROPIC_API_KEY is exported in your shell and differs from the one in .env; using the shell's (unset it to use .env's)" >&2
fi
unset shell_key
eval "$env_given"
unset env_given env_name
# Only the model service gets the API key. It stays in this shell but is not exported, so nothing
# else a script starts (voice, the seed, the page's build) holds it. A script that starts the model
# service passes the key on that one command.
export -n ANTHROPIC_API_KEY
# The compose database, unless .env names another.
: "${DATABASE_URL:=postgres://voice:voice@127.0.0.1:5432/voice?sslmode=disable}"
export DATABASE_URL
# The model service's address for a URL: an IPv6 address goes in brackets. It is never localhost,
# because curl tries ::1 first, and a process on the IPv6 side of the port would answer instead.
model_host="${MODEL_HOST:-127.0.0.1}"
[[ "$model_host" != *:* ]] || model_host="[$model_host]"
