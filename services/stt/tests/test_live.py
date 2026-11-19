"""The running service against speech Kokoro makes, streamed in at the pace it is spoken. Skipped
unless TEST_STT_URL and TEST_KOKORO_URL name the two; .claude/skills/measure/check-stt.sh starts
both and runs it.
"""

import asyncio
import base64
import os
import time

import aiohttp
import numpy as np
import pytest

STT_URL = os.environ.get("TEST_STT_URL", "")
KOKORO_URL = os.environ.get("TEST_KOKORO_URL", "")
pytestmark = pytest.mark.skipif(
    not (STT_URL and KOKORO_URL), reason="TEST_STT_URL and TEST_KOKORO_URL name no services"
)

SENTENCE = "I led the team that rebuilt our pricing service, and we cut its response time in half."
FINISHED = "That is everything I wanted to say about the project."
UNFINISHED = "The hardest part of the project was, um,"
# 20 ms of 24 kHz PCM16, the length of each audio packet voice receives.
CHUNK_BYTES = 960
CHUNK_SECONDS = 0.02


async def speak(session: aiohttp.ClientSession, text: str) -> bytes:
    """Kokoro's raw PCM: 24 kHz mono 16-bit, the rate the API takes."""
    body = {"model": "kokoro", "input": text, "voice": "af_heart", "response_format": "pcm"}
    async with session.post(f"{KOKORO_URL}/v1/audio/speech", json=body) as response:
        response.raise_for_status()
        return await response.read()


async def transcribe(session: aiohttp.ClientSession, pcm: bytes) -> tuple[str, float, list]:
    """The final text, the seconds from the audio's end to it, and when each delta came."""
    async with session.ws_connect(f"{STT_URL}/v1/realtime?intent=transcription") as ws:
        assert (await ws.receive_json())["type"] == "session.created"
        session_update = {"type": "transcription", "audio": {"input": {"turn_detection": None}}}
        await ws.send_json({"type": "session.update", "session": session_update})
        assert (await ws.receive_json())["type"] == "session.updated"
        started = time.monotonic()
        deltas: list[float] = []

        async def final_text():
            async for message in ws:
                event = message.json()
                assert event["type"] != "error", event
                if event["type"].endswith(".delta"):
                    deltas.append(time.monotonic() - started)
                if event["type"].endswith(".completed"):
                    return event["transcript"], time.monotonic()
            raise AssertionError("the connection closed before the final text")

        reader = asyncio.create_task(final_text())
        try:
            for n, at in enumerate(range(0, len(pcm), CHUNK_BYTES)):
                # Each chunk at its moment in the audio, so the service gets speech at its pace.
                await asyncio.sleep(max(0, started + n * CHUNK_SECONDS - time.monotonic()))
                chunk = base64.b64encode(pcm[at : at + CHUNK_BYTES]).decode()
                await ws.send_json({"type": "input_audio_buffer.append", "audio": chunk})
            ended = time.monotonic()
            await ws.send_json({"type": "input_audio_buffer.commit"})
            final, at = await asyncio.wait_for(reader, timeout=10)
        finally:
            reader.cancel()
        return final, at - ended, deltas


def test_live_a_spoken_sentence_is_transcribed_soon_after_its_audio_ends():
    async def main():
        async with aiohttp.ClientSession() as session:
            pcm = await speak(session, SENTENCE)
            return pcm, *(await transcribe(session, pcm))

    pcm, final, after_end, deltas = asyncio.run(main())
    first = f", the first {deltas[0]:.2f} s into the audio" if deltas else ""
    print(f"\nsaid:  {SENTENCE}\nheard: {final}")
    print(f"rough text: {len(deltas)} deltas{first}")
    print(f"audio {len(pcm) / 48000:.2f} s; audio end to final text: {after_end * 1000:.0f} ms")
    assert final


def l16_16khz(pcm: bytes) -> bytes:
    """Kokoro's 24 kHz samples at 16 kHz, in network byte order as L16 is; linear interpolation is
    rough, but enough for a check."""
    samples = np.frombuffer(pcm, dtype="<i2").astype(np.float64)
    at = np.arange(0, len(samples) - 1, 24000 / 16000)
    return np.interp(at, np.arange(len(samples)), samples).astype(">i2").tobytes()


async def turn_check(session: aiohttp.ClientSession, audio: bytes) -> tuple[dict, float]:
    url = STT_URL.replace("ws", "http", 1) + "/v1/turn/check"
    started = time.monotonic()
    headers = {"Content-Type": "audio/L16;rate=16000"}
    async with session.post(url, data=audio, headers=headers) as response:
        response.raise_for_status()
        return await response.json(), time.monotonic() - started


def test_live_a_finished_sentence_is_judged_more_finished_than_a_trailing_one():
    async def main():
        async with aiohttp.ClientSession() as session:
            return [
                await turn_check(session, l16_16khz(await speak(session, text)))
                for text in (FINISHED, UNFINISHED)
            ]

    answers = asyncio.run(main())
    for text, (answer, took) in zip((FINISHED, UNFINISHED), answers, strict=True):
        print(f"\n{text!r}: {answer} in {took * 1000:.0f} ms")
    assert answers[0][0]["probability"] > answers[1][0]["probability"]
