"""The silence around a phrase, trimmed as the container's Kokoro trims it.

Kokoro leaves about a third of a second of silence before a phrase's sound and nearly half a second
after it; untrimmed, a reply starts sounding that much late, and its phrases sit nearly a second
apart. The container voice otherwise uses keeps the rule below, and the values are its own, so both
voices sound alike.
"""

import numpy as np

RATE = 24000  # Kokoro's sample rate
# Quieter than this is silence: -45 dB below full scale, the container's line.
SILENT = 10 ** (-45 / 20)
# Kept before the first sound, so a quiet start, an "h" or an "f", isn't cut: the container's 50 ms.
LEAD_S = 0.05
# From the end of one phrase's sound to the start of the next's, this phrase's tail and the next
# one's lead together, after a full stop or a question: the container's default, 410 ms, which
# sounds like a speaker's pause between sentences; shorter after an exclamation or a comma, by its
# multipliers.
PAUSE_S = 0.41
PAUSE_AFTER = {"!": 0.9, ",": 0.8}


def trim(pcm: np.ndarray, text: str) -> np.ndarray:
    """The phrase's audio from LEAD_S before its first sound to its pause after the last; a tail
    already shorter is kept as it is, and audio with no sound is left alone."""
    loud = np.flatnonzero(np.abs(pcm) > SILENT)
    if loud.size == 0:
        return pcm
    tail_s = PAUSE_S * PAUSE_AFTER.get(text.rstrip()[-1:], 1) - LEAD_S
    start = max(loud[0] - round(LEAD_S * RATE), 0)
    return pcm[start : loud[-1] + 1 + round(tail_s * RATE)]
