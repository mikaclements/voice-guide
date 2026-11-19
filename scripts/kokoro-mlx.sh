#!/usr/bin/env bash
# Runs Kokoro natively on this Mac's GPU through Apple's MLX (services/kokoro-mlx/server.py), as
# a stand-in for a dedicated text-to-speech server with a GPU. Unlike PyTorch on the Apple GPU, MLX
# takes no extra time the first time it meets a phrase of a new length, and its memory stays flat.
# The same model and voice (af_heart) behind the part of Kokoro-FastAPI's API voice uses, Ogg Opus
# included, so voice doesn't change; point VOICE_TTS_URL at it (scripts/run.sh does with
# KOKORO=mlx).
# The first run installs about 0.6 GB of Python packages, each at a fixed version and hash, and
# 0.3 GB of model at a fixed revision, with progress shown; later runs reuse them, and the server
# never goes online. Stays in the foreground; Ctrl-C stops it. To remove everything it installed,
# delete the folder under the cache path below (uv's own cache keeps its downloaded packages;
# `uv cache clean` empties it).
#   scripts/kokoro-mlx.sh
# Needs macOS on Apple silicon and uv (https://docs.astral.sh/uv/).
set -euo pipefail
cd "$(dirname "$0")/../services/kokoro-mlx"

# The model's commit on Hugging Face, so every install gets the same weights and voices.
MODEL=mlx-community/Kokoro-82M-bf16
MODEL_REVISION=a71e4d38b236d968966a2002c4c895dbd12b1c3c
# 8882, next to the container's 8880, so both can run at once for a comparison.
port="${KOKORO_MLX_PORT:-8882}"
dir="${XDG_CACHE_HOME:-$HOME/Library/Caches}/voice-guide/kokoro-mlx"
log="$(dirname "$dir")/kokoro-mlx.log"

server=""
# Stops the server and waits until it is gone, so the port is free once this script ends; gives up
# after 10 s (a round guess; it stops in under 1 s).
trap '[ -z "$server" ] || { kill "$server" 2>/dev/null || true; for _ in $(seq 50); do kill -0 "$server" 2>/dev/null || break; sleep 0.2; done; wait "$server" 2>/dev/null || true; }' EXIT
trap 'exit 130' INT TERM

# uname -m says x86_64 for a shell running under Rosetta, even on Apple silicon.
if [ "$(uname -s)" != Darwin ] || [ "$(sysctl -n hw.optional.arm64 2>/dev/null)" != 1 ]; then
  echo "Kokoro on MLX needs macOS on Apple silicon; use the container (docker compose --profile tts up kokoro)" >&2
  exit 1
fi
# The oldest macOS MLX's packages are built for.
if [ "$(sw_vers -productVersion | cut -d. -f1)" -lt 14 ]; then
  echo "Kokoro on MLX needs macOS 14 or later; use the container (docker compose --profile tts up kokoro)" >&2
  exit 1
fi
if ! command -v uv >/dev/null; then
  echo "uv isn't installed; install it first (brew install uv)" >&2
  exit 1
fi
if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null; then
  echo "port $port is taken; stop what holds it (lsof -nP -iTCP:$port -sTCP:LISTEN) or set KOKORO_MLX_PORT" >&2
  exit 1
fi

# Exactly the packages in requirements.txt, each checked against its hash, wheels only so nothing
# builds from source; the Python is the one in .python-version here, for Apple silicon even when
# this shell runs under Rosetta. The test is for python, not the folder, so an interrupted first run
# is redone. The model's folder is marked with its revision once
# complete, so an interrupted or older download is fetched again.
# The note shows only when something will be installed: no venv yet, packages last synced from
# another requirements.txt or Python (the venv keeps a fingerprint of both), or the model not yet at
# its revision.
packages="$(cat .python-version requirements.txt | shasum -a 256)"
if [ ! -x "$dir/venv/bin/python" ] || [ "$(cat "$dir/venv/.packages" 2>/dev/null)" != "$packages" ] ||
  [ "$(cat "$dir/model/.revision" 2>/dev/null)" != "$MODEL_REVISION" ]; then
  echo "installing Kokoro on MLX: locked packages and the model (first run only: about 0.9 GB, a few minutes)"
fi
mkdir -p "$dir"
[ -x "$dir/venv/bin/python" ] || uv venv --quiet --python "cpython-$(cat .python-version)-macos-aarch64" "$dir/venv"
uv pip sync --python "$dir/venv/bin/python" --require-hashes --only-binary :all: requirements.txt
echo "$packages" >"$dir/venv/.packages"
if [ "$(cat "$dir/model/.revision" 2>/dev/null)" != "$MODEL_REVISION" ]; then
  "$dir/venv/bin/python" -c 'import sys
from huggingface_hub import snapshot_download
snapshot_download(sys.argv[1], revision=sys.argv[2], local_dir=sys.argv[3],
                  allow_patterns=["config.json", "kokoro-v1_0.safetensors", "voices/*.safetensors"])' \
    "$MODEL" "$MODEL_REVISION" "$dir/model"
  echo "$MODEL_REVISION" >"$dir/model/.revision"
fi

echo "starting Kokoro on MLX at http://127.0.0.1:$port (log: $log)"
# Offline, so nothing is fetched at runtime that the install didn't pin. Standard output carries
# only the libraries' prints, which can quote a phrase; the log gets the server's lines and errors.
HF_HUB_OFFLINE=1 "$dir/venv/bin/python" server.py "$dir/model" "$port" >/dev/null 2>"$log" &
server=$!

# Only the model load and one phrase spoken are timed now that the downloads are done: 60 s, a round
# guess (about 2 s here).
for _ in $(seq 300); do
  kill -0 "$server" 2>/dev/null || break
  if curl -sf -o /dev/null "http://127.0.0.1:$port/health"; then
    echo "Kokoro is ready at http://127.0.0.1:$port"
    wait "$server" || true
    echo "Kokoro stopped; its log: $log" >&2
    exit 1
  fi
  sleep 0.2
done
echo "Kokoro didn't start; the end of its log:" >&2
tail -n 20 "$log" >&2
exit 1
