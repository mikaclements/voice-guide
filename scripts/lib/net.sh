#!/usr/bin/env bash
# Sourced by the scripts that start things on a port or wait for them: the free-port check, the wait
# for an HTTP answer and the Apple-silicon preflight, in one place so every script says and does the
# same.

# Prints the name of the process listening on a TCP port, and nothing when the port is free.
port_holder() {
  lsof -nP -iTCP:"$1" -sTCP:LISTEN -Fc 2>/dev/null | sed -n 's/^c//p' | head -n 1 || true
}

# port_taken PORT: succeeds when something listens on the port.
port_taken() {
  [ -n "$(port_holder "$1")" ]
}

# require_free_port PORT HINT: exits 1 unless the port is free, saying how to free it or move to
# another, HINT finishing the sentence ("set MODEL_PORT").
require_free_port() {
  if port_taken "$1"; then
    echo "port $1 is taken; stop what holds it (lsof -nP -iTCP:$1 -sTCP:LISTEN) or $2" >&2
    exit 1
  fi
}

# wait_http [-f] [-p PID] [-n TRIES] [-i SECONDS] URL: succeeds once URL answers, polling every
# SECONDS (default 0.2), at most TRIES times (default 100; 0 is forever). Any answer counts, or only
# a successful one with -f. Fails early when the process PID is no longer running.
wait_http() {
  local curl_flags=-s pid="" tries=100 interval=0.2 opt n=0 OPTIND=1
  while getopts fp:n:i: opt; do
    case "$opt" in
    f) curl_flags=-sf ;;
    p) pid="$OPTARG" ;;
    n) tries="$OPTARG" ;;
    i) interval="$OPTARG" ;;
    *) return 2 ;;
    esac
  done
  shift $((OPTIND - 1))
  while [ "$tries" -eq 0 ] || [ "$n" -lt "$tries" ]; do
    n=$((n + 1))
    [ -z "$pid" ] || kill -0 "$pid" 2>/dev/null || return 1
    curl "$curl_flags" -o /dev/null "$1" && return 0
    sleep "$interval"
  done
  return 1
}

# require_mlx_host APPLE_MESSAGE MACOS_MESSAGE UV_MESSAGE: exits 1 with the matching message unless
# this is macOS 14 or later on Apple silicon with uv installed, which Kokoro on MLX needs.
require_mlx_host() {
  # uname -m says x86_64 for a shell running under Rosetta, even on Apple silicon.
  if [ "$(uname -s)" != Darwin ] || [ "$(sysctl -n hw.optional.arm64 2>/dev/null)" != 1 ]; then
    echo "$1" >&2
    exit 1
  fi
  # The oldest macOS MLX's packages are built for.
  if [ "$(sw_vers -productVersion | cut -d. -f1)" -lt 14 ]; then
    echo "$2" >&2
    exit 1
  fi
  if ! command -v uv >/dev/null; then
    echo "$3" >&2
    exit 1
  fi
}
