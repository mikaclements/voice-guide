"""The realtime transcription API over a real WebSocket, with a scripted recogniser."""

import asyncio
import base64
import json
import threading

import numpy as np
import pytest
from aiohttp import WSCloseCode, WSMsgType, WSServerHandshakeError
from aiohttp.test_utils import TestClient, TestServer

from stt import realtime
from tests.fake import FakeRecogniser

URL = "/v1/realtime?intent=transcription"
COMPLETED = "conversation.item.input_audio_transcription.completed"


def run(recogniser, script, **settings):
    """Runs the script against a connected client, after the session.created it is sent first."""

    async def main():
        async with TestClient(TestServer(realtime.app(recogniser, **settings))) as client:
            ws = await client.ws_connect(URL)
            try:
                await script(ws, await receive(ws))
            finally:
                await ws.close()

    asyncio.run(main())


async def receive(ws):
    return json.loads(await ws.receive_str(timeout=2))


async def receive_until(ws, event_type):
    while (event := await receive(ws))["type"] != event_type:
        pass
    return event


async def append(ws, *values: int):
    audio = base64.b64encode(np.array(values, dtype="<i2").tobytes()).decode()
    await ws.send_json({"type": "input_audio_buffer.append", "audio": audio})


async def commit(ws):
    await ws.send_json({"type": "input_audio_buffer.commit"})


def update(**audio_input):
    session = {"type": "transcription", "audio": {"input": audio_input}}
    return {"type": "session.update", "event_id": "event_client_1", "session": session}


def test_session_starts_as_transcription_of_24khz_pcm_without_turn_detection():
    async def script(ws, created):
        session = created["session"]
        assert created["type"] == "session.created"
        assert session["object"] == "realtime.transcription_session"
        assert session["audio"]["input"]["format"] == {"type": "audio/pcm", "rate": 24000}
        assert session["audio"]["input"]["turn_detection"] is None

    run(FakeRecogniser(), script)


def test_session_update_without_turn_detection_is_answered_with_the_session():
    async def script(ws, _):
        pcm = {"type": "audio/pcm", "rate": 24000}
        await ws.send_json(update(format=pcm, transcription={"model": "any"}, turn_detection=None))
        updated = await receive(ws)
        assert updated["type"] == "session.updated"
        assert updated["session"]["audio"]["input"]["turn_detection"] is None

    run(FakeRecogniser(), script)


@pytest.mark.parametrize(
    ("event", "param"),
    [
        (update(turn_detection={"type": "server_vad"}), "session.audio.input.turn_detection"),
        (update(format={"type": "audio/pcm", "rate": 16000}), "session.audio.input.format"),
        (update(format={"type": "audio/pcmu"}), "session.audio.input.format"),
        ({"type": "session.update", "session": {"type": "realtime"}}, "session.type"),
        (
            {"type": "session.update", "session": {"type": "transcription", "audio": 1}},
            "session.audio",
        ),
    ],
)
def test_session_update_the_service_cannot_honour_is_refused(event, param):
    async def script(ws, _):
        await ws.send_json(event)
        error = await receive(ws)
        assert (error["type"], error["error"]["type"]) == ("error", "invalid_request_error")
        assert error["error"]["param"] == param
        assert error["error"]["event_id"] == event.get("event_id")

    run(FakeRecogniser(), script)


def test_commit_answers_committed_then_the_final_text_of_the_item():
    recogniser = FakeRecogniser(final=" hello  there ")

    async def script(ws, _):
        await append(ws, *([0] * 2400))
        await append(ws, *([0] * 2400))
        await commit(ws)
        committed = await receive(ws)
        assert committed["type"] == "input_audio_buffer.committed"
        assert committed["previous_item_id"] is None
        completed = await receive_until(ws, COMPLETED)
        assert (completed["item_id"], completed["content_index"]) == (committed["item_id"], 0)
        assert completed["transcript"] == "hello there"
        assert completed["usage"] == {"type": "duration", "seconds": 0.2}

    run(recogniser, script)
    assert recogniser.items[0].closed


def test_appended_pcm16_reaches_the_recogniser_as_samples_in_unit_range():
    recogniser = FakeRecogniser()

    async def script(ws, _):
        await append(ws, -32768, 0, 16384)
        await commit(ws)
        await receive_until(ws, COMPLETED)

    run(recogniser, script)
    assert recogniser.items[0].samples.tolist() == [-1.0, 0.0, 0.5]


async def deltas_then_final(ws, appends: int):
    for _ in range(appends):
        await append(ws, 0)
    await commit(ws)
    events = []
    while (event := await receive(ws))["type"] != COMPLETED:
        events.append(event)
    item_ids = {e["item_id"] for e in events}
    return [e["delta"] for e in events if e["type"].endswith(".delta")], item_ids, event


def test_rough_text_arrives_as_deltas_holding_back_the_word_still_being_said():
    rough = ["so", "so the", "so the plan", "so the plan is"]

    async def script(ws, _):
        deltas, item_ids, completed = await deltas_then_final(ws, len(rough))
        assert deltas == ["so", " the", " plan", " is set"]
        assert item_ids == {completed["item_id"]}
        assert completed["transcript"] == "so the plan is set"

    run(FakeRecogniser(rough=rough, final="so the plan is set"), script)


def test_a_revised_word_stops_the_deltas_until_the_final_text():
    rough = ["I want", "I want to go", "I wanted to go there", "I wanted to go there now"]

    async def script(ws, _):
        deltas, _, completed = await deltas_then_final(ws, len(rough))
        assert deltas == ["I", " want to"]
        assert completed["transcript"] == "I wanted to go there now"

    run(FakeRecogniser(rough=rough, final="I wanted to go there now"), script)


def test_deltas_resume_once_the_rough_text_agrees_again_with_what_was_sent():
    rough = ["I want to", "I wanted to go", "I want to go there now"]

    async def script(ws, _):
        deltas, _, _ = await deltas_then_final(ws, len(rough))
        assert deltas == ["I want", " to go there", " now"]

    run(FakeRecogniser(rough=rough, final="I want to go there now"), script)


def test_rough_text_of_an_item_whose_recognition_fails_is_ended_by_the_failure():
    recogniser = FakeRecogniser(rough=["so the", "so the plan"])

    async def script(ws, _):
        await append(ws, 0)
        delta = await receive(ws)
        recogniser.fail = True
        await append(ws, 0)
        failed = await receive(ws)
        assert (failed["type"], failed["item_id"]) == (FAILED, delta["item_id"])

    run(recogniser, script)


def test_a_word_whose_punctuation_or_case_changed_is_the_same_word():
    rough = ["Well I", "well, I think", "Well, I think so."]

    async def script(ws, _):
        deltas, _, _ = await deltas_then_final(ws, len(rough))
        assert deltas == ["Well", " I", " think", " so."]

    run(FakeRecogniser(rough=rough, final="Well, I think so."), script)


def test_rough_text_starts_afresh_with_each_item():
    async def script(ws, _):
        for _ in range(2):
            deltas, _, _ = await deltas_then_final(ws, 2)
            assert deltas == ["one", " two"]

    run(FakeRecogniser(rough=["one", "one two"], final="one two"), script)


def test_each_commit_starts_a_new_item_after_the_last():
    recogniser = FakeRecogniser(final="words")

    async def script(ws, _):
        ids = []
        for _ in range(2):
            await append(ws, 0)
            await commit(ws)
            committed = await receive(ws)
            await receive_until(ws, COMPLETED)
            ids.append((committed["previous_item_id"], committed["item_id"]))
        assert ids[1][0] == ids[0][1] != ids[1][1]

    run(recogniser, script)
    assert len(recogniser.items) == 2


def test_commit_of_an_empty_buffer_is_refused():
    async def script(ws, _):
        await ws.send_json({"type": "input_audio_buffer.commit", "event_id": "event_client_2"})
        error = (await receive(ws))["error"]
        assert error["code"] == "input_audio_buffer_commit_empty"
        assert error["event_id"] == "event_client_2"

    run(FakeRecogniser(), script)


def test_clear_drops_the_audio_so_far():
    recogniser = FakeRecogniser()

    async def script(ws, _):
        await append(ws, 0, 0)
        await ws.send_json({"type": "input_audio_buffer.clear"})
        assert (await receive(ws))["type"] == "input_audio_buffer.cleared"
        await append(ws, 0)
        await commit(ws)
        completed = await receive_until(ws, COMPLETED)
        assert completed["usage"]["seconds"] == pytest.approx(1 / 24000)

    run(recogniser, script)
    assert recogniser.items[0].closed
    assert len(recogniser.items[1].samples) == 1


@pytest.mark.parametrize("audio", ["not base64!", base64.b64encode(b"\x00").decode(), None])
def test_audio_that_is_not_base64_pcm16_is_refused(audio):
    async def script(ws, _):
        await ws.send_json({"type": "input_audio_buffer.append", "audio": audio})
        assert (await receive(ws))["error"]["param"] == "audio"

    run(FakeRecogniser(), script)


@pytest.mark.parametrize("message", ["not json", json.dumps({"type": "response.create"}), "[]"])
def test_a_message_that_is_not_a_known_event_is_refused(message):
    async def script(ws, _):
        await ws.send_str(message)
        assert (await receive(ws))["error"]["code"] == "invalid_event"

    run(FakeRecogniser(), script)


FAILED = "conversation.item.input_audio_transcription.failed"


def test_recognition_failing_while_audio_arrives_fails_the_item_and_the_connection_stays():
    async def script(ws, _):
        await append(ws, 0)
        failed = await receive(ws)
        assert (failed["type"], failed["content_index"]) == (FAILED, 0)
        assert failed["item_id"].startswith("item_") and failed["error"]["type"] == "server_error"
        await ws.send_json({"type": "input_audio_buffer.clear"})
        assert (await receive(ws))["type"] == "input_audio_buffer.cleared"

    run(FakeRecogniser(fail=True), script)


def test_recognition_failing_after_the_commit_fails_the_committed_item():
    async def script(ws, _):
        await append(ws, 0)
        await commit(ws)
        committed = await receive(ws)
        failed = await receive(ws)
        assert (failed["type"], failed["item_id"]) == (FAILED, committed["item_id"])

    run(FakeRecogniser(fail_finish=True), script)


def test_recognition_runs_off_the_event_loop():
    recogniser = FakeRecogniser()
    recogniser.go = threading.Event()

    async def main():
        async with TestClient(TestServer(realtime.app(recogniser))) as client:
            ws = await client.ws_connect(URL)
            await receive(ws)
            await append(ws, 0)
            # Recognition is still working; the server answers another request meanwhile.
            assert (await client.get("/health")).status == 200
            recogniser.go.set()
            await commit(ws)
            assert (await receive(ws))["type"] == "input_audio_buffer.committed"
            await ws.close()

    asyncio.run(main())


def test_connections_past_the_cap_are_refused():
    async def main():
        application = realtime.app(FakeRecogniser(), max_connections=1)
        async with TestClient(TestServer(application)) as client:
            first = await client.ws_connect(URL)
            with pytest.raises(WSServerHandshakeError) as refused:
                await client.ws_connect(URL)
            assert refused.value.status == 503
            await first.close()

    asyncio.run(main())


def test_audio_past_an_items_cap_drops_the_item_so_the_next_starts_clean():
    recogniser = FakeRecogniser(final="kept")

    async def script(ws, _):
        await append(ws, *([0] * 24000))
        await append(ws, 0)
        assert (await receive(ws))["error"]["code"] == "input_audio_buffer_full"
        await commit(ws)
        assert (await receive(ws))["error"]["code"] == "input_audio_buffer_commit_empty"
        await append(ws, 0)
        await commit(ws)
        completed = await receive_until(ws, COMPLETED)
        assert completed["usage"]["seconds"] == pytest.approx(1 / 24000)

    run(recogniser, script, max_item_seconds=1)


def test_recognition_past_its_bound_fails_the_item_at_once():
    recogniser = FakeRecogniser()
    recogniser.go = threading.Event()

    async def main():
        application = realtime.app(recogniser, max_recognising=1)
        async with TestClient(TestServer(application)) as client:
            first, second = [await client.ws_connect(URL) for _ in range(2)]
            for ws in (first, second):
                await receive(ws)
            await append(first, 0)
            await asyncio.sleep(0.1)
            await append(second, 0)
            failed = await receive(second)
            assert (failed["type"], failed["error"]["code"]) == (FAILED, "server_busy")
            recogniser.go.set()
            for ws in (first, second):
                await ws.close()

    asyncio.run(main())


def test_a_client_that_stops_answering_pings_loses_its_connection():
    async def main():
        application = realtime.app(FakeRecogniser(), heartbeat=0.2)
        async with TestClient(TestServer(application)) as client:
            ws = await client.ws_connect(URL, autoping=False)
            while (await ws.receive(timeout=2)).type not in (
                WSMsgType.CLOSE,
                WSMsgType.CLOSED,
                WSMsgType.ERROR,
            ):
                pass

    asyncio.run(main())


def test_an_empty_append_starts_no_item():
    async def script(ws, _):
        await ws.send_json({"type": "input_audio_buffer.append", "audio": ""})
        await commit(ws)
        assert (await receive(ws))["error"]["code"] == "input_audio_buffer_commit_empty"

    run(FakeRecogniser(), script)


def test_a_message_over_the_limit_is_answered_with_an_error_then_closed():
    async def script(ws, _):
        await ws.send_str(" " * (realtime.MAX_MESSAGE + 1))
        assert (await receive(ws))["error"]["code"] == "message_too_large"
        assert (await ws.receive(timeout=2)).type in (WSMsgType.CLOSE, WSMsgType.CLOSED)
        assert ws.close_code == WSCloseCode.MESSAGE_TOO_BIG

    run(FakeRecogniser(), script)


def test_the_session_names_the_model_it_transcribes_with():
    async def script(ws, created):
        assert created["session"]["audio"]["input"]["transcription"] == {"model": "a-model"}

    run(FakeRecogniser(), script, model="a-model")


def test_stopping_closes_every_connection_as_going_away():
    async def main():
        server = TestServer(realtime.app(FakeRecogniser()))
        async with TestClient(server) as client:
            sockets = [await client.ws_connect(URL) for _ in range(2)]
            await server.app.shutdown()
            for ws in sockets:
                while (await ws.receive(timeout=2)).type not in (WSMsgType.CLOSE, WSMsgType.CLOSED):
                    pass
                assert ws.close_code == WSCloseCode.GOING_AWAY

    asyncio.run(main())


def test_only_transcription_sessions_are_served():
    async def main():
        async with TestClient(TestServer(realtime.app(FakeRecogniser()))) as client:
            with pytest.raises(WSServerHandshakeError) as refused:
                await client.ws_connect("/v1/realtime?model=some-model")
            assert refused.value.status == 400

    asyncio.run(main())


def test_a_cancelled_recognition_keeps_its_place_until_its_thread_is_done():
    async def main():
        application = realtime.app(FakeRecogniser(), max_recognising=1)
        load = application[realtime.SETTINGS]["recognising"]
        connection = realtime.Connection(None, FakeRecogniser(), application[realtime.SETTINGS])
        go = threading.Event()
        task = asyncio.create_task(connection._recognise(go.wait, 2))
        await asyncio.sleep(0.05)
        task.cancel()
        await asyncio.sleep(0.05)
        # The thread still holds the recogniser, so its place is still taken.
        assert load == [1]
        go.set()
        for _ in range(100):
            if load == [0]:
                break
            await asyncio.sleep(0.01)
        assert load == [0]

    asyncio.run(main())
