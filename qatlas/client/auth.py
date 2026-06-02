"""``qatlas auth`` — interactive credential management, analogous to ``gh auth``.

Stores QuantumAtlas Personal Access Tokens (PATs) keyed by hostname in
``~/.config/qatlas/hosts.yml`` so the rest of the client CLI can find a
credential without the user having to ``export QATLAS_TOKEN=`` in every
shell session.

Subcommands::

    qatlas auth login [-h HOST]      Interactively store a PAT for HOST.
    qatlas auth logout [-h HOST]     Drop the stored PAT for HOST.
    qatlas auth status               List configured hosts + token prefixes.
    qatlas auth token [-h HOST]      Print the stored plaintext token.

The store layout is YAML so it stays human-inspectable / hand-editable.
File mode is 0600 to keep co-located users (multi-tenant boxes) from
casually reading PAT plaintexts.

Token resolution precedence used by the rest of the client (see
``_common.resolve_token``):

  1. explicit ``--token <value>`` CLI flag
  2. ``QATLAS_TOKEN`` environment variable
  3. ``~/.config/qatlas/hosts.yml`` entry for the request's host
  4. nothing — request goes out without an Authorization header

The store-backed step (3) is what ``qatlas auth login`` populates and
``qatlas auth logout`` clears.

Why not pull in a third-party config library? Operators are unlikely to
have a lot of hosts (1–3 typical), the schema is tiny (host → token +
two timestamps), and the ergonomics of "I can ``cat hosts.yml`` to
debug" outweigh the cost of writing 60 lines of YAML I/O ourselves.
"""

from __future__ import annotations

import argparse
import datetime as _dt
import getpass
import os
import stat
import sys
import urllib.parse
from pathlib import Path
from typing import Any, Optional

import yaml

# Where the per-user credentials file lives. Respects XDG_CONFIG_HOME
# (gh / docker / git config / etc. all do) so users with a customised
# XDG setup land in the right place automatically.
_CONFIG_DIR_NAME = "qatlas"
_HOSTS_FILE_NAME = "hosts.yml"

# The PAT shape sentinel from internal/pat/pat.go. Kept as a string
# literal here rather than imported from anywhere, because this module
# is client-only and has no business depending on server packages.
_PAT_PREFIX = "qat_"


def config_dir() -> Path:
    """Resolve the QuantumAtlas per-user config directory.

    Honors ``XDG_CONFIG_HOME`` if set (and non-empty), otherwise falls
    back to ``~/.config``. We do NOT create the directory here — the
    write path does, with 0700 permissions, so the dir's mode matches
    the file's mode.
    """
    xdg = os.environ.get("XDG_CONFIG_HOME", "").strip()
    base = Path(xdg) if xdg else Path.home() / ".config"
    return base / _CONFIG_DIR_NAME


def hosts_file() -> Path:
    """Absolute path of the YAML credentials store."""
    return config_dir() / _HOSTS_FILE_NAME


def _load_store() -> dict[str, Any]:
    """Read the hosts file into a dict. Missing file → empty dict.

    Tolerant of empty / null / non-dict file contents (returns an empty
    dict in all those cases) so a one-time hand-edit can't break the
    rest of the CLI.
    """
    path = hosts_file()
    if not path.exists():
        return {"hosts": {}}
    try:
        loaded = yaml.safe_load(path.read_text()) or {}
    except yaml.YAMLError as exc:
        # Don't blow up the user's command — surface a clear hint so
        # they can fix or rm the file.
        print(
            f"Warning: {path} is not valid YAML ({exc}); treating as empty.",
            file=sys.stderr,
        )
        return {"hosts": {}}
    if not isinstance(loaded, dict):
        return {"hosts": {}}
    hosts = loaded.get("hosts")
    if not isinstance(hosts, dict):
        loaded["hosts"] = {}
    return loaded


def _save_store(store: dict[str, Any]) -> None:
    """Write the store atomically with mode 0600, mkdir -p with 0700.

    Uses a temp-rename so an interrupted write can't leave a half-
    serialised file that breaks the next read.
    """
    path = hosts_file()
    path.parent.mkdir(parents=True, exist_ok=True)
    try:
        os.chmod(path.parent, 0o700)
    except OSError:
        pass  # filesystem may not support chmod (Windows, FAT mounts, ...).

    tmp = path.with_suffix(path.suffix + ".tmp")
    tmp.write_text(yaml.safe_dump(store, sort_keys=True))
    try:
        os.chmod(tmp, stat.S_IRUSR | stat.S_IWUSR)  # 0600
    except OSError:
        pass
    os.replace(tmp, path)


def _normalize_host(value: str) -> str:
    """Map any of {"quantum-atlas.ai", "https://quantum-atlas.ai",
    "https://quantum-atlas.ai/", "quantum-atlas.ai:4200"} onto a single
    canonical form: scheme-less, no trailing slash, lowercased host.

    Without this normalisation a user who runs ``qatlas auth login`` on
    a bare hostname but ``qatlas ingest`` with ``QATLAS_SERVER_URL=https://...``
    would never see the stored token surface.
    """
    s = value.strip()
    if not s:
        return ""
    if "://" not in s:
        s = "//" + s
    parsed = urllib.parse.urlsplit(s)
    host = (parsed.netloc or parsed.path).lower().rstrip("/")
    return host


def host_from_server_url(server_url: str) -> str:
    """Public helper so ``_common.resolve_token`` can match stored
    entries against the server URL it just computed.
    """
    return _normalize_host(server_url)


def get_stored_token(server_url: str) -> str:
    """Return the stored PAT for the host derived from ``server_url``,
    or "" if none is configured. Called by ``_common.resolve_token``.
    """
    host = host_from_server_url(server_url)
    if not host:
        return ""
    store = _load_store()
    entry = store.get("hosts", {}).get(host)
    if not isinstance(entry, dict):
        return ""
    token = entry.get("token", "")
    return str(token).strip()


def _redact(token: str) -> str:
    """Render a token for human display: keep the prefix + the next 4
    chars, mask the rest. Mirrors how ``gh auth status`` masks tokens.
    """
    if not token:
        return ""
    if token.startswith(_PAT_PREFIX) and len(token) > len(_PAT_PREFIX) + 4:
        return token[: len(_PAT_PREFIX) + 4] + "*" * 8
    # Non-PAT (e.g. JWT) — show first 6 chars then mask.
    if len(token) > 6:
        return token[:6] + "*" * 8
    return "*" * 8


def _default_host_for_login(arg: Optional[str]) -> str:
    """Choose the host to log into when ``--host`` is omitted.

    Order:
      1. explicit ``--host`` argument
      2. ``QATLAS_SERVER_URL`` env (most users have this in .envrc / .env)
      3. prompt the user — we can't guess
    """
    if arg:
        return _normalize_host(arg)
    env = os.environ.get("QATLAS_SERVER_URL", "").strip()
    if env:
        return _normalize_host(env)
    prompted = input("Host (e.g. quantum-atlas.ai): ").strip()
    return _normalize_host(prompted)


def _default_host_for_lookup(arg: Optional[str]) -> str:
    """Like ``_default_host_for_login`` but skips the interactive prompt
    (status / token / logout shouldn't sit on stdin if the user forgot
    to set anything — they should error out loud).
    """
    if arg:
        return _normalize_host(arg)
    env = os.environ.get("QATLAS_SERVER_URL", "").strip()
    if env:
        return _normalize_host(env)
    return ""


# ---------------------------------------------------------------------------
# Subcommand: login
# ---------------------------------------------------------------------------


def _cmd_login(args: argparse.Namespace) -> int:
    host = _default_host_for_login(args.host)
    if not host:
        print("Error: host is required (--host or QATLAS_SERVER_URL).", file=sys.stderr)
        return 2

    print(
        f"""Logging into {host}.

To mint a Personal Access Token (PAT), open
  https://{host}/pat
in your browser, click "New token", grant the scopes you need (most CI
callers want at least 'papers:write'), set the expiry, and copy the
plaintext.

The plaintext starts with '{_PAT_PREFIX}' and is shown only once.""",
        file=sys.stderr,
    )

    if args.token:
        # Non-interactive path for scripts: --token / --with-token. Use
        # getpass-less route so the value can come from a pipe.
        token = args.token.strip()
    elif args.with_token:
        # `qatlas auth login --with-token < tokenfile` — read stdin.
        token = sys.stdin.read().strip()
    else:
        # Interactive paste. getpass hides the value from terminal /
        # ~/.bash_history.
        token = getpass.getpass("Paste your PAT plaintext: ").strip()

    if not token:
        print("Error: empty token; aborting.", file=sys.stderr)
        return 1

    if not token.startswith(_PAT_PREFIX):
        # Don't refuse outright — JWTs are accepted by the server too,
        # but warn so the user knows what they've stored.
        print(
            f"Warning: token does not begin with '{_PAT_PREFIX}'. "
            "Storing anyway (assumed session JWT — note JWTs typically expire in 14 days).",
            file=sys.stderr,
        )

    store = _load_store()
    store.setdefault("hosts", {})[host] = {
        "token": token,
        "added_at": _dt.datetime.now(_dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    }
    _save_store(store)

    print(f"✓ Logged in to {host} (stored at {hosts_file()})", file=sys.stderr)
    print(f"  Token: {_redact(token)}", file=sys.stderr)
    return 0


# ---------------------------------------------------------------------------
# Subcommand: logout
# ---------------------------------------------------------------------------


def _cmd_logout(args: argparse.Namespace) -> int:
    host = _default_host_for_lookup(args.host)
    if not host:
        print("Error: host is required (--host or QATLAS_SERVER_URL).", file=sys.stderr)
        return 2

    store = _load_store()
    hosts = store.get("hosts", {})
    if host not in hosts:
        print(f"No credentials stored for {host}.", file=sys.stderr)
        return 0  # idempotent; not an error
    del hosts[host]
    _save_store(store)
    print(f"✓ Logged out of {host}.", file=sys.stderr)
    return 0


# ---------------------------------------------------------------------------
# Subcommand: status
# ---------------------------------------------------------------------------


def _cmd_status(args: argparse.Namespace) -> int:
    store = _load_store()
    hosts = store.get("hosts", {})
    if not hosts:
        print(
            f"You are not logged into any QuantumAtlas hosts.\n"
            f"Run `qatlas auth login` to add one (config will land at {hosts_file()}).",
            file=sys.stderr,
        )
        return 1

    # Honour --host if supplied — useful in scripts that want a single
    # host's status to grep.
    requested = _normalize_host(args.host) if args.host else ""

    any_match = False
    for host in sorted(hosts.keys()):
        if requested and host != requested:
            continue
        any_match = True
        entry = hosts[host]
        token = entry.get("token", "") if isinstance(entry, dict) else ""
        added = entry.get("added_at", "") if isinstance(entry, dict) else ""
        kind = "PAT" if token.startswith(_PAT_PREFIX) else "JWT (rotates every ~14 days)"
        print(f"{host}")
        print(f"  ✓ Logged in (stored at {hosts_file()})")
        print(f"  - Token type:  {kind}")
        print(f"  - Token value: {_redact(token)}")
        if added:
            print(f"  - Added:       {added}")
        print()

    if requested and not any_match:
        print(f"Not logged into {requested}.", file=sys.stderr)
        return 1
    return 0


# ---------------------------------------------------------------------------
# Subcommand: token
# ---------------------------------------------------------------------------


def _cmd_token(args: argparse.Namespace) -> int:
    host = _default_host_for_lookup(args.host)
    if not host:
        print("Error: host is required (--host or QATLAS_SERVER_URL).", file=sys.stderr)
        return 2
    store = _load_store()
    entry = store.get("hosts", {}).get(host)
    if not isinstance(entry, dict) or not entry.get("token"):
        print(f"Not logged into {host}. Run `qatlas auth login -h {host}`.", file=sys.stderr)
        return 1
    # stdout: the plaintext, exactly one line — for piping.
    print(entry["token"])
    return 0


# ---------------------------------------------------------------------------
# Argparse wiring
# ---------------------------------------------------------------------------


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="qatlas auth",
        description=(
            "Manage QuantumAtlas credentials (PATs / session tokens), analogous "
            "to `gh auth`. Stores per-host secrets at ~/.config/qatlas/hosts.yml."
        ),
    )
    sub = parser.add_subparsers(dest="action", metavar="ACTION")
    sub.required = True

    p_login = sub.add_parser(
        "login",
        help="Interactively store a PAT for a host",
        description=(
            "Prompt for a Personal Access Token plaintext (or take it from "
            "--token / stdin), then store it under the host's entry in "
            "~/.config/qatlas/hosts.yml. Subsequent `qatlas ingest`, "
            "`qatlas upload`, etc. will pick it up automatically (no need "
            "to export QATLAS_TOKEN)."
        ),
    )
    p_login.add_argument("--host", "-H", help="hostname to log into (defaults to QATLAS_SERVER_URL or prompt)")
    p_login.add_argument(
        "--token",
        "-t",
        default="",
        help="PAT plaintext (avoid in shell history; prefer --with-token < file or interactive prompt)",
    )
    p_login.add_argument(
        "--with-token",
        action="store_true",
        help="read PAT plaintext from stdin (script-friendly)",
    )
    p_login.set_defaults(func=_cmd_login)

    p_logout = sub.add_parser("logout", help="Drop the stored PAT for a host")
    p_logout.add_argument("--host", "-H", help="hostname to log out of (defaults to QATLAS_SERVER_URL)")
    p_logout.set_defaults(func=_cmd_logout)

    p_status = sub.add_parser("status", help="Show configured hosts + token shape")
    p_status.add_argument("--host", "-H", help="only show one host's entry")
    p_status.set_defaults(func=_cmd_status)

    p_token = sub.add_parser(
        "token",
        help="Print the stored token for piping into other tools",
        description=(
            "Prints the stored plaintext for one host on stdout. Suitable for "
            "shell substitution: `curl -H \"Authorization: Bearer $(qatlas auth token)\" ...`."
        ),
    )
    p_token.add_argument("--host", "-H", help="hostname (defaults to QATLAS_SERVER_URL)")
    p_token.set_defaults(func=_cmd_token)

    return parser


def main(argv: Optional[list[str]] = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
