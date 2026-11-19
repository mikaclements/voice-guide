"""Runs the speech-to-text service with the settings in the environment:
STT_HOST     address to listen on (127.0.0.1)
STT_PORT     port (8092, beside the model service's 8091 on a developer's machine)
STT_MAX_CONNECTIONS  connections held at once (64; see realtime.DEFAULT_MAX_CONNECTIONS)
STT_MODEL    Moonshine's size: tiny, small or medium (small)
STT_MODELS   folder holding Moonshine's sizes and Smart Turn, as stt.models writes it (models)
"""

import json
import logging
import os
import sys
from dataclasses import dataclass
from pathlib import Path

from aiohttp import web

from stt import realtime, turn
from stt.models import SMART_TURN
from stt.moonshine import SIZES, MoonshineRecogniser


@dataclass
class Settings:
    host: str
    port: int
    max_connections: int
    size: str
    models: Path


# Small by default: by the published figures for a laptop's CPU (an M3), its text comes about
# 150 ms after speech ends with 7.8% of words wrong, against tiny's 50 ms and 12% and medium's
# 260 ms and 6.7%; .claude/skills/measure/check-stt.sh measures it here.
DEFAULT_SIZE = "small"


def settings(environ: dict[str, str]) -> Settings:
    max_connections = int(environ.get("STT_MAX_CONNECTIONS", realtime.DEFAULT_MAX_CONNECTIONS))
    if max_connections < 1:
        raise ValueError("STT_MAX_CONNECTIONS must be at least 1")
    size = environ.get("STT_MODEL", DEFAULT_SIZE)
    if size not in SIZES:
        raise ValueError(f"STT_MODEL is {size!r}; it takes {', '.join(SIZES)}")
    return Settings(
        host=environ.get("STT_HOST", "127.0.0.1"),
        port=int(environ.get("STT_PORT", "8092")),
        max_connections=max_connections,
        size=size,
        models=Path(environ.get("STT_MODELS", "models")),
    )


class JSONLines(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        line = {"time": self.formatTime(record), "level": record.levelname}
        return json.dumps(line | {"msg": record.getMessage()})


def main() -> None:
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(JSONLines())
    logging.basicConfig(level=logging.INFO, handlers=[handler])
    try:
        config = settings(dict(os.environ))
    except ValueError as err:
        sys.exit(f"stt: {err}")

    application = realtime.app(
        MoonshineRecogniser(config.models, config.size),
        model=f"moonshine-{config.size}-streaming-en",
        max_connections=config.max_connections,
    )
    turn.register(application, turn.SmartTurn(config.models / SMART_TURN))
    logging.info(f"listening on {config.host}:{config.port} with Moonshine {config.size}")
    web.run_app(
        application,
        host=config.host,
        port=config.port,
        access_log=None,
        print=None,
    )


if __name__ == "__main__":
    main()
