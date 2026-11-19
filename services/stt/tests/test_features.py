"""Smart Turn's input: Whisper's log-mel features of the last 8 s, checked against the reference.

testdata/whisper_features.npy was produced once by the reference inference's feature path:
transformers 5.23.0's WhisperFeatureExtractor(chunk_length=8), called with padding="max_length",
max_length=128000, truncation=True and do_normalize=True on chirp() front-padded with zeros to
8 s, as Smart Turn's own inference prepares audio; every 8th of its 800 frames is kept, and
the last.
"""

from pathlib import Path

import numpy as np

from stt.features import N_FRAMES, N_MELS, SAMPLE_RATE, smart_turn_input

REFERENCE = Path(__file__).parent / "testdata" / "whisper_features.npy"
KEPT_FRAMES = [*range(0, N_FRAMES, 8), N_FRAMES - 1]


def chirp() -> np.ndarray:
    """3 s rising from 200 Hz by 1300 Hz a second, with a 3 kHz tone under it."""
    t = np.arange(3 * SAMPLE_RATE) / SAMPLE_RATE
    wave = 0.3 * np.sin(2 * np.pi * (200 + 650 * t) * t) + 0.1 * np.sin(2 * np.pi * 3000 * t)
    return wave.astype(np.float32)


def test_features_match_the_reference_inference():
    features = smart_turn_input(chirp())
    assert features.shape == (1, N_MELS, N_FRAMES)
    assert features.dtype == np.float32
    np.testing.assert_allclose(features[0][:, KEPT_FRAMES], np.load(REFERENCE), atol=1e-5)


def test_only_the_last_8_seconds_are_read():
    longer = np.concatenate([np.full(2 * SAMPLE_RATE, 0.5, dtype=np.float32), *[chirp()] * 3])
    last_8_seconds = longer[-8 * SAMPLE_RATE :]
    np.testing.assert_array_equal(smart_turn_input(longer), smart_turn_input(last_8_seconds))
