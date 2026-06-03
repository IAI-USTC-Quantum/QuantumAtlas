"""``qatlas config`` — manage the user-level config file.

v0.16.0+ uses YAML (``~/.config/qatlas/config.yaml``) instead of the
legacy ``.env`` format. The first ``qatlas config init`` invocation
against a legacy ``.env`` will auto-migrate values to the new format
and rename the original to ``.env.migrated-from-v0.16.0`` (kept, not
deleted, so the user can roll back if something looks wrong).

Subcommands:

* ``qatlas config path``            — print the active config file path
                                      and which precedence rule matched.
* ``qatlas config init``            — create ``~/.config/qatlas/config.yaml``
                                      from a template, or auto-migrate
                                      from an existing legacy ``.env``.
* ``qatlas config show``            — dump the current resolved config
                                      (sensitive values masked unless
                                      ``--unmask``).
* ``qatlas config get <KEY>``       — print one resolved value (suitable
                                      for shell interpolation).
* ``qatlas config set <KEY> <VAL>`` — write a key=value pair to the user
                                      config file, creating it if absent.
* ``qatlas config unset <KEY>``     — remove a key from the user config
                                      file.

Keys are addressed by their CANONICAL ENV-VAR NAME
(``QATLAS_SERVER_URL``, ``MINERU_API_TOKEN``, ...) in set/get/unset so
existing user muscle memory carries over from the .env era. The YAML
structure on disk is grouped (``server.url``, ``mineru.api_token``,
...) but users never have to think about that mapping.

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
import stat
import sys
import tempfile
from datetime import datetime, timezone
from pathlib import Path
from typing import Dict, List, Optional

from qatlas import config_yaml
from qatlas.paths import (
    resolve_config_path,
    user_config_yaml_path,
    user_dotenv_path,
)

# Keys that should be masked in `qatlas config show` output (and on any
# trace log) unless the user passes --unmask. Matching is case-insensitive
# substring against the env-var NAME (not the value, so we don't leak via
# a long URL that happens to contain "key").
_SENSITIVE_SUBSTRINGS = ("TOKEN", "SECRET", "KEY", "PASSWORD")


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


# ---------------------------------------------------------------------------
# Legacy .env parsing — kept ONLY for the migration path. New code should
# go through qatlas.config_yaml.
# ---------------------------------------------------------------------------


def _parse_dotenv(path: Path) -> Dict[str, str]:
    """Parse a dotenv file into an ordered key→value dict.

    Tolerant of comments, blank lines, and ``KEY="quoted"`` forms.
    Used ONLY by the .env → config.yaml auto-migration path; new
    code should not call this.
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
        if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
            value = value[1:-1]
        out[key] = value
    return out


# ---------------------------------------------------------------------------
# Atomic file write
# ---------------------------------------------------------------------------


def _write_text_atomic(path: Path, content: str) -> None:
    """Write content to path via tempfile + rename. Sets mode 0600 so
    secret-bearing config files (PAT, MinerU JWT, ...) don't leak via
    group/other read permission.
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, tmp_name = tempfile.mkstemp(
        prefix=path.name + ".", suffix=".tmp", dir=str(path.parent)
    )
    try:
        os.write(fd, content.encode("utf-8"))
        os.close(fd)
        os.chmod(tmp_name, stat.S_IRUSR | stat.S_IWUSR)  # 0600
        os.replace(tmp_name, path)
    except Exception:
        try:
            os.unlink(tmp_name)
        except OSError:
            pass
        raise


# ---------------------------------------------------------------------------
# Key validation
# ---------------------------------------------------------------------------


_VALID_KEY_RE = re.compile(r"^[A-Z_][A-Z0-9_]*$")


def _validate_key(key: str) -> None:
    """Reject syntactically invalid env-var names early."""
    if not _VALID_KEY_RE.match(key):
        raise SystemExit(
            f"invalid key {key!r}: must match {_VALID_KEY_RE.pattern} "
            "(uppercase, digits, underscores; no leading digit)"
        )


def _ensure_known_key(key: str) -> None:
    """Reject env-var names that have no YAML schema home.

    Without this, ``qatlas config set QATLS_TOKEN ...`` (typo) would
    silently no-op. Caller should call this AFTER ``_validate_key``.
    """
    if config_yaml.yaml_path_for_env(key) is None:
        known = ", ".join(sorted(config_yaml.YAML_TO_ENV.values()))
        raise SystemExit(
            f"{key!r} is not a recognised client config key.\n"
            f"Known keys: {known}\n"
            f"For server-side env vars (NEO4J_*, QATLAS_S3_*, etc.) edit the\n"
            f"server's .env directly — they're not part of the client config.yaml."
        )


# ---------------------------------------------------------------------------
# Auto-migration from legacy .env
# ---------------------------------------------------------------------------


def _migrate_dotenv_to_yaml(dotenv_path: Path, yaml_path: Path) -> List[str]:
    """One-shot migration: read legacy .env, write equivalent YAML,
    rename the .env to ``.env.migrated-from-v0.16.0`` so nothing is
    silently lost.

    Returns the list of env-var names that had no YAML home and were
    therefore dropped (caller should warn the user).
    """
    env_pairs = _parse_dotenv(dotenv_path)
    if not env_pairs:
        return []

    yaml_dict = config_yaml.env_dict_to_yaml(env_pairs)
    dropped = config_yaml.unmigrated_keys(env_pairs)

    yaml_path.parent.mkdir(parents=True, exist_ok=True)
    _write_text_atomic(yaml_path, config_yaml.dump_yaml(yaml_dict))

    # Rename rather than delete — the rollback path is "rename
    # .env.migrated-* back to .env, delete config.yaml, downgrade
    # qatlas". Far better UX than "oh, your secrets are in trash".
    timestamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    backup = dotenv_path.with_name(f".env.migrated-from-v0.16.0.{timestamp}")
    dotenv_path.rename(backup)
    print(
        f"Migrated {dotenv_path.name} → {yaml_path.name}; "
        f"original kept as {backup.name} for safety.",
        file=sys.stderr,
    )
    return dropped


# ---------------------------------------------------------------------------
# Subcommand handlers
# ---------------------------------------------------------------------------


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

    # Auto-migration path: legacy .env present, no yaml yet → migrate
    # silently regardless of --force. Migration is non-destructive
    # (renames the .env) so it's always safe to attempt.
    if legacy_dotenv.is_file() and not yaml_target.exists():
        dropped = _migrate_dotenv_to_yaml(legacy_dotenv, yaml_target)
        if dropped:
            _print_err(
                f"Note: {len(dropped)} key(s) had no YAML home and were dropped:\n"
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

    # If --force is refreshing an existing yaml, seed from it so we
    # don't blow away values the user has already configured.
    seed: Dict[str, object] = {}
    if yaml_target.exists() and args.force:
        seed = config_yaml.load_yaml_file(yaml_target)
        if seed:
            _print_err(f"Refreshing {yaml_target}: preserving existing values.")

    _write_text_atomic(yaml_target, config_yaml.dump_yaml(seed))
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
    _ensure_known_key(args.key)

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

    if not config_yaml.set_yaml_value(data, args.key, value):
        # _ensure_known_key already filters unknown keys; this branch
        # should be unreachable but kept as defensive belt-and-braces.
        raise SystemExit(f"could not set {args.key}: not a known YAML key")

    _write_text_atomic(target, config_yaml.dump_yaml(data))
    if _is_sensitive(args.key):
        print(f"set {args.key}={_mask(value)} in {target}")
    else:
        print(f"set {args.key}={value} in {target}")
    return 0


def cmd_unset(args: argparse.Namespace) -> int:
    _validate_key(args.key)
    _ensure_known_key(args.key)

    target = user_config_yaml_path()
    if not target.is_file():
        _print_err(f"{target} does not exist; nothing to unset")
        return 1

    data = config_yaml.load_yaml_file(target)
    if not config_yaml.unset_yaml_value(data, args.key):
        _print_err(f"{args.key} not set in {target}")
        return 1

    _write_text_atomic(target, config_yaml.dump_yaml(data))
    print(f"unset {args.key} in {target}")
    return 0


def cmd_get(args: argparse.Namespace) -> int:
    """Resolve a value through the FULL precedence chain
    (CLI flag > env > config file). Reading just what's in the file is
    `cat $(qatlas config path)`.
    """
    _validate_key(args.key)

    # Tier 1: real OS env var (always wins).
    env_value = os.environ.get(args.key)
    if env_value is not None and env_value != "":
        print(env_value)
        return 0

    # Tier 2: resolved value from the active config file (yaml or .env).
    path, _ = resolve_config_path()
    if path is None:
        return 1

    if path.suffix.lower() in (".yaml", ".yml"):
        data = config_yaml.load_yaml_file(path)
        value = config_yaml.get_yaml_value(data, args.key)
    else:
        # Legacy .env: look up by env-var name directly.
        pairs = _parse_dotenv(path)
        value = pairs.get(args.key)

    if value is None:
        return 1
    print(value)
    return 0


def cmd_show(args: argparse.Namespace) -> int:
    """Dump every YAML-managed env var with its currently resolved value
    (env > config file precedence), plus a few unmodelled keys consumed
    elsewhere (QATLAS_TOKEN is read by qatlas.client._common, etc.).
    """
    path, source = resolve_config_path()
    print(f"# config source: {path or '(env-only)'} ({source or 'no-file'})")
    print()

    # Build the resolved value map: env wins, then config file fills gaps.
    resolved: Dict[str, Optional[str]] = {}

    if path is not None:
        if path.suffix.lower() in (".yaml", ".yml"):
            data = config_yaml.load_yaml_file(path)
            for _, env_name in config_yaml.YAML_TO_ENV.items():
                resolved[env_name] = config_yaml.get_yaml_value(data, env_name)
        else:
            file_pairs = _parse_dotenv(path)
            for env_name in config_yaml.YAML_TO_ENV.values():
                resolved[env_name] = file_pairs.get(env_name)
    else:
        for env_name in config_yaml.YAML_TO_ENV.values():
            resolved[env_name] = None

    # Real env vars beat the file (matches the resolution order).
    for env_name in list(resolved.keys()):
        env_value = os.environ.get(env_name)
        if env_value is not None and env_value != "":
            resolved[env_name] = env_value

    # Unmodelled keys consumed by other parts of the CLI.
    EXTRA_KEYS = ("QATLAS_TOKEN", "QATLAS_CONFIG", "QATLAS_DOTENV", "QATLAS_SKIP_DOTENV")
    for env_name in EXTRA_KEYS:
        if env_name in resolved:
            continue
        env_value = os.environ.get(env_name)
        # Legacy dotenv files might also stash QATLAS_TOKEN; pull from
        # there as a courtesy.
        if (env_value is None or env_value == "") and path is not None and path.suffix.lower() not in (".yaml", ".yml"):
            file_pairs = _parse_dotenv(path)
            env_value = file_pairs.get(env_name)
        resolved[env_name] = env_value

    for env_name in sorted(resolved):
        value = resolved[env_name]
        rendered = "" if value is None else str(value)
        if rendered and _is_sensitive(env_name) and not args.unmask:
            rendered = _mask(rendered)
        print(f"{env_name}={rendered}")
    return 0


# ---------------------------------------------------------------------------
# Parser plumbing
# ---------------------------------------------------------------------------


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

    sp = subs.add_parser("init", help="Create ~/.config/qatlas/config.yaml (auto-migrates legacy .env)")
    sp.add_argument(
        "--force", action="store_true",
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
