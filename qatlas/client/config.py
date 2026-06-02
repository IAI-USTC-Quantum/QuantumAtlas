"""``qatlas config`` — manage the user-level config file.

Subcommands:

* ``qatlas config path``            — print the active config file path
                                      and which precedence rule matched.
* ``qatlas config init``            — create ``~/.config/qatlas/.env``
                                      from a template (interactive when
                                      stdin is a TTY).
* ``qatlas config show``            — dump the current resolved config
                                      (sensitive values masked unless
                                      ``--unmask``).
* ``qatlas config get <KEY>``       — print one resolved value (suitable
                                      for shell interpolation).
* ``qatlas config set <KEY> <VAL>`` — write a key=value pair to the user
                                      config file, creating it if absent.
* ``qatlas config unset <KEY>``     — remove a key from the user config
                                      file.

Design: the file we write to is **always** the XDG location returned by
:func:`qatlas.paths.user_dotenv_path` (i.e.
``~/.config/qatlas/.env`` unless ``XDG_CONFIG_HOME`` is set).
We never write to the legacy ``./.env`` — that's strictly read-only
fallback. Writes go through a tempfile + atomic rename so a partial
write can't corrupt the file even on SIGKILL mid-edit.
"""

from __future__ import annotations

import argparse
import os
import re
import shutil
import stat
import sys
import tempfile
from pathlib import Path
from typing import Dict, List, Optional

from qatlas.paths import resolve_dotenv_path, user_dotenv_path

# Keys that should be masked in `qatlas config show` output (and on any
# trace log) unless the user passes --unmask. Matching is case-insensitive
# substring.
_SENSITIVE_SUBSTRINGS = ("TOKEN", "SECRET", "KEY", "PASSWORD")

# Recognised QATLAS_* / MINERU_* keys with a one-line hint for `config init`.
# Order matters — this is the template ordering. ``required`` keys are
# always emitted uncommented (with empty value if no seed); optional keys
# are emitted commented unless the seed has a value.
_TEMPLATE: List[tuple[str, str, bool]] = [
    # (key, one-line hint, required?)
    ("QATLAS_SERVER_URL",
     "URL of the qatlas server, e.g. https://quantum-atlas.ai",
     True),
    ("QATLAS_TOKEN",
     "Personal access token with papers:write scope. Mint at <server>/pat",
     True),
    ("QATLAS_INSECURE",
     "Set to 1 if the server uses a self-signed cert",
     False),
    ("MINERU_API_TOKEN",
     "Required to run `qatlas mineru` with your own MinerU quota",
     False),
    ("QATLAS_WIKI_DIR",
     "Local checkout of the wiki repo (default: ../QuantumAtlas-Wiki)",
     False),
]


def _print_err(msg: str) -> None:
    print(msg, file=sys.stderr)


def _is_sensitive(key: str) -> bool:
    upper = key.upper()
    return any(s in upper for s in _SENSITIVE_SUBSTRINGS)


def _mask(value: str) -> str:
    if not value:
        return ""
    if len(value) <= 8:
        return "***"
    return f"{value[:4]}…{value[-4:]} ({len(value)} chars)"


def _parse_dotenv(path: Path) -> Dict[str, str]:
    """Parse a dotenv file into an ordered key→value dict.

    Tolerant of comments (``#`` prefix), blank lines, and ``KEY="quoted"``
    forms. Does NOT do shell-style variable expansion — kept simple so
    `config set` round-trips cleanly.
    """
    out: Dict[str, str] = {}
    if not path.is_file():
        return out
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        if "=" not in line:
            continue
        key, _, value = line.partition("=")
        key = key.strip()
        value = value.strip()
        # Strip matching surrounding quotes.
        if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
            value = value[1:-1]
        out[key] = value
    return out


_VALID_KEY_RE = re.compile(r"^[A-Z_][A-Z0-9_]*$")


def _validate_key(key: str) -> None:
    """Reject keys that wouldn't survive a dotenv round-trip."""
    if not _VALID_KEY_RE.match(key):
        raise SystemExit(
            f"invalid key {key!r}: must match {_VALID_KEY_RE.pattern} "
            "(uppercase, digits, underscores; no leading digit)"
        )


def _write_dotenv_atomic(path: Path, content: str) -> None:
    """Write content to path via tempfile + rename. Sets mode 0600 since
    the file is expected to contain secrets (PAT, MinerU token)."""
    path.parent.mkdir(parents=True, exist_ok=True)
    # Tempfile lives in the same dir so os.replace is atomic (POSIX rule:
    # rename across filesystems may copy, breaking atomicity).
    fd, tmp_name = tempfile.mkstemp(
        prefix=".env.", suffix=".tmp", dir=str(path.parent)
    )
    try:
        os.write(fd, content.encode("utf-8"))
        os.close(fd)
        os.chmod(tmp_name, stat.S_IRUSR | stat.S_IWUSR)  # 0600
        os.replace(tmp_name, path)
    except Exception:
        # Best-effort cleanup of the orphan tempfile if rename failed.
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise


def _dotenv_serialize(pairs: Dict[str, str]) -> str:
    """Render an ordered dict back to dotenv text, quoting values that
    contain whitespace or special chars to survive re-reads."""
    lines = []
    for key, value in pairs.items():
        if value == "" or any(c.isspace() for c in value) or any(c in value for c in "#'\"`$"):
            # Use double quotes; escape any embedded double-quote.
            escaped = value.replace("\\", "\\\\").replace('"', '\\"')
            lines.append(f'{key}="{escaped}"')
        else:
            lines.append(f"{key}={value}")
    return "\n".join(lines) + "\n"


# ---------------------------------------------------------------------------
# Subcommand handlers
# ---------------------------------------------------------------------------


def cmd_path(args: argparse.Namespace) -> int:
    path, source = resolve_dotenv_path()
    if path is None:
        print(f"(no config file found; XDG candidate would be {user_dotenv_path()})")
        return 0
    print(f"{path}\t({source})")
    if args.canonical and source != "xdg":
        _print_err(
            f"note: this is the legacy cwd location. Run "
            f"`qatlas config init` to migrate to {user_dotenv_path()}."
        )
    return 0


def cmd_init(args: argparse.Namespace) -> int:
    target = user_dotenv_path()
    if target.exists() and not args.force:
        _print_err(
            f"{target} already exists. Use --force to overwrite "
            "(existing values are PRESERVED unless overwritten)."
        )
        return 1

    # Pre-fill from cwd .env if it exists, so migration is one command.
    seed: Dict[str, str] = {}
    cwd_env = Path.cwd() / ".env"
    if cwd_env.is_file() and cwd_env.resolve() != target.resolve():
        seed = _parse_dotenv(cwd_env)
        if seed:
            _print_err(
                f"Seeding from existing {cwd_env} ({len(seed)} keys). "
                "Review the resulting file before deleting the cwd copy."
            )

    lines = [
        "# QuantumAtlas client config",
        "# Managed by `qatlas config set/unset`; safe to hand-edit.",
        f"# Location: {target}",
        "#",
        "# Resolution order (see `qatlas config path`):",
        "#   1. CLI flag (--server-url / --token / --insecure)",
        "#   2. OS env var (QATLAS_*, MINERU_*)",
        "#   3. QATLAS_DOTENV=<file>",
        "#   4. This file",
        "#   5. ./.env (legacy, deprecated)",
        "",
    ]
    for key, hint, required in _TEMPLATE:
        value = seed.get(key, "")
        lines.append(f"# {hint}")
        if value or required:
            # Emit uncommented: required keys always present (even if empty,
            # to nudge the user to fill it in); optional keys uncommented
            # only when seeded.
            lines.append(f"{key}={value}")
        else:
            lines.append(f"# {key}=")
        lines.append("")

    # Append any seed keys not already covered by the template.
    template_keys = {k for k, _, _ in _TEMPLATE}
    extras = {k: v for k, v in seed.items() if k not in template_keys}
    if extras:
        lines.append("# Inherited from existing cwd .env (not in template):")
        for k, v in extras.items():
            lines.append(f"{k}={v}")
        lines.append("")

    _write_dotenv_atomic(target, "\n".join(lines))
    print(f"Wrote {target}")
    if seed:
        print("Set keys:", ", ".join(sorted(k for k, v in seed.items() if v)))
        _print_err(
            f"Migration done. You can now `rm {cwd_env}` or keep it "
            "(env vars + XDG file take precedence anyway)."
        )
    else:
        print("Edit the file or run `qatlas config set QATLAS_SERVER_URL <url>` etc.")
    return 0


def cmd_set(args: argparse.Namespace) -> int:
    _validate_key(args.key)
    target = user_dotenv_path()
    pairs = _parse_dotenv(target)
    pairs[args.key] = args.value
    _write_dotenv_atomic(target, _dotenv_serialize(pairs))
    if _is_sensitive(args.key):
        print(f"set {args.key}={_mask(args.value)} in {target}")
    else:
        print(f"set {args.key}={args.value} in {target}")
    return 0


def cmd_unset(args: argparse.Namespace) -> int:
    _validate_key(args.key)
    target = user_dotenv_path()
    pairs = _parse_dotenv(target)
    if args.key not in pairs:
        _print_err(f"{args.key} not set in {target}")
        return 1
    del pairs[args.key]
    _write_dotenv_atomic(target, _dotenv_serialize(pairs))
    print(f"unset {args.key} in {target}")
    return 0


def cmd_get(args: argparse.Namespace) -> int:
    # We deliberately resolve through the FULL precedence chain — env var
    # overrides file etc — because that's what `qatlas` actually uses.
    # Reading "what's literally in the file" is `cat $(qatlas config path)`.
    from qatlas.config import ServerConfig
    cfg = ServerConfig.from_env()
    field_value = _resolve_env_key(cfg, args.key)
    if field_value is None:
        # Unmodelled key (e.g. QATLAS_TOKEN, user-defined): walk env var
        # then dotenv file — same precedence the rest of the CLI uses.
        field_value = os.environ.get(args.key)
        if field_value is None:
            path, _ = resolve_dotenv_path()
            if path:
                pairs = _parse_dotenv(path)
                field_value = pairs.get(args.key)
    if field_value is None:
        return 1
    print(field_value)
    return 0


def cmd_show(args: argparse.Namespace) -> int:
    from qatlas.config import ServerConfig
    cfg = ServerConfig.from_env()

    path, source = resolve_dotenv_path()
    print(f"# config source: {path or '(env-only)'} ({source or 'no-file'})")
    print()
    # Dump every QATLAS_* / MINERU_* style field with the env-var name
    # users will set, not the snake_case field name.
    fields_with_aliases = _list_env_aliases(cfg)

    # Unmodelled keys consumed elsewhere (e.g. QATLAS_TOKEN is read at
    # request time in qatlas/client/_common.py). List them so users see
    # the full operational picture in one place.
    EXTRA_KEYS = ("QATLAS_TOKEN", "QATLAS_DOTENV", "QATLAS_SKIP_DOTENV")
    file_pairs = _parse_dotenv(path) if path else {}
    for env_name in EXTRA_KEYS:
        if env_name in fields_with_aliases:
            continue  # already covered by model
        # Show what's in env > file > unset.
        value = os.environ.get(env_name) or file_pairs.get(env_name) or None
        fields_with_aliases.setdefault(env_name, value)

    for env_name, value in sorted(fields_with_aliases.items()):
        rendered = "" if value is None else str(value)
        if rendered and _is_sensitive(env_name) and not args.unmask:
            rendered = _mask(rendered)
        print(f"{env_name}={rendered}")
    return 0


def _resolve_env_key(cfg, env_key: str) -> Optional[str]:
    """Return the resolved value for a given QATLAS_* env-var name, or
    None if no model field claims that alias."""
    for env_name, value in _list_env_aliases(cfg).items():
        if env_name == env_key:
            return None if value is None else str(value)
    return None


def _list_env_aliases(cfg) -> Dict[str, object]:
    """Walk the pydantic-settings model and return {env_name: value} for
    every field that has at least one ``validation_alias``. We use the
    first alias as the canonical surface name (typically ``QATLAS_*``)."""
    out: Dict[str, object] = {}
    # model_fields on the class (not the instance) avoids a Pydantic
    # 2.11 deprecation warning.
    for field_name, field in type(cfg).model_fields.items():
        alias = field.validation_alias
        if alias is None:
            continue
        # AliasChoices wraps multiple aliases; plain string is the simple case.
        if hasattr(alias, "choices"):
            primary = next(iter(alias.choices))
        else:
            primary = alias
        if hasattr(primary, "alias"):  # AliasPath / similar
            primary = primary.alias
        out[str(primary)] = getattr(cfg, field_name, None)
    return out


# ---------------------------------------------------------------------------
# Parser plumbing
# ---------------------------------------------------------------------------


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="qatlas config",
        description=(
            "Manage the user-level qatlas config file "
            f"({user_dotenv_path()}). Run `qatlas config init` first."
        ),
    )
    subs = p.add_subparsers(dest="subcommand", required=True)

    sp = subs.add_parser("path", help="Print the active config file path")
    sp.add_argument(
        "--canonical", action="store_true",
        help="Warn when the active path is the legacy cwd fallback",
    )
    sp.set_defaults(func=cmd_path)

    sp = subs.add_parser("init", help="Create ~/.config/qatlas/.env from a template")
    sp.add_argument(
        "--force", action="store_true",
        help="Overwrite an existing file (existing values are seeded back in)",
    )
    sp.set_defaults(func=cmd_init)

    sp = subs.add_parser("set", help="Set a key in the user config file")
    sp.add_argument("key", help="Env var name, e.g. QATLAS_SERVER_URL")
    sp.add_argument("value", help="Value (use --value '' to set empty)")
    sp.set_defaults(func=cmd_set)

    sp = subs.add_parser("unset", help="Remove a key from the user config file")
    sp.add_argument("key", help="Env var name to delete")
    sp.set_defaults(func=cmd_unset)

    sp = subs.add_parser(
        "get", help="Print the resolved value of one key (precedence-aware)"
    )
    sp.add_argument("key", help="Env var name, e.g. QATLAS_SERVER_URL")
    sp.set_defaults(func=cmd_get)

    sp = subs.add_parser("show", help="Dump all resolved config values")
    sp.add_argument(
        "--unmask", action="store_true",
        help="Print sensitive values in full (default: masked)",
    )
    sp.set_defaults(func=cmd_show)

    return p


def main(argv: Optional[List[str]] = None) -> int:
    parser = build_parser()
    args = parser.parse_args(sys.argv[1:] if argv is None else argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
