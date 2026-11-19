"""Kokoro on Apple's MLX behind the part of Kokoro-FastAPI's API voice uses.

POST /v1/audio/speech answers a phrase as Ogg Opus, in the voice and at the speed it names, GET
/v1/audio/voices lists the voices, and GET /health answers once the model is loaded and has spoken
once. scripts/kokoro-mlx.sh installs the packages and the model and runs it:
    python server.py MODEL_DIR PORT
"""

import io
import json
import logging
import select
import socket
import sys
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

import av
import mlx.core as mx
import numpy as np
from mlx_audio.tts.utils import load_model

from trim import RATE, trim
from voices import has_voice, speed_of, voices_body

# voice sends one phrase, a sentence or two, so 64 KiB is far over any real request; the bound
# keeps a wrong caller from filling memory. A round guess.
MAX_BODY = 64 * 1024
# For reading a whole request, not each read, so a caller sending a byte at a time can't hold the
# only thread; voice sends its request at once. A round guess.
REQUEST_DEADLINE_S = 10
# What the container's Kokoro answers for a phrase with nothing to say, such as a reply's last
# phrase being only an emoji: a short silence, so the phrase isn't counted as failed.
SILENCE = np.zeros(int(RATE * 0.3), dtype=np.int16)

# The libraries' warnings can quote a phrase or its phonemes, which hold what the user said; this
# server logs a failure by its type instead. Their prints go to standard output, which the script
# discards.
logging.disable(logging.WARNING)

model_dir = Path(sys.argv[1])
# Named, since mlx-audio would otherwise guess the model from the folder's name.
model = load_model(model_dir, model_type="kokoro")


class Gone(Exception):
    """The caller hung up, as voice does when the user cuts in."""


def speak(text: str, voice: str, speed: float = 1.0, gone=lambda: False) -> np.ndarray:
    parts = []
    # A voice's first letter is its language, as the model names them.
    for result in model.generate(text=text, voice=voice, speed=speed, lang_code=voice[0]):
        mx.eval(result.audio)
        parts.append(np.array(result.audio))
        if gone():  # a long phrase comes in parts; the rest isn't made for no one
            raise Gone
    if not parts:
        return SILENCE
    return (np.clip(trim(np.concatenate(parts), text), -1, 1) * 32767).astype(np.int16)


# The format voice asks for, Ogg Opus (RFC 7845), which it unwraps into its 20 ms packets.
def ogg_opus(pcm: np.ndarray) -> bytes:
    out = io.BytesIO()
    with av.open(out, mode="w", format="opus") as container:
        stream = container.add_stream("libopus", rate=RATE, layout="mono")
        stream.bit_rate = 128000
        frame = av.AudioFrame.from_ndarray(pcm.reshape(1, -1), format="s16", layout="mono")
        frame.sample_rate = RATE
        frame.pts = 0
        for packet in stream.encode(frame):
            container.mux(packet)
        for packet in stream.encode(None):
            container.mux(packet)
    return out.getvalue()


class DeadlineReader(io.RawIOBase):
    """Reads the connection with whatever is left of the request's deadline as each read's limit."""

    def __init__(self, sock: socket.socket, deadline: float):
        self.sock, self.deadline = sock, deadline

    def readable(self):
        return True

    def readinto(self, buf):
        left = self.deadline - time.monotonic()
        if left <= 0:
            raise TimeoutError
        self.sock.settimeout(left)
        return self.sock.recv_into(buf)


class Handler(BaseHTTPRequestHandler):
    def setup(self):
        super().setup()
        self.rfile.close()  # the base class's reader, which has no deadline
        deadline = time.monotonic() + REQUEST_DEADLINE_S
        self.rfile = io.BufferedReader(DeadlineReader(self.connection, deadline))

    def log_message(self, *args):
        pass  # one line per phrase instead, below

    def gone(self) -> bool:
        # A connection the caller closed reads as ready with nothing in it.
        try:
            ready, _, _ = select.select([self.connection], [], [], 0)
            return bool(ready) and not self.connection.recv(1, socket.MSG_PEEK)
        except OSError:
            return True

    def reply(self, status: int, body: bytes = b"", content_type: str = "text/plain") -> None:
        try:
            self.send_response(status)
            self.send_header("Content-Type", content_type)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass  # the caller hung up, as voice does when the user cuts in

    def do_GET(self):
        if self.path == "/v1/audio/voices":
            return self.reply(200, voices_body(model_dir), "application/json")
        self.reply(200 if self.path == "/health" else 404)

    def do_POST(self):
        if self.path != "/v1/audio/speech":
            return self.reply(404)
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            return self.reply(400, b"bad Content-Length")
        if not 0 <= length <= MAX_BODY:
            return self.reply(413, b"request too large")
        try:
            req = json.loads(self.rfile.read(length))
        except (ValueError, RecursionError):  # RecursionError: nested too deep
            return self.reply(400, b"not JSON")
        if not isinstance(req, dict):
            return self.reply(400, b"not a JSON object")
        text, voice = req.get("input"), req.get("voice") or "af_heart"
        if req.get("response_format") != "opus":
            return self.reply(400, b"only response_format opus")
        # Never a path: the name picks one of the model's own voices.
        if not has_voice(model_dir, voice):
            return self.reply(400, b"unknown voice")
        speed = speed_of(req)
        if speed is None:
            return self.reply(400, b"speed must be a number from 0.5 to 2")
        if not isinstance(text, str):
            return self.reply(400, b"no input")
        start = time.monotonic()
        try:
            speakable = any(c.isalnum() for c in text)
            body = ogg_opus(speak(text, voice, speed, self.gone) if speakable else SILENCE)
        except Gone:
            print(f"{len(text.split())} words cancelled", file=sys.stderr)
            return
        except Exception as e:
            # The type only: a message could quote the text, which holds what the user said.
            print(f"phrase failed: {type(e).__name__}", file=sys.stderr)
            return self.reply(500)
        ms = (time.monotonic() - start) * 1000
        print(f"{len(text.split())} words in {ms:.0f} ms", file=sys.stderr)
        self.reply(200, body, "audio/opus")


speak("Warming up.", "af_heart")
# One request at a time: MLX isn't safe across threads, so a second session's phrase waits for
# the first. Loopback only: nothing else on the network may use it.
HTTPServer(("127.0.0.1", int(sys.argv[2])), Handler).serve_forever()
