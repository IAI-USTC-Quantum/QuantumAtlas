"""Tests for ``qatlas config <subcommand>`` (v0.16.0+ YAML format).

Covers the user-visible behaviours after the YAML migration:

* file is created with 0600 perms, in the right XDG location, with the
  right extension (.yaml, not .env)
* `set`/`get`/`unset` round-trip through the YAML schema
* sensitive values masked on `show` / `set` echo, unmaskable
* `get` resolves via the full precedence chain (env > file)
* `init` auto-migrates a legacy ``.env`` to ``config.yaml`` and
  renames the original to ``.env.migrated-from-v0.16.0.*``
* unknown env-var names rejected (typo guard)
"""

from __future__ import annotations

import stat
from pathlib import Path
from typing import Iterator

import pytest

from qatlas.client import config as cfg_cmd


@pytest.fixture
def isolated_env(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Iterator[Path]:
    """Clean ``$HOME``, ``XDG_*``, and qatlas env vars so each test sees
    a deterministic empty config slate.
    """
    home = tmp_path / "home"
    home.mkdir()
    monkeypatch.setenv("HOME", str(home))
    for var in (
        "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME",
        "QATLAS_CONFIG", "QATLAS_DOTENV",
        "QATLAS_TOKEN", "QATLAS_SERVER_URL", "QATLAS_INSECURE",
        "MINERU_API_TOKEN", "QATLAS_WIKI_DIR",
        "QATLAS_SKIP_DOTENV", "QUANTUMATLAS_SKIP_DOTENV",
    ):
        monkeypatch.delenv(var, raising=False)
    monkeypatch.chdir(tmp_path)
    yield tmp_path


def _yaml_path(isolated_env: Path) -> Path:
    return isolated_env / "home" / ".config" / "qatlas" / "config.yaml"


def _legacy_dotenv(isolated_env: Path) -> Path:
    return isolated_env / "home" / ".config" / "qatlas" / ".env"


def _run(argv: list[str]) -> int:
    return cfg_cmd.main(argv)


# ---------------------------------------------------------------------------
# init
# ---------------------------------------------------------------------------


class TestInit:
    def test_init_creates_yaml_with_header(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        rc = _run(["init"])
        assert rc == 0
        target = _yaml_path(isolated_env)
        assert target.is_file()
        content = target.read_text()
        assert "QuantumAtlas client config" in content
        assert "qatlas config init/set/unset" in content
        assert "QATLAS_CONFIG" in content
        # Empty bootstrap: no actual YAML keys yet.
        assert "server:" not in content
        assert str(target) in capsys.readouterr().out

    def test_init_refuses_to_overwrite_without_force(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        target = _yaml_path(isolated_env)
        target.parent.mkdir(parents=True)
        target.write_text("server:\n  url: https://keep.example\n")
        rc = _run(["init"])
        assert rc == 1
        err = capsys.readouterr().err
        assert "already exists" in err
        assert "--force" in err
        assert "https://keep.example" in target.read_text()

    def test_init_force_preserves_existing_values(self, isolated_env: Path) -> None:
        target = _yaml_path(isolated_env)
        target.parent.mkdir(parents=True)
        target.write_text(
            "server:\n"
            "  url: https://user.example\n"
            "  token: qat_user_already_set\n"
        )
        assert _run(["init", "--force"]) == 0
        content = target.read_text()
        assert "qat_user_already_set" in content
        assert "https://user.example" in content
        assert "QuantumAtlas client config" in content

    def test_init_file_has_0600_perms(self, isolated_env: Path) -> None:
        assert _run(["init"]) == 0
        target = _yaml_path(isolated_env)
        mode = stat.S_IMODE(target.stat().st_mode)
        assert mode == 0o600

    def test_init_does_not_seed_from_cwd_files(self, isolated_env: Path) -> None:
        (isolated_env / ".env").write_text(
            "QATLAS_SERVER_URL=https://staging.example.com\n"
            "QATLAS_TOKEN=qat_should_not_leak_in\n"
        )
        (isolated_env / "config.yaml").write_text(
            "server:\n  url: https://staging.example.com\n"
        )
        rc = _run(["init"])
        assert rc == 0
        content = _yaml_path(isolated_env).read_text()
        assert "https://staging.example.com" not in content
        assert "qat_should_not_leak_in" not in content


# ---------------------------------------------------------------------------
# auto-migration from legacy .env
# ---------------------------------------------------------------------------


class TestMigration:
    def test_init_migrates_legacy_dotenv_when_no_yaml(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        legacy = _legacy_dotenv(isolated_env)
        legacy.parent.mkdir(parents=True)
        legacy.write_text(
            "QATLAS_SERVER_URL=https://migrated.example.com\n"
            'QATLAS_TOKEN="qat_migrated_token_value"\n'
            "MINERU_API_TOKEN=jwt_migrated\n"
            "QATLAS_INSECURE=1\n"
        )

        assert _run(["init"]) == 0

        target = _yaml_path(isolated_env)
        assert target.is_file()
        content = target.read_text()
        assert "https://migrated.example.com" in content
        assert "qat_migrated_token_value" in content
        assert "jwt_migrated" in content
        assert "insecure: true" in content

        assert not legacy.exists()
        backups = list(legacy.parent.glob(".env.migrated-from-v0.16.0.*"))
        assert len(backups) == 1
        assert backups[0].read_text().startswith("QATLAS_SERVER_URL=")

        err = capsys.readouterr().err
        assert "Migrated" in err
        assert ".env.migrated-from-v0.16.0" in err

    def test_init_does_not_migrate_when_yaml_already_present(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        yaml_path = _yaml_path(isolated_env)
        yaml_path.parent.mkdir(parents=True)
        yaml_path.write_text("server:\n  url: https://keep.example\n")
        legacy = _legacy_dotenv(isolated_env)
        legacy.write_text("QATLAS_SERVER_URL=https://stale.example\n")

        rc = _run(["init"])
        assert rc == 1
        assert legacy.exists()

    def test_migration_warns_about_dropped_server_only_keys(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        legacy = _legacy_dotenv(isolated_env)
        legacy.parent.mkdir(parents=True)
        legacy.write_text(
            "QATLAS_SERVER_URL=https://x.example\n"
            "NEO4J_URI=bolt://server.example:7687\n"
            "NEO4J_PASSWORD=server-side-secret\n"
            "QATLAS_S3_ENDPOINT=https://rustfs.example\n"
        )

        assert _run(["init"]) == 0

        err = capsys.readouterr().err
        assert "no YAML home" in err
        assert "NEO4J_URI" in err


# ---------------------------------------------------------------------------
# set / get / unset
# ---------------------------------------------------------------------------


class TestSetGetUnset:
    def test_set_creates_file_if_missing(self, isolated_env: Path) -> None:
        target = _yaml_path(isolated_env)
        assert not target.exists()
        assert _run(["set", "QATLAS_SERVER_URL", "https://x.example.com"]) == 0
        assert target.is_file()
        content = target.read_text()
        assert "url: https://x.example.com" in content
        assert "server:" in content

    def test_set_then_get_round_trip(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        _run(["set", "QATLAS_SERVER_URL", "https://y.example.com"])
        capsys.readouterr()
        rc = _run(["get", "QATLAS_SERVER_URL"])
        assert rc == 0
        out = capsys.readouterr().out.strip()
        assert out == "https://y.example.com"

    def test_set_bool_normalised_in_yaml(self, isolated_env: Path) -> None:
        _run(["set", "QATLAS_INSECURE", "1"])
        target = _yaml_path(isolated_env)
        assert "insecure: true" in target.read_text()

    def test_set_sensitive_value_masked_in_echo(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        rc = _run(["set", "QATLAS_TOKEN", "qat_VeryLongSensitiveValue1234"])
        assert rc == 0
        out = capsys.readouterr().out
        assert "qat_VeryLongSensitiveValue1234" not in out
        assert "qat_" in out

    def test_unset_removes_key_and_prunes_section(self, isolated_env: Path) -> None:
        _run(["set", "MINERU_API_TOKEN", "jwt_xxx"])
        target = _yaml_path(isolated_env)
        assert "mineru:" in target.read_text()
        rc = _run(["unset", "MINERU_API_TOKEN"])
        assert rc == 0
        content = target.read_text()
        assert "mineru:" not in content

    def test_unset_missing_key_returns_1(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        _run(["set", "QATLAS_SERVER_URL", "v"])
        rc = _run(["unset", "MINERU_API_TOKEN"])
        assert rc == 1
        err = capsys.readouterr().err
        assert "MINERU_API_TOKEN" in err

    def test_unset_unknown_key_rejected(self, isolated_env: Path) -> None:
        with pytest.raises(SystemExit):
            _run(["unset", "QATLAS_TYPO_KEY"])

    def test_set_unknown_key_rejected(self, isolated_env: Path) -> None:
        with pytest.raises(SystemExit) as excinfo:
            _run(["set", "QATLAS_S3_ENDPOINT", "https://rustfs.example"])
        msg = str(excinfo.value)
        assert "QATLAS_S3_ENDPOINT" in msg
        assert "not a recognised client config key" in msg

    def test_invalid_key_syntax_rejected(self, isolated_env: Path) -> None:
        for bad in ("lowercase-key", "1starts_with_digit", "has spaces"):
            with pytest.raises(SystemExit):
                _run(["set", bad, "v"])

    def test_set_sensitive_hidden_prompt_when_value_omitted(
        self,
        isolated_env: Path,
        capsys: pytest.CaptureFixture,
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        """Onboarding sugar: `qatlas config set MINERU_API_TOKEN` (no value)
        prompts with getpass so the JWT never lands in shell history /
        `ps aux` / scrollback. This is what the README's "30-second
        contribute MinerU quota" path relies on.
        """
        secret = "eyJ0eXBlIjoiSldUIiwiYWxnIjoiSFM1MTIifQ.payload.signature"
        captured: dict[str, str] = {}

        def fake_getpass(prompt: str) -> str:
            captured["prompt"] = prompt
            return secret

        # Pretend we're on a tty so cmd_set picks the getpass branch rather
        # than the piped-stdin branch.
        monkeypatch.setattr("sys.stdin.isatty", lambda: True)
        monkeypatch.setattr("getpass.getpass", fake_getpass)

        rc = _run(["set", "MINERU_API_TOKEN"])
        assert rc == 0
        # Prompt mentions the key + "hidden" so user knows what to paste.
        assert "MINERU_API_TOKEN" in captured["prompt"]
        assert "hidden" in captured["prompt"].lower()
        # Value persisted.
        target = _yaml_path(isolated_env)
        assert secret in target.read_text()
        # Echo masks it.
        out = capsys.readouterr().out
        assert secret not in out

    def test_set_value_from_stdin_when_piped(
        self,
        isolated_env: Path,
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        """Script-friendly path: `echo $TOKEN | qatlas config set MINERU_API_TOKEN`
        reads one line from stdin and trims the trailing newline.
        """
        import io

        secret = "eyJ.stdin-piped.value"
        monkeypatch.setattr("sys.stdin.isatty", lambda: False)
        monkeypatch.setattr("sys.stdin", io.StringIO(secret + "\n"))

        rc = _run(["set", "MINERU_API_TOKEN"])
        assert rc == 0
        target = _yaml_path(isolated_env)
        # Load the YAML and check the *stored* value didn't accumulate the
        # trailing newline (YAML naturally ends the file with \n, so a raw
        # substring check would false-positive).
        import yaml
        data = yaml.safe_load(target.read_text())
        assert data["mineru"]["api_token"] == secret

    def test_set_empty_prompted_value_refuses(
        self,
        isolated_env: Path,
        capsys: pytest.CaptureFixture,
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        """Empty hidden input is a user mistake — refuse and point at the
        right command to clear a key (`config unset KEY`)."""
        monkeypatch.setattr("sys.stdin.isatty", lambda: True)
        monkeypatch.setattr("getpass.getpass", lambda prompt: "")

        rc = _run(["set", "MINERU_API_TOKEN"])
        assert rc == 1
        err = capsys.readouterr().err
        assert "empty value" in err.lower()
        assert "qatlas config unset" in err

    def test_get_env_var_overrides_file(
        self, isolated_env: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture
    ) -> None:
        _run(["set", "QATLAS_SERVER_URL", "https://file.example.com"])
        capsys.readouterr()
        monkeypatch.setenv("QATLAS_SERVER_URL", "https://env.example.com")
        rc = _run(["get", "QATLAS_SERVER_URL"])
        assert rc == 0
        out = capsys.readouterr().out.strip()
        assert out == "https://env.example.com"

    def test_get_unknown_key_returns_1(self, isolated_env: Path) -> None:
        assert _run(["get", "QATLAS_TOKEN"]) == 1

    def test_get_respects_legacy_dotenv_when_no_yaml(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        legacy = _legacy_dotenv(isolated_env)
        legacy.parent.mkdir(parents=True)
        legacy.write_text("QATLAS_SERVER_URL=https://legacy.example\n")
        rc = _run(["get", "QATLAS_SERVER_URL"])
        assert rc == 0
        out = capsys.readouterr().out.strip()
        assert out == "https://legacy.example"


# ---------------------------------------------------------------------------
# path
# ---------------------------------------------------------------------------


class TestPath:
    def test_reports_xdg_yaml_when_file_exists(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        _run(["set", "QATLAS_SERVER_URL", "https://x"])
        capsys.readouterr()
        rc = _run(["path"])
        assert rc == 0
        out = capsys.readouterr().out
        assert "xdg_yaml" in out
        assert "config.yaml" in out

    def test_reports_legacy_dotenv_with_source_suffix(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        legacy = _legacy_dotenv(isolated_env)
        legacy.parent.mkdir(parents=True)
        legacy.write_text("QATLAS_SERVER_URL=https://x\n")
        rc = _run(["path"])
        assert rc == 0
        out = capsys.readouterr().out
        assert "xdg_dotenv_legacy" in out
        assert legacy.name in out

    def test_no_file_returns_friendly_message(
        self, isolated_env: Path, capsys: pytest.CaptureFixture
    ) -> None:
        rc = _run(["path"])
        assert rc == 0
        out = capsys.readouterr().out
        assert "no config file" in out
        assert "config.yaml" in out

    def test_qatlas_config_env_override_wins(
        self, isolated_env: Path, monkeypatch: pytest.MonkeyPatch, capsys: pytest.CaptureFixture
    ) -> None:
        custom = isolated_env / "custom.yaml"
        custom.write_text("server:\n  url: https://from-override\n")
        monkeypatch.setenv("QATLAS_CONFIG", str(custom))

        _yaml_path(isolated_env).parent.mkdir(parents=True)
        _yaml_path(isolated_env).write_text("server:\n  url: https://xdg\n")

        rc = _run(["path"])
        assert rc == 0
        out = capsys.readouterr().out
        assert "env_override_yaml" in out
        assert str(custom) in out


# ---------------------------------------------------------------------------
# show
# ---------------------------------------------------------------------------


class TestShow:
    def test_show_masks_sensitive(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        _run(["set", "QATLAS_SERVER_URL", "https://x.example.com"])
        _run(["set", "QATLAS_TOKEN", "qat_abcdefghijklmnopqrstuv"])
        capsys.readouterr()
        rc = _run(["show"])
        assert rc == 0
        out = capsys.readouterr().out
        assert "QATLAS_SERVER_URL=https://x.example.com" in out
        assert "qat_abcdefghijklmnopqrstuv" not in out
        token_line = [l for l in out.splitlines() if l.startswith("QATLAS_TOKEN=")][0]
        assert "qat_" in token_line

    def test_show_unmask_reveals_value(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        _run(["set", "QATLAS_TOKEN", "qat_abcdefghijklmnopqrstuv"])
        capsys.readouterr()
        assert _run(["show", "--unmask"]) == 0
        out = capsys.readouterr().out
        assert "qat_abcdefghijklmnopqrstuv" in out

    def test_show_reports_source(self, isolated_env: Path, capsys: pytest.CaptureFixture) -> None:
        _run(["set", "QATLAS_SERVER_URL", "https://x"])
        capsys.readouterr()
        rc = _run(["show"])
        assert rc == 0
        first = capsys.readouterr().out.splitlines()[0]
        assert "config source" in first
        assert "config.yaml" in first
        assert "xdg_yaml" in first
