"""Tests for ``qatlas config <subcommand>`` (v0.17.0+: YAML-only, snake_case keys).

Covers the user-visible behaviours after the v0.17.0 simplification:

* file is created via auto-init (no ``qatlas config init`` subcommand)
* `set`/`get`/`unset` round-trip through the flat YAML schema, using
  snake_case YAML keys (``server_url``, not ``QATLAS_SERVER_URL``)
* sensitive values masked on `show` / `set` echo, ``--unmask`` toggles
* `path` prints the canonical location
* unknown keys rejected (typo guard)
* `--base-url` / `--token` / `--insecure` CLI flags removed in v0.17.0
"""

from __future__ import annotations

import stat
from pathlib import Path
from typing import Iterator

import pytest

from qatlas.client import config as cfg_cmd


@pytest.fixture
def isolated_env(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Iterator[Path]:
    """Clean ``$HOME`` and ``$XDG_*`` so each test sees a deterministic
    empty config slate. v0.17.0 doesn't read env vars for config so we
    don't bother delete-env'ing ``QATLAS_*``.
    """
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("HOME", str(home))
    for var in ("XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"):
        monkeypatch.delenv(var, raising=False)
    monkeypatch.chdir(tmp_path)
    yield tmp_path


def _yaml_path(isolated_env: Path) -> Path:
    return isolated_env / "home" / ".config" / "qatlas" / "config.yaml"


def _run(argv: list[str]) -> int:
    return cfg_cmd.main(argv)


def _run_with_stdin(argv: list[str], stdin_text: str, monkeypatch: pytest.MonkeyPatch) -> int:
    """Invoke `qatlas config ...` piping ``stdin_text`` into stdin.

    Used for `set <sensitive-key>` calls — v0.19.0 rejects the argv
    form for sensitive keys (token / secret / key / password) so the
    secret never enters shell history / `ps` / CI runner log.
    """
    import io
    monkeypatch.setattr("sys.stdin", io.StringIO(stdin_text))
    return cfg_cmd.main(argv)


# ---------------------------------------------------------------------------
# path
# ---------------------------------------------------------------------------


class TestPath:
    def test_prints_canonical_yaml_path(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        rc = _run(["path"])
        assert rc == 0
        out = capsys.readouterr().out.strip()
        assert out == str(_yaml_path(isolated_env))

    def test_warns_when_file_missing(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        _run(["path"])
        err = capsys.readouterr().err
        assert "auto-created" in err

    def test_silent_when_file_exists(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        target = _yaml_path(isolated_env)
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text("server_url: https://x\n")
        _run(["path"])
        err = capsys.readouterr().err
        assert "auto-created" not in err


# ---------------------------------------------------------------------------
# set / unset / get
# ---------------------------------------------------------------------------


class TestSet:
    def test_set_creates_file_with_0600_mode(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        assert _run(["set", "server_url", "https://x.example.com"]) == 0
        target = _yaml_path(isolated_env)
        assert target.is_file()
        assert stat.S_IMODE(target.stat().st_mode) == 0o600
        # File body holds the snake_case YAML key, not the env-style name.
        content = target.read_text()
        assert "server_url: https://x.example.com" in content
        assert "QATLAS_SERVER_URL" not in content

    def test_set_preserves_existing_keys(
        self, isolated_env: Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        _run(["set", "server_url", "https://x"])
        _run_with_stdin(["set", "openai_api_key"], "sk-existing_token\n", monkeypatch)
        content = _yaml_path(isolated_env).read_text()
        assert "server_url: https://x" in content
        assert "openai_api_key: sk-existing_token" in content

    def test_set_bool_coerced(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        _run(["set", "insecure", "true"])
        # YAML serialises a python bool as ``true``, not the literal "true".
        content = _yaml_path(isolated_env).read_text()
        assert "insecure: true" in content

    def test_set_int_coerced(self, isolated_env: Path) -> None:
        _run(["set", "mineru_timeout", "300"])
        content = _yaml_path(isolated_env).read_text()
        assert "mineru_timeout: 300" in content
        # Verify it's the int, not the string "300", by reading through
        # ServerConfig.
        from qatlas.config import ServerConfig

        assert ServerConfig.from_env().mineru_timeout == 300

    def test_set_sensitive_value_masked_in_output(
        self, isolated_env: Path,
        monkeypatch: pytest.MonkeyPatch,
        capsys: pytest.CaptureFixture,
    ) -> None:
        rc = _run_with_stdin(["set", "openai_api_key"],
                             "sk-VeryLongSensitiveValue1234\n", monkeypatch)
        assert rc == 0
        out = capsys.readouterr().out
        assert "sk-VeryLongSensitiveValue1234" not in out
        assert "sk-" in out  # head visible
        assert "1234" in out  # tail visible

    def test_set_sensitive_value_rejected_on_argv(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        """Sensitive keys must NOT accept the value on argv — the
        secret would leak into shell history / `ps` / CI runner log.
        Steer the user to stdin or the TTY prompt instead.
        """
        rc = _run(["set", "openai_api_key", "sk-LeakedOnArgv"])
        assert rc == 2
        err = capsys.readouterr().err
        assert "refusing" in err
        assert "stdin" in err or "echo" in err

    def test_set_rejects_unknown_field(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        with pytest.raises(SystemExit) as exc_info:
            _run(["set", "totally_made_up_field", "value"])
        # Error message should list known keys so the operator can find
        # the right one.
        assert "not a recognised client config key" in str(exc_info.value)

    def test_set_rejects_env_var_style_key(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        # v0.17.0 switched to snake_case YAML keys; QATLAS_SERVER_URL
        # style is rejected by the key validator (uppercase).
        with pytest.raises(SystemExit) as exc_info:
            _run(["set", "QATLAS_SERVER_URL", "https://x"])
        assert "invalid key" in str(exc_info.value)

    def test_set_invalid_key_format(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        with pytest.raises(SystemExit) as exc_info:
            _run(["set", "123abc", "value"])
        assert "invalid key" in str(exc_info.value)


class TestUnset:
    def test_unset_removes_key(self, isolated_env: Path) -> None:
        _run(["set", "server_url", "https://x"])
        rc = _run(["unset", "server_url"])
        assert rc == 0
        content = _yaml_path(isolated_env).read_text()
        assert "server_url" not in content

    def test_unset_missing_key_returns_1(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        _run(["set", "server_url", "https://existing"])
        rc = _run(["unset", "insecure"])
        assert rc == 1
        err = capsys.readouterr().err
        assert "not set" in err

    def test_unset_missing_file_returns_1(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        rc = _run(["unset", "server_url"])
        assert rc == 1


class TestGet:
    def test_get_resolved_value(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        _run(["set", "server_url", "https://x.example"])
        capsys.readouterr()  # discard the set output
        rc = _run(["get", "server_url"])
        assert rc == 0
        assert capsys.readouterr().out.strip() == "https://x.example"

    def test_get_returns_field_default_for_unset_field(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        # mineru_api_base_url has a Field default — get should return it
        # even when no yaml is present.
        rc = _run(["get", "mineru_api_base_url"])
        assert rc == 0
        assert capsys.readouterr().out.strip() == "https://mineru.net"

    def test_get_exits_1_when_value_is_none(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        # server_url has no default; an unset value reports exit 1.
        rc = _run(["get", "server_url"])
        assert rc == 1

    def test_get_unknown_key_exits_1(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        # Unknown valid-format key just exits 1 (no SystemExit) so shell
        # scripts can use `if qatlas config get foo; ...` safely.
        rc = _run(["get", "totally_made_up_field"])
        assert rc == 1

    def test_get_renders_bool(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        _run(["set", "insecure", "true"])
        capsys.readouterr()  # discard the set output
        rc = _run(["get", "insecure"])
        assert rc == 0
        assert capsys.readouterr().out.strip() == "true"


# ---------------------------------------------------------------------------
# show
# ---------------------------------------------------------------------------


class TestShow:
    def test_show_dumps_all_fields(
        self, isolated_env: Path,
        monkeypatch: pytest.MonkeyPatch,
        capsys: pytest.CaptureFixture,
    ) -> None:
        _run(["set", "server_url", "https://x.example.com"])
        _run_with_stdin(["set", "openai_api_key"],
                        "sk-abcdefghijklmnopqrstuv\n", monkeypatch)
        capsys.readouterr()  # discard set output
        _run(["show"])
        out = capsys.readouterr().out
        assert "server_url: https://x.example.com" in out
        # Sensitive value masked by default.
        assert "sk-abcdefghijklmnopqrstuv" not in out
        assert "openai_api_key" in out

    def test_show_unmask_reveals_secrets(
        self, isolated_env: Path,
        monkeypatch: pytest.MonkeyPatch,
        capsys: pytest.CaptureFixture,
    ) -> None:
        _run_with_stdin(["set", "openai_api_key"],
                        "sk-RevealMeFully123\n", monkeypatch)
        capsys.readouterr()
        _run(["show", "--unmask"])
        out = capsys.readouterr().out
        assert "sk-RevealMeFully123" in out

    def test_show_indicates_when_file_missing(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        _run(["show"])
        out = capsys.readouterr().out
        assert "does not exist yet" in out
        # Defaults still surface (e.g. mineru_api_base_url default).
        assert "mineru_api_base_url: https://mineru.net" in out
