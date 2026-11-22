"""The voices and speeds this server answers for, apart from the model, so they are tested without it.

GET /v1/audio/voices answers {"voices": [...]} with names only, where Kokoro-FastAPI describes each
voice by its "id"; voice reads either. A speech request may carry its "speed" field.
"""

import json
import math
import re
from pathlib import Path

# A language and a style, as the model names its voices (af_heart, af_v0bella): the same form voice
# checks, so both engines offer the same names.
VOICE_NAME = re.compile(r"[a-z]{2}_[a-z0-9]{1,32}")
# What voice may ask for: half the voice's pace to double it. A round guess, the same bounds voice
# checks before it asks; Kokoro-FastAPI's own are wider.
MIN_SPEED, MAX_SPEED = 0.5, 2.0


def voice_names(model_dir: Path) -> list[str]:
    """The model's voices, sorted: one file of voices/ each, named by a name this server accepts."""
    return sorted(
        p.stem for p in (model_dir / "voices").glob("*.safetensors") if VOICE_NAME.fullmatch(p.stem)
    )


def voices_body(model_dir: Path) -> bytes:
    return json.dumps({"voices": voice_names(model_dir)}).encode()


def has_voice(model_dir: Path, voice: object) -> bool:
    """Whether voice names one of the model's own voices. Never a path: only a name of the form above
    is looked for, as one file of the voices folder."""
    return (
        isinstance(voice, str)
        and VOICE_NAME.fullmatch(voice) is not None
        and (model_dir / "voices" / f"{voice}.safetensors").is_file()
    )


def speed_of(req: dict) -> float | None:
    """The request's speed, 1 when it names none, or None when it isn't a number from MIN_SPEED to
    MAX_SPEED: a boolean is not a number here, and neither is NaN."""
    speed = req.get("speed")
    if speed is None:
        return 1.0
    if isinstance(speed, bool) or not isinstance(speed, int | float) or not math.isfinite(speed):
        return None
    return float(speed) if MIN_SPEED <= speed <= MAX_SPEED else None
