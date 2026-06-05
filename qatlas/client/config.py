"""``qatlas config`` — inspect / edit the user-level config file.

v0.17.0: YAML-only config at ``~/.config/qatlas/config.yaml``. Auto-
created on first run of any ``qatlas`` subcommand (no separate ``init``
step). This module provides:

* ``qatlas config path`` — print the canonical YAML path
* ``qatlas config show`` — dump current resolved values (secrets masked)
* ``qatlas config get <key>`` — print one resolved value, exit 1 if unset
* ``qatlas config set <key> <value>`` — write a value (hidden prompt for
  sensitive keys; reads stdin when piped)
* ``qatlas config unset <key>`` — remove a value (no-op if absent)

Keys are the snake_case YAML field names defined on ``ServerConfig``:
``server_url``, ``token``, ``insecure``, ``mineru_api_token``, ... The
full list comes from ``ServerConfig.model_fields`` so adding a field is
automatically reflected here.

Writes go through tempfile + atomic rename; file mode is 0600.
"""

from __future__ import annotations

import argparse
import re
import sys
from typing import Any, Dict, List, Optional

from qatlas import config_yaml
from qatlas.config import ServerConfig
from qatlas.paths import user_config_yaml_path

_SENSITIVE_SUBSTRINGS = ("token", "secret", "key", "password")


def _print_err(msg: str) -> None:
    print(msg, file=sys.stderr)


def _is_sensitive(field_name: str) -> bool:
    lower = field_name.lower()
    return any(s in lower for s in _SENSITIVE_SUBSTRINGS)


def _mask(value: str) -> str:
    if not value:
        return ""
    if len(value) <= 8:
        return "***"
    return f"{value[:4]}\u2026{value[-4:]} ({len(value)} chars)"


_VALID_KEY_RE = re.compile(r"^[a-z_][a-z0-9_]*$")


def _validate_key(key: str) -> None:
    if not _VALID_KEY_RE.match(key):
        raise SystemExit(
            f"invalid key {key!r}: must match {_VALID_KEY_RE.pattern} "
            "(lowercase snake_case, e.g. server_url / mineru_api_token)"
        )


def _resolve_field(key: str) -> str:
    """Map a YAML key onto a known ``ServerConfig`` field.

    Rejects unknown keys with a clear error listing all known keys so a
    typo (``qatlas config set servr_url ...``) fails loudly.
    """
    if key in ServerConfig.model_fields:
        return key
    known = sorted(ServerConfig.model_fields)
    raise SystemExit(
        f"{key!r} is not a recognised client config key.\n"
        f"Known keys: {', '.join(known)}\n"
        f"Server-side fields (NEO4J_*, QATLAS_S3_*, GITHUB_*) live in the\n"
        f"qatlasd server's .env, not here."
    )


def cmd_path(args: argparse.Namespace) -> int:
    """Print the canonical YAML path (whether or not it currently exists)."""
    path = user_config_yaml_path()
    print(path)
    if not path.is_file():
        print("(file not yet created; will be auto-created on next `qatlas` invocation)",
              file=sys.stderr)
    return 0


def cmd_set(args: argparse.Namespace) -> int:
    import getpass

    _validate_key(args.key)
    field = _resolve_field(args.key)

    value = args.value
    if value is not None and _is_sensitive(args.key):
        # Argv form for sensitive keys is a footgun — the secret ends
        # up in shell history / `ps` / CI runner log. Mirror what we
        # do on `auth login` (no argv form for tokens), and steer the
        # user to stdin or the TTY prompt. The argv form stays open
        # for non-sensitive keys like `server_url` / `insecure` so
        # ordinary set-and-commit workflows are unchanged.
        _print_err(
            f"refusing to take {args.key} on the command line "
            "(would leak into shell history / `ps` / CI logs).\n"
            f"Use one of:\n"
            f"  - prompt:  qatlas config set {args.key}        (hidden input on TTY)\n"
            f"  - stdin:   echo $SECRET | qatlas config set {args.key}"
        )
        return 2

    if value is None:
        # No value on the command line: prompt interactively. For sensitive
        # keys (TOKEN / SECRET / KEY / PASSWORD) hide the typed value with
        # getpass so it doesn't end up in scrollback. For piped stdin (CI /
        # scripts) read one line: `echo $TOKEN | qatlas config set mineru_api_token`.
        if not sys.stdin.isatty():
            value = sys.stdin.readline().rstrip("\n\r")
        elif _is_sensitive(args.key):
            value = getpass.getpass(f"{args.key} (hidden): ")
        else:
            value = input(f"{args.key}: ")
        if value == "":
            _print_err(
                f"empty value entered for {args.key}; use "
                f"`qatlas config unset {args.key}` to remove a key instead."
            )
            return 1

    target = user_config_yaml_path()
    data = config_yaml.load_yaml_file(target) if target.is_file() else {}
    data[field] = config_yaml.coerce_for_field(field, value)
    config_yaml.write_yaml_atomic(target, data)

    if _is_sensitive(args.key):
        print(f"set {args.key}={_mask(value)} in {target}")
    else:
        print(f"set {args.key}={value} in {target}")
    return 0


def cmd_unset(args: argparse.Namespace) -> int:
    _validate_key(args.key)
    field = _resolve_field(args.key)

    target = user_config_yaml_path()
    if not target.is_file():
        _print_err(f"{target} does not exist; nothing to unset")
        return 1

    data = config_yaml.load_yaml_file(target)
    if field not in data:
        _print_err(f"{args.key} not set in {target}")
        return 1
    del data[field]
    config_yaml.write_yaml_atomic(target, data)
    print(f"unset {args.key} in {target}")
    return 0


def cmd_get(args: argparse.Namespace) -> int:
    """Print the resolved value of one key. Exit 1 when unset/null.

    The "resolved" value comes from ``ServerConfig.from_env()`` (i.e.
    after Field defaults + YAML overlay), not the raw YAML — so e.g.
    ``qatlas config get mineru_api_base_url`` on a fresh install prints
    ``https://mineru.net`` even though the YAML doesn't carry it.
    """
    _validate_key(args.key)
    if args.key not in ServerConfig.model_fields:
        return 1
    cfg = ServerConfig.from_env()
    value = getattr(cfg, args.key, None)
    if value is None or value == "":
        return 1
    if isinstance(value, bool):
        print("true" if value else "false")
    else:
        print(value)
    return 0


def cmd_show(args: argparse.Namespace) -> int:
    """Dump all field=value pairs after YAML overlay.

    Sensitive fields (containing ``token`` / ``secret`` / ``key`` /
    ``password`` in their name) are masked; pass ``--unmask`` for full
    plaintext (debug only; avoid screen-sharing).
    """
    path = user_config_yaml_path()
    print(f"# config file: {path}{'  (does not exist yet; using defaults)' if not path.is_file() else ''}")
    print()

    cfg = ServerConfig.from_env()

    rendered: Dict[str, Optional[str]] = {}
    for field_name in ServerConfig.model_fields:
        value = getattr(cfg, field_name, None)
        if value is None:
            rendered[field_name] = None
        elif isinstance(value, bool):
            rendered[field_name] = "true" if value else "false"
        else:
            rendered[field_name] = str(value)

    for field_name in sorted(rendered):
        value = rendered[field_name]
        text = "" if value is None else str(value)
        if text and _is_sensitive(field_name) and not args.unmask:
            text = _mask(text)
        print(f"{field_name}: {text}")
    return 0


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="qatlas config",
        description=(
            "Inspect / edit the user-level qatlas config file "
            f"({user_config_yaml_path()}). Auto-created on first run; "
            "edit directly or use the subcommands below."
        ),
    )
    subs = p.add_subparsers(dest="subcommand", required=True)

    sp = subs.add_parser("path", help="Print the config file path")
    sp.set_defaults(func=cmd_path)

    sp = subs.add_parser("set", help="Set a key in the user config file")
    sp.add_argument("key", help="Field name, e.g. server_url / token / mineru_api_token")
    sp.add_argument(
        "value",
        nargs="?",
        default=None,
        help=(
            "Value to set. Omit for an interactive prompt (hidden when "
            "the key looks sensitive — *token / *key / *secret / "
            "*password). Reads stdin when not a tty: "
            "`echo $JWT | qatlas config set mineru_api_token`. "
            "Sensitive keys reject the argv form (use stdin / prompt) "
            "so the secret never leaks into shell history / `ps`."
        ),
    )
    sp.set_defaults(func=cmd_set)

    sp = subs.add_parser("unset", help="Remove a key from the user config file")
    sp.add_argument("key", help="Field name to delete")
    sp.set_defaults(func=cmd_unset)

    sp = subs.add_parser(
        "get", help="Print one resolved value (exit 1 if unset)"
    )
    sp.add_argument("key", help="Field name, e.g. server_url")
    sp.set_defaults(func=cmd_get)

    sp = subs.add_parser("show", help="Dump all resolved config values")
    sp.add_argument(
        "--unmask",
        action="store_true",
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
