"""The end-of-turn check over HTTP, with a scripted model."""

import asyncio
import base64
import threading
from concurrent.futures import ThreadPoolExecutor

import numpy as np
import pytest
from aiohttp import web
from aiohttp.test_utils import TestClient, TestServer

from stt import realtime, turn
from tests.fake import FakeRecogniser

L16 = "audio/L16;rate=16000"


class FakeTurnModel:
    """Answers the probability it was given, recording the audio; told to, waits for a go, and
    fails if none comes."""

    def __init__(self, probability: float = 0.0, fail: bool = False):
        self._probability = probability
        self._fail = fail
        self.audio: list[np.ndarray] = []
        self.go: threading.Event | None = None

    def probability(self, audio: np.ndarray) -> float:
        self.audio.append(audio)
        if self._fail or (self.go is not None and not self.go.wait(timeout=2)):
            raise RuntimeError("the fake was told to fail")
        return self._probability


def run(model, script):
    async def main():
        application = web.Application()
        turn.register(application, model)
        application.router.add_get("/health", realtime.health)
        async with TestClient(TestServer(application)) as client:
            await script(client)

    asyncio.run(main())


def samples(*values: int) -> bytes:
    return np.array(values, dtype=">i2").tobytes()


async def check(client, body: bytes, content_type: str = L16):
    return await client.post("/v1/turn/check", data=body, headers={"Content-Type": content_type})


@pytest.mark.parametrize(("probability", "finished"), [(0.7, True), (0.5, False), (0.2, False)])
def test_the_user_has_finished_when_the_model_says_more_likely_than_half(probability, finished):
    async def script(client):
        response = await check(client, samples(0, 0))
        assert response.status == 200
        assert await response.json() == {"finished": finished, "probability": probability}

    run(FakeTurnModel(probability), script)


def test_the_body_is_16_bit_samples_in_network_byte_order():
    model = FakeTurnModel()

    async def script(client):
        await check(client, bytes([0x80, 0x00, 0x40, 0x00]))

    run(model, script)
    assert model.audio[0].tolist() == [-1.0, 0.5]


@pytest.mark.parametrize(
    "content_type",
    ["audio/L16;rate=24000", "audio/L16", "audio/L16;rate=16000;channels=2", "audio/x;rate=16000"],
)
def test_audio_other_than_16khz_mono_l16_is_refused(content_type):
    async def script(client):
        response = await check(client, samples(0), content_type)
        assert response.status == 415
        assert response.content_type == "application/problem+json"

    run(FakeTurnModel(), script)


def test_parameters_are_read_in_any_case_and_spacing():
    async def script(client):
        assert (await check(client, samples(0), "audio/l16; Rate=16000; channels=1")).status == 200

    run(FakeTurnModel(), script)


@pytest.mark.parametrize(
    ("body", "status"), [(b"", 400), (b"\x00", 400), (bytes(2 * 16000 * 8 + 2), 413)]
)
def test_a_body_that_is_not_up_to_8_seconds_of_samples_is_refused(body, status):
    async def script(client):
        assert (await check(client, body)).status == status

    run(FakeTurnModel(), script)


def test_8_seconds_are_taken():
    model = FakeTurnModel()

    async def script(client):
        assert (await check(client, bytes(2 * 16000 * 8))).status == 200

    run(model, script)


def test_the_model_runs_off_the_event_loop():
    model = FakeTurnModel(0.9)
    model.go = threading.Event()

    async def script(client):
        answer = asyncio.create_task(check(client, samples(0)))
        while not model.audio:
            await asyncio.sleep(0.01)
        # The model is still working; the server answers another request meanwhile.
        assert (await client.get("/health")).status == 200
        model.go.set()
        assert (await (await answer).json())["finished"]

    run(model, script)


def test_a_model_failure_is_a_server_error():
    async def script(client):
        response = await check(client, samples(0))
        assert response.status == 500
        assert response.content_type == "application/problem+json"

    run(FakeTurnModel(fail=True), script)


def test_a_check_never_waits_behind_recognition():
    recogniser = FakeRecogniser()
    recogniser.go = threading.Event()

    async def main():
        # One thread for everything that doesn't bring its own, which recognition will hold.
        asyncio.get_running_loop().set_default_executor(ThreadPoolExecutor(1))
        application = realtime.app(recogniser)
        turn.register(application, FakeTurnModel(0.9))
        async with TestClient(TestServer(application)) as client:
            ws = await client.ws_connect("/v1/realtime?intent=transcription")
            await ws.receive_json()
            audio = base64.b64encode(bytes(2)).decode()
            await ws.send_json({"type": "input_audio_buffer.append", "audio": audio})
            await asyncio.sleep(0.1)
            answer = await asyncio.wait_for(check(client, samples(0)), 1)
            assert (await answer.json())["finished"]
            recogniser.go.set()
            await ws.close()

    asyncio.run(main())


def test_a_body_over_8_seconds_is_refused_as_problem_details_however_it_comes():
    async def chunks():
        for _ in range(20):
            yield bytes(64 * 1024)

    async def script(client):
        for body in (bytes(2 * 1024 * 1024), chunks()):
            response = await check(client, body)
            assert response.status == 413
            assert response.content_type == "application/problem+json"

    run(FakeTurnModel(), script)


def test_checks_past_the_queue_bound_are_refused():
    model = FakeTurnModel(0.9)
    model.go = threading.Event()

    async def main():
        application = web.Application()
        turn.register(application, model, max_queued=1)
        async with TestClient(TestServer(application)) as client:
            first = asyncio.create_task(check(client, samples(0)))
            while not model.audio:
                await asyncio.sleep(0.01)
            refused = await check(client, samples(0))
            assert refused.status == 503
            assert refused.content_type == "application/problem+json"
            model.go.set()
            assert (await first).status == 200

    asyncio.run(main())


def test_a_check_that_waited_past_its_callers_deadline_is_not_run():
    model = FakeTurnModel(0.9)
    model.go = threading.Event()

    async def main():
        application = web.Application()
        turn.register(application, model, max_queued=2, stale_after=0.1)
        async with TestClient(TestServer(application)) as client:
            first = asyncio.create_task(check(client, samples(0)))
            while not model.audio:
                await asyncio.sleep(0.01)
            late = asyncio.create_task(check(client, samples(0)))
            await asyncio.sleep(0.3)
            model.go.set()
            assert (await first).status == 200
            assert (await late).status == 503
            assert len(model.audio) == 1

    asyncio.run(main())
