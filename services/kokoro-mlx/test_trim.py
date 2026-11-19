import numpy as np
import pytest

from trim import RATE, trim


def phrase(before_s: float, sound_s: float, after_s: float) -> np.ndarray:
    """Silence, a tone at half of full scale, then silence."""
    t = np.arange(int(sound_s * RATE)) / RATE
    tone = 0.5 * np.sin(2 * np.pi * 220 * t).astype(np.float32)
    return np.concatenate(
        [
            np.zeros(int(before_s * RATE), np.float32),
            tone,
            np.zeros(int(after_s * RATE), np.float32),
        ]
    )


def sound_span(pcm: np.ndarray) -> tuple[float, float]:
    """Seconds of silence before the first sound and after the last."""
    loud = np.flatnonzero(np.abs(pcm) > 0.01)
    return loud[0] / RATE, (len(pcm) - 1 - loud[-1]) / RATE


@pytest.mark.parametrize(
    "text, after_s",
    [
        # The pause to the next phrase's first sound is 410 ms after a full stop or a question,
        # less after an exclamation or a comma; the next phrase keeps 50 ms of it before its sound.
        ("We versioned every price list.", 0.36),
        ("Which part was hardest?", 0.36),
        ("That's great!", 0.319),
        ("When the events arrived out of order,", 0.278),
        ("And then", 0.36),
        ("Trailing spaces count from the last mark.  ", 0.36),
    ],
)
def test_silence_around_a_phrase_is_trimmed_to_the_pause(text, after_s):
    before, after = sound_span(trim(phrase(0.33, 0.8, 0.45), text))
    assert before == pytest.approx(0.05, abs=0.002)
    assert after == pytest.approx(after_s, abs=0.002)


def test_a_phrase_starting_at_once_keeps_its_start():
    before, _ = sound_span(trim(phrase(0.0, 0.8, 0.45), "Yes."))
    assert before == pytest.approx(0, abs=0.002)


def test_a_tail_shorter_than_the_pause_is_kept_as_it_is():
    pcm = phrase(0.33, 0.8, 0.1)
    _, after = sound_span(trim(pcm, "Yes."))
    assert after == pytest.approx(0.1, abs=0.002)


def test_a_phrase_with_no_sound_is_left_alone():
    pcm = np.zeros(RATE, np.float32)
    assert len(trim(pcm, "...")) == RATE
