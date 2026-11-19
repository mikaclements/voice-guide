"""Model files are taken only with the SHA-256 they were listed with."""

import hashlib
import socket

import pytest

from stt import fetch_models as models
from stt.fetch_models import CHECKSUMS, check, wanted


def test_each_size_lists_every_file_moonshine_loads():
    for size in ("tiny", "small", "medium"):
        names = sorted(path.rsplit("/", 1)[1] for _, path in wanted(size, CHECKSUMS.read_text()))
        assert names == [
            "adapter.ort",
            "cross_kv.ort",
            "decoder_kv.ort",
            "encoder.ort",
            "frontend.model.ort",
            "frontend.weights.ort",
            "streaming_config.json",
            "tokenizer.bin",
        ]


def test_a_size_with_no_files_listed_is_refused():
    with pytest.raises(ValueError):
        wanted("large", CHECKSUMS.read_text())


def test_a_file_whose_sha256_differs_is_refused_and_removed(tmp_path):
    model = tmp_path / "encoder.ort"
    model.write_bytes(b"weights")
    with pytest.raises(ValueError, match="SHA-256"):
        check(model, hashlib.sha256(b"other weights").hexdigest())
    assert not model.exists()


def test_a_host_that_stops_answering_fails_the_fetch(tmp_path, monkeypatch):
    with socket.create_server(("127.0.0.1", 0)) as silent:
        monkeypatch.setattr(models, "BASE_URL", f"http://127.0.0.1:{silent.getsockname()[1]}/")
        monkeypatch.setattr(models, "FETCH_TIMEOUT", 0.2)
        with pytest.raises(TimeoutError):
            models.fetch(tmp_path, "tiny")


def test_a_file_whose_sha256_matches_is_kept(tmp_path):
    model = tmp_path / "encoder.ort"
    model.write_bytes(b"weights")
    check(model, hashlib.sha256(b"weights").hexdigest())
    assert model.exists()
