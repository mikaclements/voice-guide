"""Fetches one size of Moonshine's models and Smart Turn, each file checked against its SHA-256
(Moonshine's in models.sha256), so a build gets the files that were measured or fails.
  python -m stt.models DEST SIZE    writes DEST/SIZE/<file> and DEST/smart-turn-v3.2-cpu.onnx
"""

import hashlib
import shutil
import sys
import urllib.request
from pathlib import Path

BASE_URL = "https://download.moonshine.ai/model/"
CHECKSUMS = Path(__file__).parent.parent / "models.sha256"
# A host that sends nothing for a minute has stalled, so the build fails instead of hanging.
FETCH_TIMEOUT = 60
# Smart Turn v3.2 for the CPU (int8, BSD-2-Clause), at a fixed revision of its repository.
SMART_TURN = "smart-turn-v3.2-cpu.onnx"
SMART_TURN_URL = (
    "https://huggingface.co/pipecat-ai/smart-turn-v3/resolve/"
    f"f766f81d3cfdf7737ac64aad813d91bbfd56bf93/{SMART_TURN}"
)
SMART_TURN_SHA256 = "2bb026316b14a660486a75b1733cd3fbab8c2fd0314dc9af7be49f8cca967e4f"


def wanted(size: str, checksums: str) -> list[tuple[str, str]]:
    """The (SHA-256, path) of each file of one size, from lines of `sha256  path`."""
    files = []
    for line in checksums.splitlines():
        digest, path = line.split()
        if path.startswith(f"{size}-streaming-en/"):
            files.append((digest, path))
    if not files:
        raise ValueError(f"no model files listed for size {size!r}")
    return files


def check(path: Path, digest: str) -> None:
    actual = hashlib.sha256(path.read_bytes()).hexdigest()
    if actual != digest:
        path.unlink()
        raise ValueError(f"{path.name}: SHA-256 {actual}, expected {digest}")


def download(url: str, target: Path, digest: str) -> None:
    # Moonshine's host refuses Python's default user agent.
    request = urllib.request.Request(url, headers={"User-Agent": "stt-models"})
    with (
        urllib.request.urlopen(request, timeout=FETCH_TIMEOUT) as response,
        target.open("wb") as file,
    ):
        shutil.copyfileobj(response, file)
    check(target, digest)


def fetch(dest: Path, size: str) -> None:
    (dest / size).mkdir(parents=True, exist_ok=True)
    for digest, path in wanted(size, CHECKSUMS.read_text()):
        download(BASE_URL + path, dest / size / Path(path).name, digest)
    download(SMART_TURN_URL, dest / SMART_TURN, SMART_TURN_SHA256)


if __name__ == "__main__":
    fetch(Path(sys.argv[1]), sys.argv[2])
