"""Unit tests for ``atlas.client.auth`` — the qatlas auth CLI module.

Covers the parts that are reachable without spawning a real terminal:
- the YAML store (load/save round-trip, malformed file tolerance)
- host normalisation (the key the rest of the client looks up by)
- the public ``get_stored_token`` accessor
- redaction (security: never echo full PAT plaintext to status output)
- the cobra-style subcommand handlers (login --token / logout / status /
  token) — driven through ``main()`` so the argparse wiring is also
  exercised

The interactive ``getpass.getpass`` path of ``login`` is the one
deliberately-unreachable branch: covering it requires PTY hijinks
that aren't worth the test fixture complexity. Non-interactive paths
(``--token`` flag, ``--with-token`` stdin) handle every CI / scripting
need and ARE covered here.

All tests use an XDG_CONFIG_HOME pointed at a per-test tmp_path so
the developer's real ``~/.config/qatlas/hosts.yml`` never leaks into
or out of test runs.
"""

from __future__ import annotations

import io
import os
import stat

import pytest
import yaml

from qatlas.client import auth


@pytest.fixture(autouse=True)
def _isolate_xdg(monkeypatch, tmp_path):
    """Force every test to see a fresh empty config dir."""
    monkeypatch.setenv("XDG_CONFIG_HOME", str(tmp_path))
    # Also wipe QATLAS_SERVER_URL so default-host resolution doesn't
    # pull a value from the developer's shell. Individual tests that
    # need it set will do so explicitly.
    monkeypatch.delenv("QATLAS_SERVER_URL", raising=False)


# ---------------------------------------------------------------------------
# host normalisation
# ---------------------------------------------------------------------------


@pytest.mark.parametrize(
    "raw,expected",
    [
        ("quantum-atlas.ai", "quantum-atlas.ai"),
        ("https://quantum-atlas.ai", "quantum-atlas.ai"),
        ("https://quantum-atlas.ai/", "quantum-atlas.ai"),
        ("https://QUANTUM-atlas.ai/", "quantum-atlas.ai"),
        ("http://quantum-atlas.ai:4200/foo/bar", "quantum-atlas.ai:4200"),
        ("https://203.0.113.10", "203.0.113.10"),
        ("203.0.113.10", "203.0.113.10"),
        ("", ""),
        ("   ", ""),
    ],
)
def test_normalize_host_canonicalisation(raw, expected):
    """All of these surface forms must map onto the same stored key
    so that ``qatlas auth login -H quantum-atlas.ai`` is visible to
    a later ``qatlas ingest`` whose ``QATLAS_SERVER_URL`` is
    ``https://quantum-atlas.ai/``.
    """
    assert auth._normalize_host(raw) == expected


# ---------------------------------------------------------------------------
# store load / save round-trip
# ---------------------------------------------------------------------------


def test_save_then_load_roundtrip(tmp_path):
    store = {"hosts": {"quantum-atlas.ai": {"token": "qat_xyz", "added_at": "2026-01-01"}}}
    auth._save_store(store)
    again = auth._load_store()
    assert again == store


def test_load_missing_file_returns_empty():
    assert auth._load_store() == {"hosts": {}}


def test_load_malformed_yaml_returns_empty(tmp_path, capsys):
    path = auth.hosts_file()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("::: not valid yaml :::")
    out = auth._load_store()
    assert out == {"hosts": {}}
    # User should see a hint, not a silent failure.
    captured = capsys.readouterr()
    assert "not valid YAML" in captured.err


def test_load_non_dict_yaml_returns_empty(tmp_path):
    path = auth.hosts_file()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("just a string, not a mapping")
    assert auth._load_store() == {"hosts": {}}


def test_save_uses_atomic_rename_no_tmp_leftover(tmp_path):
    """The temp-then-rename pattern means no half-written file is ever
    visible to readers. Verify the .tmp file isn't lingering after a
    successful save.
    """
    auth._save_store({"hosts": {"a": {"token": "qat_x"}}})
    tmp_leftover = auth.hosts_file().with_suffix(auth.hosts_file().suffix + ".tmp")
    assert not tmp_leftover.exists()


def test_save_sets_0600_file_mode(tmp_path):
    """Credentials file must not be world / group readable."""
    auth._save_store({"hosts": {"x": {"token": "qat_y"}}})
    mode = stat.S_IMODE(os.stat(auth.hosts_file()).st_mode)
    # Permissive about higher bits some filesystems can't strip, but
    # the bottom two octets MUST be 00.
    assert mode & 0o077 == 0, f"hosts.yml mode {oct(mode)} leaks read bits to group/other"


# ---------------------------------------------------------------------------
# get_stored_token public accessor
# ---------------------------------------------------------------------------


def test_get_stored_token_matches_normalised_host():
    auth._save_store({"hosts": {"quantum-atlas.ai": {"token": "qat_StoredXyz"}}})
    # Various surface forms all resolve to the same canonical host.
    assert auth.get_stored_token("https://quantum-atlas.ai") == "qat_StoredXyz"
    assert auth.get_stored_token("quantum-atlas.ai") == "qat_StoredXyz"
    assert auth.get_stored_token("https://quantum-atlas.ai/") == "qat_StoredXyz"


def test_get_stored_token_missing_host_returns_empty():
    auth._save_store({"hosts": {"a.example": {"token": "qat_a"}}})
    assert auth.get_stored_token("b.example") == ""


def test_get_stored_token_empty_host_returns_empty():
    assert auth.get_stored_token("") == ""


# ---------------------------------------------------------------------------
# redaction
# ---------------------------------------------------------------------------


def test_redact_keeps_only_prefix_and_4_chars_of_pat():
    """PAT redaction: ``qat_AbCdEfGh...`` → ``qat_AbCd********`` so an
    operator can recognise their token but a screen-share leak can't
    yield the secret.
    """
    full = "qat_AbCdEfGhIjKlMnOpQrStUvWxYz"
    redacted = auth._redact(full)
    assert redacted.startswith("qat_AbCd")
    assert "EfGh" not in redacted  # everything past 4 body chars masked
    assert redacted.endswith("*" * 8)
    # And critically: the full secret must NOT be a substring of the
    # redacted output (no off-by-one that re-includes the tail).
    assert full not in redacted


def test_redact_empty_returns_empty():
    assert auth._redact("") == ""


def test_redact_short_jwt_still_masked():
    """A short / weird token (not PAT-shaped) still gets masked so
    rendering doesn't expose the whole value.
    """
    out = auth._redact("eyJabc.def.ghi")
    assert "ghi" not in out
    assert "*" in out


# ---------------------------------------------------------------------------
# subcommand integration via main()
# ---------------------------------------------------------------------------


def test_login_via_token_flag_persists_and_status_lists(tmp_path, capsys):
    rc = auth.main(["login", "-H", "quantum-atlas.ai", "--token", "qat_TestPlaintextAbc"])
    assert rc == 0

    # File on disk: token round-trips intact (no truncation, no
    # extra newline mangling).
    store = auth._load_store()
    assert store["hosts"]["quantum-atlas.ai"]["token"] == "qat_TestPlaintextAbc"
    assert store["hosts"]["quantum-atlas.ai"]["added_at"]  # ISO 8601 stamp

    # status shows it (redacted).
    capsys.readouterr()  # drop login chatter
    rc = auth.main(["status"])
    assert rc == 0
    out = capsys.readouterr().out
    assert "quantum-atlas.ai" in out
    assert "qat_Test" in out  # prefix visible
    assert "PlaintextAbc" not in out  # tail must NOT be visible


def test_login_via_with_token_stdin(monkeypatch, capsys):
    monkeypatch.setattr("sys.stdin", io.StringIO("qat_FromStdin12345\n"))
    rc = auth.main(["login", "-H", "qatlas.example", "--with-token"])
    assert rc == 0
    assert auth.get_stored_token("qatlas.example") == "qat_FromStdin12345"


def test_login_warns_on_non_pat_token(capsys):
    """A non-PAT token (e.g. JWT) is accepted but produces a warning
    so the user knows their stored cred will expire in 14 days.
    """
    rc = auth.main(["login", "-H", "x.example", "--token", "eyJabc.def.ghi"])
    assert rc == 0
    err = capsys.readouterr().err
    assert "does not begin with 'qat_'" in err


def test_login_empty_token_is_error(monkeypatch):
    monkeypatch.setattr("sys.stdin", io.StringIO(""))
    rc = auth.main(["login", "-H", "x.example", "--with-token"])
    assert rc == 1


def test_login_no_host_no_env_no_arg_is_error(monkeypatch):
    """Without --host AND without QATLAS_SERVER_URL, login must error
    rather than prompt indefinitely (the test runner has no TTY).
    Simulate "user just hits enter" by feeding an empty stdin line.
    """
    monkeypatch.setattr("sys.stdin", io.StringIO("\n"))
    rc = auth.main(["login", "--token", "qat_irrelevant"])
    assert rc == 2  # argparse-style usage error


def test_logout_is_idempotent(capsys):
    # Logging out a never-logged-in host succeeds with rc=0.
    rc = auth.main(["logout", "-H", "qatlas.example"])
    assert rc == 0
    assert "No credentials stored" in capsys.readouterr().err

    # Login then logout actually removes the entry.
    auth.main(["login", "-H", "qatlas.example", "--token", "qat_xyz"])
    assert auth.get_stored_token("qatlas.example") == "qat_xyz"
    rc = auth.main(["logout", "-H", "qatlas.example"])
    assert rc == 0
    assert auth.get_stored_token("qatlas.example") == ""


def test_status_empty_returns_nonzero(capsys):
    rc = auth.main(["status"])
    assert rc == 1
    err = capsys.readouterr().err
    assert "not logged into any QuantumAtlas hosts" in err


def test_token_subcommand_prints_plaintext(capsys):
    auth.main(["login", "-H", "qatlas.example", "--token", "qat_pipeMe"])
    capsys.readouterr()  # drop login chatter
    rc = auth.main(["token", "-H", "qatlas.example"])
    assert rc == 0
    out = capsys.readouterr().out
    # Token is on stdout, exactly one line, no extra adornment — this
    # is the contract that makes `curl -H "Bearer $(qatlas auth token)"`
    # work.
    assert out.strip() == "qat_pipeMe"


def test_token_subcommand_unknown_host_is_error(capsys):
    rc = auth.main(["token", "-H", "never.logged.in"])
    assert rc == 1
    err = capsys.readouterr().err
    assert "Not logged into" in err


def test_token_subcommand_falls_back_to_qatlas_server_url(monkeypatch, capsys):
    """Omitting --host on `token` must use QATLAS_SERVER_URL so a
    user-friendly default exists for shell substitution.
    """
    monkeypatch.setenv("QATLAS_SERVER_URL", "https://quantum-atlas.ai")
    auth.main(["login", "-H", "quantum-atlas.ai", "--token", "qat_envHost"])
    capsys.readouterr()
    rc = auth.main(["token"])
    assert rc == 0
    assert capsys.readouterr().out.strip() == "qat_envHost"


# ---------------------------------------------------------------------------
# YAML on disk has the documented schema
# ---------------------------------------------------------------------------


def test_on_disk_schema_is_documented_shape(capsys):
    auth.main(["login", "-H", "quantum-atlas.ai", "--token", "qat_SchemaCheck"])
    raw = auth.hosts_file().read_text()
    parsed = yaml.safe_load(raw)
    # Top-level key is "hosts".
    assert "hosts" in parsed
    # Each host entry has "token" + "added_at" keys, no other secrets.
    entry = parsed["hosts"]["quantum-atlas.ai"]
    assert set(entry.keys()) == {"token", "added_at"}
    # No surprise nested secrets / token_hash / etc.
    assert entry["token"] == "qat_SchemaCheck"
