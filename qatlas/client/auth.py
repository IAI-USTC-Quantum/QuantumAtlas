"""``qatlas auth`` — interactive credential management, analogous to ``gh auth``.

Stores QuantumAtlas Personal Access Tokens (PATs) keyed by hostname in
``~/.config/qatlas/hosts.yml`` so the rest of the client CLI can find a
credential without the user having to ``export QATLAS_TOKEN=`` in every
shell session.

Subcommands::

    qatlas auth login [-h HOST]      Open a browser, mint a PAT, store it.
    qatlas auth logout [-h HOST]     Drop the stored PAT for HOST.
    qatlas auth status               List configured hosts + token prefixes.
    qatlas auth token [-h HOST]      Print the stored plaintext token.

``qatlas auth login`` runs an RFC 8628 device-code flow:

  1. POST /api/oauth/device/code → get a short user_code and a
     ``verification_uri_complete`` deep-link that pre-fills the code.
  2. Open the user's browser at that deep-link (or print it so the
     user can paste it into a browser on any other device).
  3. Poll /api/oauth/device/token until the user clicks Approve in
     the browser (where they can also edit the scopes / name / expiry
     before approving) — then write the plaintext PAT into hosts.yml.

``--no-browser`` skips the ``webbrowser.open`` call (you'll just see
the URL printed). ``--with-token`` keeps the script-friendly
"I already have a PAT, just store it" path for CI (reads stdin so
the secret never enters argv / shell history / process listings).

Why this and not a loopback flow? An earlier iteration of this command
spun up a 127.0.0.1 HTTP server and form-POSTed the freshly-minted
token back through it. That worked well on the user's own laptop but
was useless from an SSH session (no browser to navigate to a localhost
URL on a remote box) and added a lot of moving parts — port binding,
Origin checking, CORS-avoiding form-POST, two parallel state machines
client-and-server. Device flow handles both desktop and SSH with one
code path, so we keep just that.

The store layout is YAML so it stays human-inspectable / hand-editable.
File mode is 0600 to keep co-located users (multi-tenant boxes) from
casually reading PAT plaintexts.

Token resolution precedence used by the rest of the client (see
``_common.resolve_token``):

  1. ``~/.config/qatlas/hosts.yml`` entry for the request's host
     (written by ``qatlas auth login`` — OAuth or ``--with-token``)
  2. nothing — request goes out without an Authorization header

(``config.yaml`` ``token:`` field was removed in v0.19.0; it
silently shadowed ALL per-host tokens in hosts.yml, which was a
real footgun. Use ``qatlas auth login --with-token`` to drop a
ready-made PAT into hosts.yml in a CI-friendly way.)

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
import os
import socket
import stat
import sys
import time
import urllib.parse
import webbrowser
from pathlib import Path
from typing import Any, Optional

import requests
import yaml

from qatlas.paths import user_config_dir

# Filename of the per-host credentials store inside the user config dir.
_HOSTS_FILE_NAME = "hosts.yml"

# The PAT shape sentinel from internal/pat/pat.go. Kept as a string
# literal here rather than imported from anywhere, because this module
# is client-only and has no business depending on server packages.
_PAT_PREFIX = "qat_"


def config_dir() -> Path:
    """Resolve the QuantumAtlas per-user config directory.

    Delegates to :func:`qatlas.paths.user_config_dir`, which uses
    ``platformdirs`` to pick the right location per platform
    (``~/.config/qatlas/`` on Linux honoring XDG, ``~/Library/Application
    Support/qatlas/`` on macOS, ``%APPDATA%\\qatlas\\`` on Windows).
    Kept as a separate function so callers reading ``hosts.yml`` and
    callers reading ``config.yaml`` resolve to the same root.

    We do NOT create the directory here — the write path does, with
    0700 permissions, so the dir's mode matches the file's mode.
    """
    return user_config_dir()


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
    a bare hostname but ``qatlas ingest`` with a full URL in
    ``server_url:`` would never see the stored token surface.
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
    """Choose the host to log into when ``--server-url`` is omitted.

    Order:
      1. explicit ``--server-url`` argument
      2. ``server_url`` from ``~/.config/qatlas/config.yaml``
      3. prompt the user — we can't guess
    """
    if arg:
        return _normalize_host(arg)
    try:
        from qatlas.config import ServerConfig

        cfg_url = ServerConfig.from_env().get_server_url()
        if cfg_url:
            return _normalize_host(cfg_url)
    except Exception:
        pass
    prompted = input("Host (e.g. quantum-atlas.ai): ").strip()
    return _normalize_host(prompted)


def _default_host_for_lookup(arg: Optional[str]) -> str:
    """Like ``_default_host_for_login`` but skips the interactive prompt
    (status / token / logout shouldn't sit on stdin if the user forgot
    to set anything — they should error out loud).
    """
    if arg:
        return _normalize_host(arg)
    try:
        from qatlas.config import ServerConfig

        cfg_url = ServerConfig.from_env().get_server_url()
        if cfg_url:
            return _normalize_host(cfg_url)
    except Exception:
        pass
    return ""


# ---------------------------------------------------------------------------
# Subcommand: login
# ---------------------------------------------------------------------------


def _cmd_login(args: argparse.Namespace) -> int:
    host = _default_host_for_login(args.host)
    if not host:
        print(
            "Error: server URL is required (--server-url / -s, or set server_url: in your qatlas config — run `qatlas config path` to find the file).",
            file=sys.stderr,
        )
        return 2

    # --with-token keeps the non-interactive contract for scripts /
    # CI. We deliberately don't run the browser flow in that case —
    # the user explicitly opted into supplying their own token.
    # Following `gh auth login` we only accept the secret over stdin
    # (never argv) so it can't leak into shell history / `ps` / CI
    # runner log.
    if args.with_token:
        token = sys.stdin.read().strip()
        return _store_manual_token(host, token)

    # Honor --insecure flag (one-shot for this invocation) before we
    # try to talk to the server, so self-signed cert hosts work too.
    if getattr(args, "insecure", False):
        os.environ["QATLAS_INSECURE"] = "1"

    try:
        from qatlas.config import ServerConfig
    except Exception as exc:
        print(f"Error: failed to load qatlas config: {exc}", file=sys.stderr)
        return 1

    cfg = ServerConfig.from_env()
    base_url = _server_base_url(host, cfg=cfg)
    verify: bool = not bool(cfg.insecure)

    scopes = _parse_scopes(args.scopes)
    expires_days = int(args.expires_days)
    if expires_days < 1 or expires_days > 365:
        print("Error: --expires-days must be in 1..365.", file=sys.stderr)
        return 2
    suggested_name = args.token_name or _default_token_name()

    print(f"Logging into {host} via {base_url}.", file=sys.stderr)

    try:
        received = _device_login(
            base_url=base_url,
            verify=verify,
            suggested_name=suggested_name,
            scopes=scopes,
            expires_days=expires_days,
            timeout=float(args.timeout),
            open_browser=not bool(getattr(args, "no_browser", False)),
        )
    except _DeviceFlowError as exc:
        print(f"Error: {exc}", file=sys.stderr)
        return 1

    return _persist_login(host, received)


def _store_manual_token(host: str, token: str) -> int:
    """Persist a hand-supplied token (the --with-token CI path).

    Kept as a separate function so the new flows don't have to worry
    about the empty-token / warn-on-non-PAT-prefix branches.
    """
    if not token:
        print("Error: empty token; aborting.", file=sys.stderr)
        return 1
    if not token.startswith(_PAT_PREFIX):
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


def _persist_login(host: str, received: dict[str, Any]) -> int:
    """Persist a token obtained via the loopback or device flow and
    print a gh-style success summary to stderr.
    """
    token = str(received.get("token") or "").strip()
    if not token:
        print("Error: server returned an empty token; aborting.", file=sys.stderr)
        return 1
    if not token.startswith(_PAT_PREFIX):
        print(
            f"Warning: token does not begin with '{_PAT_PREFIX}'.",
            file=sys.stderr,
        )
    name = str(received.get("name") or "")
    scopes_raw = received.get("scopes") or ""
    if isinstance(scopes_raw, list):
        scopes_display = ", ".join(str(s) for s in scopes_raw) or "(none)"
    else:
        scopes_display = str(scopes_raw) or "(none)"
    expires_at = str(received.get("expires_at") or "")

    store = _load_store()
    store.setdefault("hosts", {})[host] = {
        "token": token,
        "added_at": _dt.datetime.now(_dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    }
    _save_store(store)
    print(f"✓ Logged in to {host} (stored at {hosts_file()})", file=sys.stderr)
    print(f"  Token:   {_redact(token)}", file=sys.stderr)
    if name:
        print(f"  Name:    {name}", file=sys.stderr)
    print(f"  Scopes:  {scopes_display}", file=sys.stderr)
    if expires_at:
        print(f"  Expires: {expires_at}", file=sys.stderr)
    return 0


# ---------------------------------------------------------------------------
# Helpers shared by login flow
# ---------------------------------------------------------------------------


def _parse_scopes(raw: str) -> list[str]:
    """Split a comma list into trimmed, deduped scope names."""
    if not raw:
        return []
    seen: list[str] = []
    for part in raw.split(","):
        cleaned = part.strip()
        if cleaned and cleaned not in seen:
            seen.append(cleaned)
    return seen


def _default_token_name() -> str:
    """``qatlas-cli-<host>-<YYYY-MM-DD>`` so the row in /pat is
    self-explanatory ("oh, that was minted by the laptop two months ago").
    """
    try:
        host = socket.gethostname() or "unknown"
    except Exception:
        host = "unknown"
    # gethostname() can include dots / colons; keep the slug shell-safe.
    host = host.replace(".", "-").replace(":", "-").lower()[:32]
    today = _dt.datetime.now(_dt.timezone.utc).strftime("%Y-%m-%d")
    return f"qatlas-cli-{host}-{today}"


def _server_base_url(host: str, *, cfg: Any) -> str:
    """Resolve the HTTPS base URL for ``host``.

    If the user already configured ``server_url:`` for the same host,
    honor it verbatim (preserves any port / scheme override). Otherwise
    assume ``https://<host>`` — every public QuantumAtlas deployment
    is TLS-fronted; self-signed certs are handled via ``insecure``.
    """
    cfg_url = cfg.get_server_url() if cfg is not None else None
    if cfg_url and _normalize_host(cfg_url) == host:
        return cfg_url.rstrip("/")
    return f"https://{host}"


# ---------------------------------------------------------------------------
# Device flow (RFC 8628)
# ---------------------------------------------------------------------------


class _DeviceFlowError(Exception):
    """Raised for any terminal device-flow failure (denied / expired / HTTP)."""


def _device_login(
    *,
    base_url: str,
    verify: bool,
    suggested_name: str,
    scopes: list[str],
    expires_days: int,
    timeout: float = 600.0,
    open_browser: bool = True,
) -> dict[str, Any]:
    """Run the RFC 8628-style device authorization flow.

    1. POST /api/oauth/device/code → user_code + verification URI.
    2. Print the URI + code to stderr; if ``open_browser`` is true
       (the default), also try ``webbrowser.open`` so the user's
       local browser jumps straight to the deep-link with user_code
       pre-filled. ``open_browser=False`` is the SSH / headless case
       where the user will copy the URL to a browser elsewhere.
    3. Poll /api/oauth/device/token at ``interval`` seconds, bumping
       on ``slow_down``, until we get the token or a terminal error.
    """
    init_body = {
        "name": suggested_name,
        "scopes": scopes,
        "expires_in_days": expires_days,
    }
    try:
        resp = requests.post(
            f"{base_url}/api/oauth/device/code",
            json=init_body,
            verify=verify,
            timeout=15,
        )
    except requests.RequestException as exc:
        raise _DeviceFlowError(f"could not reach {base_url}: {exc}") from exc
    if resp.status_code != 200:
        raise _DeviceFlowError(
            f"/api/oauth/device/code returned HTTP {resp.status_code}: {resp.text[:300]}"
        )
    try:
        payload = resp.json()
    except ValueError as exc:
        raise _DeviceFlowError(f"/api/oauth/device/code returned non-JSON: {exc}") from exc

    device_code = str(payload.get("device_code") or "")
    user_code = str(payload.get("user_code") or "")
    verification_uri = str(payload.get("verification_uri") or "")
    verification_uri_complete = str(
        payload.get("verification_uri_complete") or verification_uri
    )
    interval = max(1, int(payload.get("interval") or 5))
    expires_in = max(60, int(payload.get("expires_in") or 600))
    if not device_code or not user_code:
        raise _DeviceFlowError(
            "server response missing device_code / user_code (got: "
            f"{sorted(payload.keys())})"
        )

    print("", file=sys.stderr)
    print("To finish logging in, open this URL in a browser:", file=sys.stderr)
    print(f"  {verification_uri}", file=sys.stderr)
    print(f"and enter the code:  {user_code}", file=sys.stderr)
    if verification_uri_complete and verification_uri_complete != verification_uri:
        print(f"(or open the deep link: {verification_uri_complete})", file=sys.stderr)
    print("", file=sys.stderr)
    print(f"Waiting for approval (up to {min(int(timeout), expires_in)}s, polling every {interval}s)…", file=sys.stderr)

    if open_browser:
        try:
            webbrowser.open(verification_uri_complete or verification_uri, new=2, autoraise=True)
        except Exception:
            # webbrowser may raise on headless / WSL setups; printed
            # URL is the fallback so this is non-fatal.
            pass

    deadline = time.monotonic() + min(timeout, float(expires_in))
    cur_interval = float(interval)
    while True:
        if time.monotonic() >= deadline:
            raise _DeviceFlowError(
                f"Device flow timed out after {int(timeout)}s without an approve/deny."
            )
        # Sleep BEFORE the first poll: the server enforces a poll
        # interval (slow_down) and the spec recommends not polling
        # immediately after /code returns.
        time.sleep(cur_interval)
        try:
            poll = requests.post(
                f"{base_url}/api/oauth/device/token",
                json={"device_code": device_code},
                verify=verify,
                timeout=15,
            )
        except requests.RequestException as exc:
            # Treat network blips as recoverable; the deadline check
            # above will fire if they persist.
            print(f"  (poll error: {exc}; retrying)", file=sys.stderr)
            continue

        if poll.status_code == 200:
            try:
                return poll.json()
            except ValueError as exc:
                raise _DeviceFlowError(
                    f"approve succeeded but response was not JSON: {exc}"
                ) from exc

        try:
            err = poll.json()
        except ValueError:
            raise _DeviceFlowError(
                f"/api/oauth/device/token returned HTTP {poll.status_code}: {poll.text[:200]}"
            )
        kind = str(err.get("error") or "")
        if kind == "authorization_pending":
            continue
        if kind == "slow_down":
            cur_interval += 5.0
            continue
        if kind == "access_denied":
            raise _DeviceFlowError("Authorization denied in the browser.")
        if kind == "expired_token":
            raise _DeviceFlowError("Device code expired before approval.")
        raise _DeviceFlowError(
            f"server rejected device-code poll: {kind or poll.status_code}"
        )


# ---------------------------------------------------------------------------
# Subcommand: logout
# ---------------------------------------------------------------------------


def _cmd_logout(args: argparse.Namespace) -> int:
    host = _default_host_for_lookup(args.host)
    if not host:
        print("Error: server URL is required (--server-url / -s, or set server_url: in your qatlas config).", file=sys.stderr)
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

    # Honour --server-url if supplied — useful in scripts that want a single
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
        print("Error: server URL is required (--server-url / -s, or set server_url: in your qatlas config).", file=sys.stderr)
        return 2
    store = _load_store()
    entry = store.get("hosts", {}).get(host)
    if not isinstance(entry, dict) or not entry.get("token"):
        print(f"Not logged into {host}. Run `qatlas auth login -s {host}`.", file=sys.stderr)
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
        help="Sign in to a QuantumAtlas host (RFC 8628 device-code flow)",
        description=(
            "Open a browser to authorize a Personal Access Token, then\n"
            "fetch the freshly-minted plaintext automatically. The CLI\n"
            "asks the server for a short user_code, prints (and tries to\n"
            "open) a deep-link to /<lang>/device with the code pre-filled,\n"
            "and polls until you click Approve in the browser.\n\n"
            "In the browser you can edit the scopes, name and expiry\n"
            "before approving — the CLI's --scopes / --token-name /\n"
            "--expires-days flags only seed the defaults.\n\n"
            "Use --no-browser when you're on a headless / SSH host (the\n"
            "URL gets printed instead of opened); open it on any other\n"
            "device.\n\n"
            "For scripts / CI use --with-token, reading PAT plaintext\n"
            "from stdin (the same shape as `gh auth login --with-token`).\n"
            "This skips the browser flow entirely and just stores the\n"
            "supplied PAT in hosts.yml. The secret never enters argv /\n"
            "shell history / process listings."
        ),
        formatter_class=argparse.RawDescriptionHelpFormatter,
        # argparse default abbreviates "--token" → "--token-name". Bad
        # for us: an old script (pre-removal of --token) that passes a
        # PAT secret as `--token qat_xxx` would silently land in the
        # display-name field. allow_abbrev=False makes the unknown
        # flag fail loudly instead.
        allow_abbrev=False,
    )
    p_login.add_argument(
        "--server-url", "-s",
        dest="host",
        metavar="URL",
        help="canonical URL (or bare hostname) of the QuantumAtlas server to log into; auto-normalized into a hosts.yml key. Defaults to server_url: in your qatlas config, else prompt.",
    )
    p_login.add_argument(
        "--with-token",
        action="store_true",
        help="read PAT plaintext from stdin (script-friendly: `cat token | qatlas auth login -s ... --with-token`); mirrors `gh auth login --with-token`",
    )
    p_login.add_argument(
        "--no-browser",
        action="store_true",
        help="don't try to open a local browser — just print the URL (headless / SSH friendly)",
    )
    p_login.add_argument(
        "--scopes",
        default="",
        help="comma-separated list of scopes to seed in the browser approval form (default: empty = all scopes pre-checked)",
    )
    p_login.add_argument(
        "--expires-days",
        type=int,
        default=90,
        help="seed value for the expiry-days field in the browser approval form (1..365, default 90)",
    )
    p_login.add_argument(
        "--token-name",
        default="",
        help="seed value for the token name field in the browser approval form (default: qatlas-cli-<hostname>-<YYYY-MM-DD>)",
    )
    p_login.add_argument(
        "--timeout",
        type=float,
        default=600.0,
        help="seconds to wait for the browser approval (default 600)",
    )
    p_login.add_argument(
        "--insecure",
        action="store_true",
        help="skip TLS verification (self-signed dev certs only; sets QATLAS_INSECURE=1 for this command)",
    )
    p_login.set_defaults(func=_cmd_login)

    p_logout = sub.add_parser("logout", help="Drop the stored PAT for a host")
    p_logout.add_argument(
        "--server-url", "-s",
        dest="host",
        metavar="URL",
        help="canonical URL (or bare hostname) of the server to log out of; defaults to server_url: in your qatlas config.",
    )
    p_logout.set_defaults(func=_cmd_logout)

    p_status = sub.add_parser("status", help="Show configured hosts + token shape")
    p_status.add_argument(
        "--server-url", "-s",
        dest="host",
        metavar="URL",
        help="only show this server's entry (otherwise list all)",
    )
    p_status.set_defaults(func=_cmd_status)

    p_token = sub.add_parser(
        "token",
        help="Print the stored token for piping into other tools",
        description=(
            "Prints the stored plaintext for one server on stdout. Suitable for "
            "shell substitution: `curl -H \"Authorization: Bearer $(qatlas auth token)\" ...`."
        ),
    )
    p_token.add_argument(
        "--server-url", "-s",
        dest="host",
        metavar="URL",
        help="canonical URL (or bare hostname) of the server (defaults to server_url: in your qatlas config)",
    )
    p_token.set_defaults(func=_cmd_token)

    return parser


def main(argv: Optional[list[str]] = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
