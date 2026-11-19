"""A recogniser that answers from a script, recording the audio it was given."""

import threading

import numpy as np


class FakeItem:
    def __init__(self, recogniser: FakeRecogniser):
        self._recogniser = recogniser
        self.samples = np.zeros(0, dtype=np.float32)
        self._added = 0
        self.closed = False

    def add(self, samples: np.ndarray) -> str:
        if self._recogniser.go is not None and not self._recogniser.go.wait(timeout=2):
            raise TimeoutError("no go came")
        if self._recogniser.fail:
            raise RuntimeError("the fake was told to fail")
        self.samples = np.concatenate([self.samples, samples])
        rough = self._recogniser.rough
        self._added += 1
        return rough[min(self._added, len(rough)) - 1] if rough else ""

    def finish(self) -> str:
        if self._recogniser.fail or self._recogniser.fail_finish:
            raise RuntimeError("the fake was told to fail")
        return self._recogniser.final

    def close(self) -> None:
        self.closed = True


class FakeRecogniser:
    """Each item returns the rough texts in order, one per add (the last repeats), then the final
    text; told to fail, every call raises, or only finishing; given go, adding waits for it, and
    fails if none comes."""

    def __init__(
        self,
        rough: list[str] | None = None,
        final: str = "",
        fail: bool = False,
        fail_finish: bool = False,
    ):
        self.rough = rough or []
        self.final = final
        self.fail = fail
        self.fail_finish = fail_finish
        self.go: threading.Event | None = None
        self.items: list[FakeItem] = []

    def start(self) -> FakeItem:
        item = FakeItem(self)
        self.items.append(item)
        return item
