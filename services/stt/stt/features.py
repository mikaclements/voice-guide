"""Smart Turn's input: Whisper's log-mel spectrogram of the last 8 s of 16 kHz audio, in numpy,
from Whisper's published parameters: 25 ms Hann windows every 10 ms, 80 mel bands on the Slaney
scale up to 8 kHz, log10, at most 8 decades below the loudest point, scaled by (x + 4) / 4.
"""

import numpy as np

SAMPLE_RATE = 16000
# Smart Turn judges the last 8 s of the user's audio.
SECONDS = 8
N_FFT = 400  # 25 ms
HOP_LENGTH = 160  # 10 ms
N_MELS = 80
N_FRAMES = SECONDS * SAMPLE_RATE // HOP_LENGTH


def hz_to_mel(hz: np.ndarray) -> np.ndarray:
    """Slaney's mel scale: linear to 1 kHz (15 mels), logarithmic above (27 mels per 6.4 times)."""
    linear = hz * 3 / 200
    log = 15 + np.log(np.maximum(hz, 1000) / 1000) * 27 / np.log(6.4)
    return np.where(hz < 1000, linear, log)


def mel_to_hz(mel: np.ndarray) -> np.ndarray:
    return np.where(mel < 15, mel * 200 / 3, 1000 * np.exp((mel - 15) * np.log(6.4) / 27))


def mel_filters() -> np.ndarray:
    """Triangles between evenly spaced mel points, each scaled to the same area, as rows over the
    FFT's bins."""
    bins = np.linspace(0, SAMPLE_RATE / 2, N_FFT // 2 + 1)
    edges = mel_to_hz(np.linspace(0, hz_to_mel(np.array(SAMPLE_RATE / 2)), N_MELS + 2))
    lower, centre, upper = edges[:-2, None], edges[1:-1, None], edges[2:, None]
    rising = (bins - lower) / (centre - lower)
    falling = (upper - bins) / (upper - centre)
    return np.maximum(0, np.minimum(rising, falling)) * 2 / (upper - lower)


MEL_FILTERS = mel_filters()
# Periodic, as the windows of a spectrogram overlap.
WINDOW = np.hanning(N_FFT + 1)[:-1]


def log_mel(audio: np.ndarray) -> np.ndarray:
    # Each window centred on its frame, the edges mirrored, so the first frame sees the start.
    padded = np.pad(audio.astype(np.float64), N_FFT // 2, mode="reflect")
    frames = np.lib.stride_tricks.sliding_window_view(padded, N_FFT)[::HOP_LENGTH]
    power = np.abs(np.fft.rfft(frames * WINDOW, axis=-1)) ** 2
    # The frame centred on the very end is left out, so 8 s make exactly 800 frames.
    mel = MEL_FILTERS @ power[:-1].T
    log_spec = np.log10(np.maximum(mel, 1e-10))
    log_spec = np.maximum(log_spec, log_spec.max() - 8.0)
    return (log_spec + 4.0) / 4.0


def smart_turn_input(audio: np.ndarray) -> np.ndarray:
    """The model's input, shaped (1, 80, 800), from 16 kHz samples as float32 in [-1, 1]: the last
    8 s, silence put in front of shorter audio, scaled to zero mean and unit variance."""
    audio = audio[-SECONDS * SAMPLE_RATE :]
    audio = np.pad(audio, (SECONDS * SAMPLE_RATE - len(audio), 0))
    audio = (audio - audio.mean()) / np.sqrt(audio.var() + 1e-7)
    return log_mel(audio)[None].astype(np.float32)
