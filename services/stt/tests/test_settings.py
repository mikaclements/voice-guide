"""The service's settings, read from the environment."""

from pathlib import Path

import pytest

from stt.__main__ import settings


def test_settings_default_to_the_small_model_listening_on_this_machine_only():
    config = settings({})
    assert (config.host, config.port, config.size, config.models) == (
        "127.0.0.1",
        8092,
        "small",
        Path("models"),
    )


def test_connections_are_capped_at_what_one_process_holds_unless_set():
    assert settings({}).max_connections == 64
    assert settings({"STT_MAX_CONNECTIONS": "2"}).max_connections == 2
    with pytest.raises(ValueError, match="STT_MAX_CONNECTIONS"):
        settings({"STT_MAX_CONNECTIONS": "0"})


def test_a_model_size_moonshine_lacks_stops_the_start():
    with pytest.raises(ValueError, match="STT_MODEL"):
        settings({"STT_MODEL": "large"})
