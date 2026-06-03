"""``qatlas config`` — manage the user-level config file.

v0.17.0 simplification: the YAML schema is derived automatically from
``ServerConfig.model_fields`` (in ``qatlas/config.py``), and the
read/write surface here just maps the operator's env-var-style key
(``QATLAS_SERVER_URL``, ``MINERU_API_TOKEN``) onto the corresponding
snake_case YAML field. No hand-maintained mapping table; adding a new
field to ``ServerConfig`` automatically exposes it through
``qatlas config set/get/unset/show``.

Subcommands: path, init, show, get, set, unset. Keys are addressed by
their canonical env-var name (``QATLAS_SERVER_URL``,
``MINERU_API_TOKEN``, ...) — the identifier operators already know.
Internally ``qatlas.config.env_alias_to_field`` maps it onto the
snake_case YAML key (``server_url``, ``mineru_api_token``) before
reading or writing.

Design notes:

* Writes go through a tempfile + atomic rename so a partial write
  can't corrupt the file even on SIGKILL mid-edit. File mode 0600
  (secrets like PAT, MinerU JWT live here).
* PyYAML is used unconditionally — round-trip comment preservation
  (ruamel.yaml) was rejected because ``set`` will always rewrite
  the file anyway, and ``gh / kubectl`` operate the same way. The
  generated header comment explicitly tells the user this.
* User-facing CLI rejects unknown env var names rather than silently
  ignoring them, so a typo in ``qatlas config set QATLS_TOKEN ...``
  fails loudly.
"""

from __future__ import annotations

import argparse
import os
import re
import sys
from pathlib import Path
from typing import Any, Dict, List, Optional

from qatlas import config_yaml
from qatlas.config import (
    ServerConfig,
    env_alias_to_field,
    field_to_env_alias,
)
from qatlas.paths import (
    resolve_config_path,
    user_config_yaml_path,
    user_dotenv_path,
)

_SENSITIVE_SUBSTRINGS = ("TOKEN", "SECRET", "KEY", "PASSWORD")


def _print_err(msg: str) -> None:
    print(msg, file=sys.stderr)


def _is_sensitive(env_name: str) -> bool:
    upper = env_name.upper()
    return any(s in upper for s in _SENSITIVE_SUBSTRINGS)


def _mask(value: str) -> str:
    if not value:
        return ""
    if len(value) <= 8:
        return "***"
    return f"{value[:4]}\u2026{value[-4:]} ({len(value)} chars)"


_VALID_KEY_RE = re.compile(r"^[A-Z_][A-Z0-9_]*$")


def _validate_key(key: str) -> None:
    if not _VALID_KEY_RE.match(key):
        raise SystemExit(
            f"invalid key {key!r}: must match {_VALID_KEY_RE.pattern} "
            "(uppercase, digits, underscores; no leading digit)"
        )


def _resolve_field(env_name: str) -> str:
    """Map QATLAS_SERVER_URL -> server_url; raise SystemExit with a
    friendly diagnostic when the env-var name doesn't claim any
    ServerConfig field (typo or server-only field like
    QATLAS_S3_ENDPOINT that the client schema doesn't carry).
    """
    field = env_alias_to_field(env_name)
    if field is None:
        known = sorted(
            {field_to_env_alias(name) for name in ServerConfig.model_fields}
            - {None}
        )
        raise SystemExit(
            f"{env_name!r} is not a recognised client config key.\n"
            f"Known keys: {', '.join(known)}\n"
            f"For server-side env vars (NEO4J_*, QATLAS_S3_*, etc.) edit the\n"
            f"server's .env directly - they're not part of the client config.yaml."
        )
    return field


def cmd_path(args: argparse.Namespace) -> int:
    path, source = resolve_config_path()
    if path is None:
        print(
            f"(no config file found; would default to {user_config_yaml_path()})"
        )
        return 0
    print(f"{path}\t({source})")
    return 0


def cmd_init(args: argparse.Namespace) -> int:
    yaml_target = user_config_yaml_path()
    legacy_dotenv = user_dotenv_path()

    if legacy_dotenv.is_file() and not yaml_target.exists():
        dropped = config_yaml.migrate_dotenv_to_yaml(legacy_dotenv, yaml_target)
        if dropped:
            _print_err(
                f"Note: {len(dropped)} key(s) had no client config field and were dropped:\n"
                f"  {', '.join(dropped)}\n"
                f"They were server-side env vars or unknown user vars; not used by "
                f"the qatlas client. If you need them, set them via shell `export`."
            )
        print(f"Wrote {yaml_target}")
        return 0

    if yaml_target.exists() and not args.force:
        _print_err(
            f"{yaml_target} already exists. Use --force to overwrite "
            "(existing values are PRESERVED unless overwritten)."
        )
        return 1

    seed: Dict[str, Any] = {}
    if yaml_target.exists() and args.force:
        seed = config_yaml.load_yaml_file(yaml_target)
        if seed:
            _print_err(f"Refreshing {yaml_target}: preserving existing values.")

    config_yaml.write_yaml_atomic(yaml_target, seed)
    print(f"Wrote {yaml_target}")
    if not seed:
        print(
            "Edit the file, or run "
            "`qatlas config set QATLAS_SERVER_URL https://...` to populate."
        )
    return 0


def cmd_set(args: argparse.Namespace) -> int:
    import getpass

    _validate_key(args.key)
    field = _resolve_field(args.key)

    value = args.value
    if value is None:
        # No value on the command line: prompt interactively. For sensitive
        # keys (TOKEN / SECRET / KEY / PASSWORD) hide the typed value with
        # getpass so it doesn't end up in scrollback. For piped stdin (CI /
        # scripts) read one line: `echo $TOKEN | qatlas config set KEY`.
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
    data[field] = config_yaml._coerce_for_field(field, value)
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
    _validate_key(args.key)
    field = env_alias_to_field(args.key)

    env_value = os.environ.get(args.key)
    if env_value is not None and env_value != "":
        print(env_value)
        return 0

    if field is not None:
        cfg = ServerConfig.from_env()
        value = getattr(cfg, field, None)
        if value is not None and value != "":
            if isinstance(value, bool):
                print("true" if value else "false")
            else:
                print(value)
            return 0
    return 1


def cmd_show(args: argparse.Namespace) -> int:
    path, source = resolve_config_path()
    print(f"# config source: {path or '(env-only)'} ({source or 'no-file'})")
    print()

    cfg = ServerConfig.from_env()

    rendered: Dict[str, Optional[str]] = {}
    for field_name in ServerConfig.model_fields:
        env_name = field_to_env_alias(field_name)
        if env_name is None:
            continue
        value = getattr(cfg, field_name, None)
        if value is None:
            rendered[env_name] = None
        elif isinstance(value, bool):
            rendered[env_name] = "true" if value else "false"
        else:
            rendered[env_name] = str(value)

    for env_name in ("QATLAS_CONFIG", "QATLAS_DOTENV", "QATLAS_SKIP_DOTENV"):
        rendered.setdefault(env_name, os.environ.get(env_name))

    for env_name in sorted(rendered):
        value = rendered[env_name]
        text = "" if value is None else str(value)
        if text and _is_sensitive(env_name) and not args.unmask:
            text = _mask(text)
        print(f"{env_name}={text}")
    return 0


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="qatlas config",
        description=(
            "Manage the user-level qatlas config file "
            f"({user_config_yaml_path()}). Run `qatlas config init` first."
        ),
    )
    subs = p.add_subparsers(dest="subcommand", required=True)

    sp = subs.add_parser("path", help="Print the active config file path")
    sp.set_defaults(func=cmd_path)

    sp = subs.add_parser(
        "init",
        help="Create ~/.config/qatlas/config.yaml (auto-migrates legacy .env)",
    )
    sp.add_argument(
        "--force",
        action="store_true",
        help="Overwrite an existing YAML file (existing values are preserved)",
    )
    sp.set_defaults(func=cmd_init)

    sp = subs.add_parser("set", help="Set a key in the user config file")
    sp.add_argument("key", help="Env var name, e.g. QATLAS_SERVER_URL")
    sp.add_argument(
        "value",
        nargs="?",
        default=None,
        help=(
            "Value. Omit to prompt interactively (hidden for sensitive "
            "keys like *TOKEN / *KEY / *SECRET / *PASSWORD). Reads stdin "
            "if not a tty: `echo $TOKEN | qatlas config set MINERU_API_TOKEN`."
        ),
    )
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
