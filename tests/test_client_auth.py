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
    so that ``qatlas auth login --server-url quantum-atlas.ai`` is visible to
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


def _login_with(monkeypatch, *args, token: str) -> int:
    """Test helper: invoke ``qatlas auth login ...`` feeding TOKEN over
    stdin via --with-token. Centralizes the monkeypatch dance so each
    test stays one line."""
    monkeypatch.setattr("sys.stdin", io.StringIO(token + "\n"))
    return auth.main(["login", *args, "--with-token"])


def test_login_via_token_flag_persists_and_status_lists(tmp_path, capsys, monkeypatch):
    rc = _login_with(monkeypatch, "--server-url", "quantum-atlas.ai", token="qat_TestPlaintextAbc")
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
    rc = auth.main(["login", "--server-url", "qatlas.example", "--with-token"])
    assert rc == 0
    assert auth.get_stored_token("qatlas.example") == "qat_FromStdin12345"


def test_login_warns_on_non_pat_token(capsys, monkeypatch):
    """A non-PAT token (e.g. JWT) is accepted but produces a warning
    so the user knows their stored cred will expire in 14 days.
    """
    rc = _login_with(monkeypatch, "--server-url", "x.example", token="eyJabc.def.ghi")
    assert rc == 0
    err = capsys.readouterr().err
    assert "does not begin with 'qat_'" in err


def test_login_empty_token_is_error(monkeypatch):
    monkeypatch.setattr("sys.stdin", io.StringIO(""))
    rc = auth.main(["login", "--server-url", "x.example", "--with-token"])
    assert rc == 1


def test_login_no_host_no_env_no_arg_is_error(monkeypatch):
    """Without --host AND without QATLAS_SERVER_URL, login must error
    rather than prompt indefinitely (the test runner has no TTY).
    Simulate "user just hits enter" by feeding an empty stdin line.
    """
    monkeypatch.setattr("sys.stdin", io.StringIO("\n"))
    rc = auth.main(["login", "--with-token"])
    assert rc == 2  # argparse-style usage error


def test_logout_is_idempotent(capsys, monkeypatch):
    # Logging out a never-logged-in host succeeds with rc=0.
    rc = auth.main(["logout", "--server-url", "qatlas.example"])
    assert rc == 0
    assert "No credentials stored" in capsys.readouterr().err

    # Login then logout actually removes the entry.
    _login_with(monkeypatch, "--server-url", "qatlas.example", token="qat_xyz")
    assert auth.get_stored_token("qatlas.example") == "qat_xyz"
    rc = auth.main(["logout", "--server-url", "qatlas.example"])
    assert rc == 0
    assert auth.get_stored_token("qatlas.example") == ""


def test_status_empty_returns_nonzero(capsys):
    rc = auth.main(["status"])
    assert rc == 1
    err = capsys.readouterr().err
    assert "not logged into any QuantumAtlas hosts" in err


def test_token_subcommand_prints_plaintext(capsys, monkeypatch):
    _login_with(monkeypatch, "--server-url", "qatlas.example", token="qat_pipeMe")
    capsys.readouterr()  # drop login chatter
    rc = auth.main(["token", "--server-url", "qatlas.example"])
    assert rc == 0
    out = capsys.readouterr().out
    # Token is on stdout, exactly one line, no extra adornment — this
    # is the contract that makes `curl -H "Bearer $(qatlas auth token)"`
    # work.
    assert out.strip() == "qat_pipeMe"


def test_token_subcommand_unknown_host_is_error(capsys):
    rc = auth.main(["token", "--server-url", "never.logged.in"])
    assert rc == 1
    err = capsys.readouterr().err
    assert "Not logged into" in err


def test_token_subcommand_falls_back_to_yaml_server_url(monkeypatch, tmp_path, capsys):
    """Omitting --host on `token` must use ``server_url:`` from
    ``~/.config/qatlas/config.yaml`` so a user-friendly default exists
    for shell substitution (v0.17.0: yaml-only, no env fallback).
    """
    home = tmp_path / "auth-home"
    home.mkdir()
    cfg_dir = home / ".config" / "qatlas"
    cfg_dir.mkdir(parents=True)
    (cfg_dir / "config.yaml").write_text("server_url: https://quantum-atlas.ai\n")
    monkeypatch.setenv("HOME", str(home))
    monkeypatch.delenv("XDG_CONFIG_HOME", raising=False)
    _login_with(monkeypatch, "--server-url", "quantum-atlas.ai", token="qat_envHost")
    capsys.readouterr()
    rc = auth.main(["token"])
    assert rc == 0
    assert capsys.readouterr().out.strip() == "qat_envHost"


def test_login_token_flag_is_rejected(monkeypatch, capsys):
    """We deliberately do NOT expose --token on argv (only --with-token
    from stdin) so the secret can't end up in shell history / `ps` /
    CI runner log. Mirrors `gh auth login`'s stance.
    """
    # SystemExit raised by argparse when it hits an unknown flag.
    with pytest.raises(SystemExit) as exc:
        auth.main(["login", "--server-url", "x.example", "--token", "qat_xxx"])
    assert exc.value.code != 0


# ---------------------------------------------------------------------------
# YAML on disk has the documented schema
# ---------------------------------------------------------------------------


def test_on_disk_schema_is_documented_shape(capsys, monkeypatch):
    _login_with(monkeypatch, "--server-url", "quantum-atlas.ai", token="qat_SchemaCheck")
    raw = auth.hosts_file().read_text()
    parsed = yaml.safe_load(raw)
    # Top-level key is "hosts".
    assert "hosts" in parsed
    # Each host entry has "token" + "added_at" keys, no other secrets.
    entry = parsed["hosts"]["quantum-atlas.ai"]
    assert set(entry.keys()) == {"token", "added_at"}
    # No surprise nested secrets / token_hash / etc.
    assert entry["token"] == "qat_SchemaCheck"


# ---------------------------------------------------------------------------
# Device flow — mock the HTTP poll loop.
# ---------------------------------------------------------------------------


class _FakeResponse:
    """Minimal ``requests.Response`` shim for the poll loop."""

    def __init__(self, *, status_code: int, payload):
        self.status_code = status_code
        self._payload = payload
        self.text = "" if isinstance(payload, dict) else str(payload)

    def json(self):
        if isinstance(self._payload, Exception):
            raise self._payload
        return self._payload


def test_device_login_happy_path(monkeypatch):
    """pending → pending → approved/200 with token payload."""
    monkeypatch.setattr(auth.webbrowser, "open", lambda *_, **__: True)
    monkeypatch.setattr(auth.time, "sleep", lambda _s: None)  # no real wait

    calls = {"count": 0}

    def fake_post(url, **_):
        calls["count"] += 1
        if url.endswith("/api/oauth/device/code"):
            return _FakeResponse(
                status_code=200,
                payload={
                    "device_code": "DEV-XYZ",
                    "user_code": "WDJB-MJHT",
                    "verification_uri": "https://quantum-atlas.ai/device",
                    "verification_uri_complete": "https://quantum-atlas.ai/device?user_code=WDJB-MJHT",
                    "interval": 1,
                    "expires_in": 600,
                },
            )
        # /token
        if calls["count"] < 4:
            return _FakeResponse(status_code=400, payload={"error": "authorization_pending"})
        return _FakeResponse(
            status_code=200,
            payload={
                "plaintext": "qat_DeviceFlowResult",
                "name": "qatlas-cli-test",
                "prefix": "qat_Devic",
                "scopes": ["papers:write"],
                "expires_at": "2099-01-01",
            },
        )

    monkeypatch.setattr(auth.requests, "post", fake_post)
    received = auth._device_login(
        base_url="https://quantum-atlas.ai",
        verify=True,
        suggested_name="qatlas-cli-test",
        scopes=["papers:write"],
        expires_days=90,
        timeout=10.0,
    )
    assert received["plaintext"] == "qat_DeviceFlowResult"
    assert received["scopes"] == ["papers:write"]


def test_device_login_slow_down_bumps_interval(monkeypatch):
    """slow_down response must NOT raise; it should keep polling at a
    larger interval. We verify by tracking how many times time.sleep
    was called and whether sleep duration grew.
    """
    monkeypatch.setattr(auth.webbrowser, "open", lambda *_, **__: True)

    sleeps: list[float] = []
    monkeypatch.setattr(auth.time, "sleep", lambda s: sleeps.append(s))

    state = {"polls": 0}

    def fake_post(url, **_):
        if url.endswith("/api/oauth/device/code"):
            return _FakeResponse(
                status_code=200,
                payload={
                    "device_code": "DEV",
                    "user_code": "AB-CD",
                    "verification_uri": "x",
                    "verification_uri_complete": "x",
                    "interval": 1,
                    "expires_in": 600,
                },
            )
        state["polls"] += 1
        if state["polls"] == 1:
            return _FakeResponse(status_code=400, payload={"error": "slow_down"})
        if state["polls"] == 2:
            return _FakeResponse(status_code=400, payload={"error": "slow_down"})
        return _FakeResponse(
            status_code=200,
            payload={"plaintext": "qat_Q", "name": "n", "prefix": "qat_Q", "scopes": [], "expires_at": ""},
        )

    monkeypatch.setattr(auth.requests, "post", fake_post)
    auth._device_login(
        base_url="https://x",
        verify=True,
        suggested_name="x",
        scopes=[],
        expires_days=90,
        timeout=30.0,
    )
    # 3 polls → 3 sleeps; sleep[1] > sleep[0] after the first
    # slow_down, sleep[2] > sleep[1] after the second.
    assert len(sleeps) >= 3
    assert sleeps[1] > sleeps[0]
    assert sleeps[2] > sleeps[1]


@pytest.mark.parametrize(
    "kind,match",
    [
        ("access_denied", "denied"),
        ("expired_token", "expired"),
        ("invalid_grant", "rejected"),
    ],
)
def test_device_login_terminal_errors(monkeypatch, kind, match):
    monkeypatch.setattr(auth.webbrowser, "open", lambda *_, **__: True)
    monkeypatch.setattr(auth.time, "sleep", lambda _s: None)

    def fake_post(url, **_):
        if url.endswith("/api/oauth/device/code"):
            return _FakeResponse(
                status_code=200,
                payload={
                    "device_code": "DEV",
                    "user_code": "AB-CD",
                    "verification_uri": "x",
                    "verification_uri_complete": "x",
                    "interval": 1,
                    "expires_in": 600,
                },
            )
        return _FakeResponse(status_code=400, payload={"error": kind})

    monkeypatch.setattr(auth.requests, "post", fake_post)
    with pytest.raises(auth._DeviceFlowError, match=match):
        auth._device_login(
            base_url="https://x",
            verify=True,
            suggested_name="x",
            scopes=[],
            expires_days=90,
            timeout=10.0,
        )


def test_device_login_init_http_error(monkeypatch):
    """/code returning non-200 → DeviceFlowError surfacing the status."""
    monkeypatch.setattr(auth.webbrowser, "open", lambda *_, **__: True)

    def fake_post(*_a, **_k):
        return _FakeResponse(status_code=500, payload={"detail": "boom"})

    monkeypatch.setattr(auth.requests, "post", fake_post)
    with pytest.raises(auth._DeviceFlowError, match="500"):
        auth._device_login(
            base_url="https://x",
            verify=True,
            suggested_name="x",
            scopes=[],
            expires_days=90,
            timeout=10.0,
        )


def test_device_login_open_browser_default_true(monkeypatch):
    """Default behaviour calls webbrowser.open with the deep-link."""
    opened: list[str] = []
    monkeypatch.setattr(
        auth.webbrowser,
        "open",
        lambda url, *_a, **_k: opened.append(url) or True,
    )
    monkeypatch.setattr(auth.time, "sleep", lambda _s: None)

    def fake_post(url, **_):
        if url.endswith("/api/oauth/device/code"):
            return _FakeResponse(
                status_code=200,
                payload={
                    "device_code": "DEV",
                    "user_code": "AB-CD",
                    "verification_uri": "https://srv/device",
                    "verification_uri_complete": "https://srv/device?user_code=AB-CD",
                    "interval": 1,
                    "expires_in": 600,
                },
            )
        return _FakeResponse(
            status_code=200,
            payload={"plaintext": "qat_X", "name": "n", "prefix": "qat_X", "scopes": [], "expires_at": ""},
        )

    monkeypatch.setattr(auth.requests, "post", fake_post)
    auth._device_login(
        base_url="https://srv",
        verify=True,
        suggested_name="x",
        scopes=[],
        expires_days=90,
        timeout=30.0,
    )
    assert opened == ["https://srv/device?user_code=AB-CD"], opened


def test_device_login_open_browser_false_skips_webbrowser(monkeypatch):
    """``open_browser=False`` must NOT call webbrowser.open — that's
    the contract the CLI's --no-browser flag depends on for SSH
    sessions where opening a local browser would do nothing helpful.
    """
    opened: list[str] = []
    monkeypatch.setattr(
        auth.webbrowser,
        "open",
        lambda url, *_a, **_k: opened.append(url) or True,
    )
    monkeypatch.setattr(auth.time, "sleep", lambda _s: None)

    def fake_post(url, **_):
        if url.endswith("/api/oauth/device/code"):
            return _FakeResponse(
                status_code=200,
                payload={
                    "device_code": "DEV",
                    "user_code": "AB-CD",
                    "verification_uri": "https://srv/device",
                    "verification_uri_complete": "https://srv/device?user_code=AB-CD",
                    "interval": 1,
                    "expires_in": 600,
                },
            )
        return _FakeResponse(
            status_code=200,
            payload={"plaintext": "qat_X", "name": "n", "prefix": "qat_X", "scopes": [], "expires_at": ""},
        )

    monkeypatch.setattr(auth.requests, "post", fake_post)
    auth._device_login(
        base_url="https://srv",
        verify=True,
        suggested_name="x",
        scopes=[],
        expires_days=90,
        timeout=30.0,
        open_browser=False,
    )
    assert opened == [], opened


def test_device_login_passes_empty_scopes_by_default(monkeypatch):
    """When the CLI didn't pin --scopes, we must send `scopes: []` to
    the server so the browser-side approve form can default to all-
    checked (the server treats an empty seeded list as "user can pick
    everything"). Regression guard for the v2 redesign.
    """
    monkeypatch.setattr(auth.webbrowser, "open", lambda *_, **__: True)
    monkeypatch.setattr(auth.time, "sleep", lambda _s: None)

    sent_body: dict = {}

    def fake_post(url, json=None, **_):
        if url.endswith("/api/oauth/device/code"):
            sent_body.update(json or {})
            return _FakeResponse(
                status_code=200,
                payload={
                    "device_code": "DEV",
                    "user_code": "AB-CD",
                    "verification_uri": "x",
                    "verification_uri_complete": "x",
                    "interval": 1,
                    "expires_in": 600,
                },
            )
        return _FakeResponse(
            status_code=200,
            payload={"plaintext": "qat_X", "name": "n", "prefix": "qat_X", "scopes": [], "expires_at": ""},
        )

    monkeypatch.setattr(auth.requests, "post", fake_post)
    auth._device_login(
        base_url="https://srv",
        verify=True,
        suggested_name="x",
        scopes=[],
        expires_days=90,
        timeout=30.0,
    )
    assert sent_body.get("scopes") == [], sent_body
