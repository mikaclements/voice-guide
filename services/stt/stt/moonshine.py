"""Moonshine's English streaming models as the recogniser."""

import threading
from pathlib import Path
from typing import Any

import numpy as np
from moonshine_voice import ModelArch, Transcriber

from stt.realtime import SAMPLE_RATE

SIZES = {
    "tiny": ModelArch.TINY_STREAMING,
    "small": ModelArch.SMALL_STREAMING,
    "medium": ModelArch.MEDIUM_STREAMING,
}

# Moonshine's own default: rough text about twice a second, each pass costing CPU, so a shorter
# interval is a trade measured first.
UPDATE_INTERVAL = 0.5


class MoonshineRecogniser:
    def __init__(self, models: Path, size: str):
        self._transcriber = Transcriber(
            model_path=str(models / size), model_arch=SIZES[size], update_interval=UPDATE_INTERVAL
        )
        # Moonshine doesn't promise its streams may be used from several threads at once, so the
        # calls into it take turns.
        self._lock = threading.Lock()

    def start(self) -> MoonshineItem:
        with self._lock:
            stream = self._transcriber.create_stream()
            stream.start()
        return MoonshineItem(stream, self._lock)


class MoonshineItem:
    """Moonshine cuts speech into lines at its own pauses, which aren't the client's; an item is
    every line heard between two commits, joined."""

    def __init__(self, stream: Any, lock: threading.Lock):
        self._stream = stream
        self._lock = lock
        self._lines: dict[int, str] = {}
        self._error: Exception | None = None
        stream.add_listener(self._heard)

    def _heard(self, event: Any) -> None:
        if event.line is None:
            self._error = event.error
            return
        # In the order lines were first heard, which is the order they were said.
        self._lines[event.line.line_id] = event.line.text

    def _text(self) -> str:
        return " ".join(text.strip() for text in self._lines.values() if text.strip())

    def add(self, samples: np.ndarray) -> str:
        with self._lock:
            self._stream.add_audio(samples, SAMPLE_RATE)
        return self._text()

    def finish(self) -> str:
        # Stopping recognises the audio not yet looked at and ends the open line.
        with self._lock:
            self._stream.stop()
        if self._error is not None:
            raise self._error
        return self._text()

    def close(self) -> None:
        with self._lock:
            self._stream.close()
