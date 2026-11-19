"""How an item reads Moonshine's lines, with a scripted stream in place of the model."""

import threading
from types import SimpleNamespace

import numpy as np
import pytest

from stt.moonshine import MoonshineItem
from stt.realtime import SAMPLE_RATE


class ScriptedStream:
    """Each add_audio, then stop, reports the next batch of (line id, text) as Moonshine's events."""

    def __init__(self, *batches, error: Exception | None = None):
        self._batches = list(batches)
        self._listeners = []
        self._error = error
        self.rates = []
        self.closed = False

    def add_listener(self, listener):
        self._listeners.append(listener)

    def _report(self):
        for line_id, text in self._batches.pop(0):
            for listener in self._listeners:
                listener(SimpleNamespace(line=SimpleNamespace(line_id=line_id, text=text)))

    def add_audio(self, samples, rate):
        self.rates.append(rate)
        self._report()

    def stop(self):
        if self._error is not None:
            for listener in self._listeners:
                listener(SimpleNamespace(line=None, error=self._error))
            return
        self._report()

    def close(self):
        self.closed = True


def item(stream):
    return MoonshineItem(stream, threading.Lock())


SILENCE = np.zeros(480, dtype=np.float32)


def test_lines_moonshine_cut_at_its_own_pauses_are_one_item():
    stream = ScriptedStream(
        [(1, "Well,")],
        [(1, "Well, I think."), (2, "Maybe")],
        [(2, "Maybe not.")],
    )
    recognised = item(stream)
    assert recognised.add(SILENCE) == "Well,"
    assert recognised.add(SILENCE) == "Well, I think. Maybe"
    assert recognised.finish() == "Well, I think. Maybe not."


def test_audio_goes_to_moonshine_at_the_api_rate():
    stream = ScriptedStream([])
    item(stream).add(SILENCE)
    assert stream.rates == [SAMPLE_RATE]


def test_lines_with_no_words_add_nothing():
    stream = ScriptedStream([(1, "Yes."), (2, " "), (3, "")], [])
    recognised = item(stream)
    recognised.add(SILENCE)
    assert recognised.finish() == "Yes."


def test_a_failure_moonshine_reports_on_stopping_fails_the_item():
    recognised = item(ScriptedStream([(1, "Hi")], error=RuntimeError("broken")))
    recognised.add(SILENCE)
    with pytest.raises(RuntimeError):
        recognised.finish()


def test_closing_releases_the_stream():
    stream = ScriptedStream()
    item(stream).close()
    assert stream.closed
