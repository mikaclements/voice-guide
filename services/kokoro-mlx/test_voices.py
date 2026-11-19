import pytest

from voices import MAX_SPEED, MIN_SPEED, has_voice, speed_of


@pytest.fixture
def model_dir(tmp_path):
    (tmp_path / "voices").mkdir()
    for name in [
        "bf_emma.safetensors",
        "af_heart.safetensors",
        "am_adam.safetensors",
        "notes.txt",
        "Bad_Name.safetensors",
    ]:
        (tmp_path / "voices" / name).touch()
    return tmp_path


def test_only_a_name_of_one_of_the_models_voices_is_a_voice(model_dir):
    assert has_voice(model_dir, "bf_emma")
    for bad in [
        "zf_nobody",
        "../af_heart",
        "af_heart/../bf_emma",
        "AF_heart",
        "",
        None,
        7,
        ["af_heart"],
    ]:
        assert not has_voice(model_dir, bad), bad


def test_a_request_naming_no_speed_speaks_at_the_voices_own_pace():
    assert speed_of({}) == 1.0
    assert speed_of({"speed": None}) == 1.0


@pytest.mark.parametrize("speed", [MIN_SPEED, 0.75, 1, 1.25, MAX_SPEED])
def test_a_speed_within_the_bounds_is_passed_on(speed):
    assert speed_of({"speed": speed}) == speed


@pytest.mark.parametrize(
    "speed", [0.49, 2.01, 0, -1, float("nan"), float("inf"), "1.0", True, [1.0], {"x": 1}]
)
def test_a_speed_out_of_bounds_or_not_a_number_is_refused(speed):
    assert speed_of({"speed": speed}) is None
