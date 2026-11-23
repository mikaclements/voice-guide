"""OpenAI's realtime transcription API over a WebSocket: the part voice uses, in the API's own
message shapes, so a service speaking the same API can take this one's place. There is no turn
detection: the client commits each item, and every commit is answered with the item's final text.
"""

import asyncio
import base64
import binascii
import json
import logging
import secrets
import threading
import weakref
from typing import Any

import numpy as np
from aiohttp import WSCloseCode, WSMsgType, web

# The only rate the API takes for PCM.
SAMPLE_RATE = 24000
# An item holds at most two minutes of audio, the cap voice plans for one turn's speech, so a
# microphone left open without a commit can't fill memory. A long spoken answer runs past a minute;
# two minutes is a round guess at one, measured against real answers' lengths once sessions are
# timed.
MAX_ITEM_SECONDS = 120
# voice appends 32 ms of audio at a time, about 2 KiB as base64; 256 KiB holds 4 s, a wide margin.
MAX_MESSAGE = 256 * 1024
# How many connections one process holds. voice keeps one open for each audio connection, mostly
# silent, so what a connection costs is memory: at most one item, up to two minutes of audio,
# about 12 MB as the service holds it. 64 keeps that under 800 MB, a round guess to measure when a
# host's memory is chosen. More containers serve more.
DEFAULT_MAX_CONNECTIONS = 64
# How many recognition calls may run or wait at once. Moonshine's calls take turns on one lock,
# and small recognised 44 s of speech in 9.6 s on an M3 Pro, so about four people speaking at once
# keep up; eight calls in line means recognition has fallen a pass behind each, and an item past
# that fails at once rather than falling further behind.
DEFAULT_MAX_RECOGNISING = 8
# A ping every 10 s, a connection closed when 5 s pass without its answer: a voice host lost
# without closing frees its place in about 15 s, not after TCP's two-hour keepalive.
HEARTBEAT = 10
# Docker stops a container 10 s after asking; closing the connections takes at most half that.
CLOSE_WITHIN = 5
FAILED = "conversation.item.input_audio_transcription.failed"

log = logging.getLogger(__name__)

RECOGNISER = web.AppKey("recogniser", object)
SETTINGS = web.AppKey("settings", dict)
SOCKETS = web.AppKey("sockets", weakref.WeakSet)


def new_id(prefix: str) -> str:
    return f"{prefix}_{secrets.token_hex(12)}"


class Failed(Exception):
    """Recognising an item failed: answered with the API's failed event for it."""

    def __init__(self, item_id: str, code: str | None = None):
        super().__init__(item_id)
        self.item_id, self.code = item_id, code


class Refused(Exception):
    """A client event the service won't act on, answered with an error event."""

    def __init__(self, code: str, message: str, param: str | None = None):
        super().__init__(message)
        self.code, self.message, self.param = code, message, param


class RoughText:
    """Turns the recogniser's rough text into the API's deltas, which only ever add text.

    The last word is held back, since it is often still being said. A word already sent that the
    recogniser revises can't be taken back, so deltas pause until the words agree again, and the
    item's final text corrects the rest.
    """

    def __init__(self):
        self._sent: list[str] = []

    def delta(self, text: str, final: bool = False) -> str:
        words = text.split()
        ready = words if final else words[:-1]
        if [plain(w) for w in ready[: len(self._sent)]] != [plain(w) for w in self._sent]:
            return ""
        new = ready[len(self._sent) :]
        delta = (" " if self._sent and new else "") + " ".join(new)
        self._sent.extend(new)
        return delta


def plain(word: str) -> str:
    """A word as compared between rough texts, where its punctuation and case often change."""
    return word.strip(".,;:!?\"'").lower()


def transcription_session(session_id: str, model: str) -> dict[str, Any]:
    audio_input = {
        "format": {"type": "audio/pcm", "rate": SAMPLE_RATE},
        "noise_reduction": None,
        "transcription": {"model": model},
        "turn_detection": None,
    }
    return {
        "id": session_id,
        "object": "realtime.transcription_session",
        "type": "transcription",
        "audio": {"input": audio_input},
        "include": None,
    }


class Connection:
    """One client's transcription session: the item being appended and the item committed last."""

    def __init__(self, ws: web.WebSocketResponse, recogniser: Any, settings: dict[str, Any]):
        """The recogniser's start() gives an item whose add(samples), finish() and close() block;
        they are called off the event loop, one at a time."""
        self._ws = ws
        self._recogniser = recogniser
        self._settings = settings
        self._session_id = new_id("sess")
        self._item: Any = None
        self._item_id = ""
        self._samples = 0
        self._rough = RoughText()
        self._previous_item_id: str | None = None

    async def serve(self) -> None:
        await self._send("session.created", session=self._session())
        try:
            async for message in self._ws:
                if message.type == WSMsgType.TEXT and len(message.data) > MAX_MESSAGE:
                    refused = Refused("message_too_large", "An event is at most 256 KiB.")
                    await self._error("invalid_request_error", refused, None)
                    await self._ws.close(code=WSCloseCode.MESSAGE_TOO_BIG)
                elif message.type == WSMsgType.TEXT:
                    await self._handle(message.data)
        finally:
            await self._drop_item()

    async def _handle(self, data: str) -> None:
        event_id = None
        try:
            try:
                event = json.loads(data)
                event_id = event.get("event_id")
            except ValueError, AttributeError:
                raise Refused("invalid_event", "The event is not a JSON object.") from None
            match event.get("type"):
                case "session.update":
                    await self._update(event.get("session"))
                case "input_audio_buffer.append":
                    await self._append(event.get("audio"))
                case "input_audio_buffer.commit":
                    await self._commit()
                case "input_audio_buffer.clear":
                    await self._drop_item()
                    await self._send("input_audio_buffer.cleared")
                case _:
                    raise Refused("invalid_event", "The event type is not one this service takes.")
        except Refused as refused:
            await self._error("invalid_request_error", refused, event_id)
        except Failed as failed:
            # Logged without the exception's text, which can quote what was said.
            log.error("recognising failed")
            await self._drop_item()
            error = {"type": "server_error", "code": failed.code, "message": "Recognising failed."}
            error["param"] = None
            await self._send(FAILED, item_id=failed.item_id, content_index=0, error=error)

    async def _update(self, session: Any) -> None:
        try:
            if session["type"] != "transcription":
                raise KeyError
        except TypeError, KeyError:
            raise Refused("invalid_value", "Only transcription sessions.", "session.type") from None
        try:
            audio_input = (session.get("audio") or {}).get("input") or {}
            audio_format = audio_input.get("format")
            pcm = audio_format is None or (
                audio_format.get("type") == "audio/pcm"
                and audio_format.get("rate", SAMPLE_RATE) == SAMPLE_RATE
            )
        except AttributeError:
            raise Refused("invalid_value", "Not an object.", "session.audio") from None
        if not pcm:
            param = "session.audio.input.format"
            raise Refused("invalid_value", "Only audio/pcm at 24000 Hz.", param)
        if audio_input.get("turn_detection") is not None:
            param = "session.audio.input.turn_detection"
            raise Refused("invalid_value", "No turn detection: commit each item.", param)
        # The rest (the model, a prompt, languages) has no effect: the service runs one model.
        await self._send("session.updated", session=self._session())

    def _session(self) -> dict[str, Any]:
        return transcription_session(self._session_id, self._settings["model"])

    async def _append(self, audio: Any) -> None:
        try:
            pcm = base64.b64decode(audio, validate=True)
        except TypeError, binascii.Error:
            raise Refused("invalid_value", "The audio is not base64.", "audio") from None
        if len(pcm) % 2:
            raise Refused("invalid_value", "The audio is not whole 16-bit samples.", "audio")
        samples = np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768
        if not len(samples):
            return
        cap = self._settings["max_item_seconds"]
        if self._samples + len(samples) > cap * SAMPLE_RATE:
            # The item goes, so what follows starts clean instead of failing on a full buffer.
            await self._drop_item()
            raise Refused(
                "input_audio_buffer_full", f"An item holds at most {cap} s; it was dropped."
            )
        if self._item is None:
            self._item_id = new_id("item")
            self._item = await self._recognise(self._recogniser.start)
        self._samples += len(samples)
        rough = await self._recognise(self._item.add, samples)
        await self._delta(item_id=self._item_id, delta=self._rough.delta(rough))

    async def _recognise(self, call, *args) -> Any:
        """A blocking call into the recogniser, off the event loop, failing the item it is for."""
        load, guard = self._settings["recognising"], self._settings["recognising_guard"]
        with guard:
            if load[0] >= self._settings["max_recognising"]:
                raise Failed(self._item_id, "server_busy")
            load[0] += 1

        def work() -> Any:
            # The place is given back when the thread is done, not when its caller stops
            # waiting, as a cancelled call's thread still holds the recogniser.
            try:
                return call(*args)
            finally:
                with guard:
                    load[0] -= 1

        try:
            return await asyncio.to_thread(work)
        except asyncio.CancelledError:
            raise
        except Exception:
            raise Failed(self._item_id) from None

    async def _commit(self) -> None:
        if self._item is None:
            raise Refused("input_audio_buffer_commit_empty", "The input audio buffer is empty.")
        item, item_id, seconds = self._item, self._item_id, self._samples / SAMPLE_RATE
        await self._send(
            "input_audio_buffer.committed", previous_item_id=self._previous_item_id, item_id=item_id
        )
        self._previous_item_id = item_id
        try:
            final = await self._recognise(item.finish)
            delta = self._rough.delta(final, final=True)
        finally:
            await self._drop_item()
        await self._delta(item_id=item_id, delta=delta)
        await self._send(
            "conversation.item.input_audio_transcription.completed",
            item_id=item_id,
            content_index=0,
            transcript=" ".join(final.split()),
            usage={"type": "duration", "seconds": seconds},
        )

    async def _delta(self, item_id: str, delta: str) -> None:
        if delta:
            await self._send(
                "conversation.item.input_audio_transcription.delta",
                item_id=item_id,
                content_index=0,
                delta=delta,
            )

    async def _drop_item(self) -> None:
        item, self._item, self._samples, self._rough = self._item, None, 0, RoughText()
        if item is not None:
            await asyncio.to_thread(item.close)

    async def _error(self, kind: str, refused: Refused, event_id: Any) -> None:
        error = {"type": kind, "code": refused.code, "message": refused.message}
        await self._send("error", error=error | {"param": refused.param, "event_id": event_id})

    async def _send(self, event_type: str, **fields: Any) -> None:
        await self._ws.send_json({"type": event_type, "event_id": new_id("event"), **fields})


async def realtime(request: web.Request) -> web.WebSocketResponse:
    if request.query.get("intent") != "transcription":
        raise web.HTTPBadRequest(text="Only intent=transcription is served.\n")
    settings = request.app[SETTINGS]
    if len(request.app[SOCKETS]) >= settings["max_connections"]:
        n = settings["max_connections"]
        raise web.HTTPServiceUnavailable(text=f"At most {n} connections at once are served.\n")
    # Twice the event limit, so an event over it arrives whole and is answered before closing.
    ws = web.WebSocketResponse(max_msg_size=2 * MAX_MESSAGE, heartbeat=settings["heartbeat"])
    await ws.prepare(request)
    request.app[SOCKETS].add(ws)
    await Connection(ws, request.app[RECOGNISER], settings).serve()
    return ws


async def health(_: web.Request) -> web.Response:
    return web.Response(text="ok\n")


async def close_sockets(app: web.Application) -> None:
    # Clients are told the server is going away, all at once, so stopping doesn't wait on them.
    closing = [ws.close(code=WSCloseCode.GOING_AWAY) for ws in set(app[SOCKETS])]
    try:
        await asyncio.wait_for(asyncio.gather(*closing), CLOSE_WITHIN)
    except TimeoutError:
        log.warning("connections still open when stopping")


def app(
    recogniser: Any,
    model: str = "moonshine",
    max_connections: int = DEFAULT_MAX_CONNECTIONS,
    max_item_seconds: int = MAX_ITEM_SECONDS,
    max_recognising: int = DEFAULT_MAX_RECOGNISING,
    heartbeat: float = HEARTBEAT,
) -> web.Application:
    """The service over a recogniser whose start() opens an item (see Connection)."""
    application = web.Application()
    application[RECOGNISER] = recogniser
    application[SETTINGS] = {
        "model": model,
        "max_connections": max_connections,
        "max_item_seconds": max_item_seconds,
        "max_recognising": max_recognising,
        # Calls running or waiting now, shared by every connection.
        "recognising": [0],
        "recognising_guard": threading.Lock(),
        "heartbeat": heartbeat,
    }
    application[SOCKETS] = weakref.WeakSet()
    application.router.add_get("/v1/realtime", realtime)
    application.router.add_get("/health", health)
    application.on_shutdown.append(close_sockets)
    return application
