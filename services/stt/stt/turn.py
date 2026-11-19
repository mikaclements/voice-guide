"""The end-of-turn check: POST /v1/turn/check with up to 8 s of the user's latest audio, answered
with whether they have finished speaking. It sits outside the realtime API, so a speech-to-text
service swapped in for this one doesn't take the check with it.
"""

import asyncio
import logging
import time
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from typing import Any

import numpy as np
import onnxruntime
from aiohttp import web

from stt.features import SAMPLE_RATE, SECONDS, smart_turn_input

MAX_BYTES = SECONDS * SAMPLE_RATE * 2
# A check comes once per pause per connection and takes tens of milliseconds, so more than four
# waiting means checks come faster than they are made: past that, one is refused at once.
MAX_QUEUED = 4
# voice gives a check a second; one that waited longer has no caller left, so it isn't run.
STALE_AFTER = 1.0
# Smart Turn's own threshold: finished when the model finds it more likely than not.
THRESHOLD = 0.5

log = logging.getLogger(__name__)


class SmartTurn:
    """probability() says how likely the user has finished, from 16 kHz samples as float32 in
    [-1, 1]; it blocks."""

    def __init__(self, path: Path):
        self._session = onnxruntime.InferenceSession(str(path), providers=["CPUExecutionProvider"])

    def probability(self, audio: np.ndarray) -> float:
        # The model ends in a sigmoid, so its one output is already a probability.
        return self._session.run(None, {"input_features": smart_turn_input(audio)})[0].item()


def problem(status: int, title: str) -> web.Response:
    body = {"type": "about:blank", "title": title, "status": status}
    return web.json_response(body, status=status, content_type="application/problem+json")


def is_l16_mono_16khz(content_type: str) -> bool:
    """audio/L16 (RFC 2586) at 16 kHz with one channel, the parameters in any case and order."""
    kind, *parameters = [part.strip().lower() for part in content_type.split(";")]
    params = dict(p.partition("=")[::2] for p in parameters)
    return (
        kind == "audio/l16" and params.get("rate") == "16000" and params.get("channels", "1") == "1"
    )


MODEL = web.AppKey("turn_model", object)
CHECKS = web.AppKey("turn_checks", ThreadPoolExecutor)


async def check(request: web.Request) -> web.Response:
    if not is_l16_mono_16khz(request.headers.get("Content-Type", "")):
        return problem(415, "The body must be audio/L16;rate=16000, one channel.")
    # Read up to the limit only, however the body is sent, so a larger one costs no memory.
    body = bytearray()
    async for chunk in request.content.iter_chunked(64 * 1024):
        body += chunk
        if len(body) > MAX_BYTES:
            return problem(413, "At most 8 s of audio is checked.")
    if not body or len(body) % 2:
        return problem(400, "The body must be whole 16-bit samples.")
    # L16 samples are in network byte order.
    audio = np.frombuffer(body, dtype=">i2").astype(np.float32) / 32768
    try:
        probability = await queue(request.app, audio)
    except Refused as refused:
        return problem(503, str(refused))
    except Exception:
        log.error("the end-of-turn check failed")
        return problem(500, "The end-of-turn check failed.")
    return web.json_response({"finished": probability > THRESHOLD, "probability": probability})


class Refused(Exception):
    """A check refused before it runs: too many waiting, or waited too long."""


async def queue(app: web.Application, audio: np.ndarray) -> float:
    state = app[QUEUE]
    if state["queued"] >= state["max_queued"]:
        raise Refused("Too many checks are waiting.")
    queued_at = time.monotonic()

    def run() -> float:
        if time.monotonic() - queued_at > state["stale_after"]:
            raise Refused("The check waited past its caller's deadline.")
        return app[MODEL].probability(audio)

    state["queued"] += 1
    try:
        return await asyncio.get_running_loop().run_in_executor(app[CHECKS], run)
    finally:
        state["queued"] -= 1


QUEUE = web.AppKey("turn_queue", dict)


def register(
    application: web.Application,
    model: Any,
    max_queued: int = MAX_QUEUED,
    stale_after: float = STALE_AFTER,
) -> None:
    """Serves the check with a model like SmartTurn."""
    application[MODEL] = model
    application[QUEUE] = {"queued": 0, "max_queued": max_queued, "stale_after": stale_after}
    # Checks have a thread of their own, so one never waits behind recognition, which holds the
    # shared threads while its calls take turns; one, as a check takes tens of milliseconds and
    # comes once per pause.
    application[CHECKS] = ThreadPoolExecutor(1, thread_name_prefix="turn-check")

    async def stop_checks(app: web.Application) -> None:
        app[CHECKS].shutdown(wait=True)

    application.on_cleanup.append(stop_checks)
    application.router.add_post("/v1/turn/check", check)
