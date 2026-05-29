"""
systemd service installer for QuantumAtlas server.

User-scope services can be installed directly. System-scope services are staged
locally and accompanied by explicit sudo commands for the operator to run.
"""

from __future__ import annotations

import argparse
import getpass
import os
import shutil
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Callable, List, Optional, Sequence

from qatlas.server.config import ServerConfig, get_project_root

Runner = Callable[..., subprocess.CompletedProcess]


def _resolve_enable_now(
    scope: str, enable: Optional[bool], now: Optional[bool]
) -> tuple[bool, bool]:
    """Map CLI --enable/--now to flags. System scope defaults both on when omitted."""
    if scope == "system":
        return (
            True if enable is None else enable,
            True if now is None else now,
        )
    return (
        False if enable is None else enable,
        False if now is None else now,
    )


def _systemd_unit_path(value: str | Path) -> str:
    """Serialize a filesystem path for systemd unit settings.

    WorkingDirectory=, EnvironmentFile=, and the executable token in
    ExecStart= do not strip surrounding double quotes; a value like
    "/foo" is rejected as non-absolute. Use an unquoted path with minimal
    escaping instead.
    """
    text = str(value)
    if '"' in text or "\n" in text or "\r" in text:
        raise ValueError("path must not contain quotes or newlines")
    return text.replace("\\", "\\\\")


@dataclass(frozen=True)
class ServiceSpec:
    """Inputs for rendering a QuantumAtlas systemd unit."""

    name: str = "quantum-atlas"
    scope: str = "user"
    runner: str = "uv"
    app_runner: str = "uvicorn"
    python_executable: Path = Path(sys.executable)
    uv_executable: Path = Path("uv")
    working_dir: Path = get_project_root()
    env_file: Optional[Path] = None
    run_as: Optional[str] = None
    host: str = "127.0.0.1"
    port: int = 4200
    description: str = "QuantumAtlas server"

    @property
    def unit_name(self) -> str:
        return self.name if self.name.endswith(".service") else f"{self.name}.service"


@dataclass(frozen=True)
class ServiceInstallResult:
    """Result of writing or staging a service unit."""

    unit_path: Path
    unit_text: str
    commands: List[List[str]]
    sudo_commands: List[List[str]]


def default_env_file() -> Path:
    """Default project .env path."""
    return get_project_root() / ".env"


def default_user_service_path(name: str) -> Path:
    """Return the user-scope systemd unit path for a service name."""
    config_home = Path(os.getenv("XDG_CONFIG_HOME", Path.home() / ".config"))
    unit_name = name if name.endswith(".service") else f"{name}.service"
    return config_home / "systemd" / "user" / unit_name


def default_system_stage_path(name: str) -> Path:
    """Return the default local staging path for a system-scope unit."""
    unit_name = name if name.endswith(".service") else f"{name}.service"
    return get_project_root() / "build" / "systemd" / unit_name


def detect_runner(
    working_dir: Path,
    *,
    uv_arg: Optional[str] = None,
    python_arg: Optional[str] = None,
) -> tuple[str, Path, Path]:
    """Pick a service runner that matches the local installation."""
    local_uv = working_dir / ".venv" / "bin" / "uv"
    path_uv = shutil.which("uv")
    if uv_arg:
        uv_path = Path(uv_arg).expanduser()
        if uv_path.is_absolute() or uv_path.parent != Path("."):
            return "uv", uv_path, _default_python_path(working_dir, python_arg)
        found = shutil.which(str(uv_path))
        if found:
            return "uv", Path(found), _default_python_path(working_dir, python_arg)
        return "uv", uv_path, _default_python_path(working_dir, python_arg)
    if local_uv.exists():
        return "uv", local_uv, _default_python_path(working_dir, python_arg)
    if path_uv:
        return "uv", Path(path_uv), _default_python_path(working_dir, python_arg)
    return "python", Path("uv"), _default_python_path(working_dir, python_arg)


def _default_python_path(working_dir: Path, python_arg: Optional[str]) -> Path:
    if python_arg:
        return Path(python_arg).expanduser()
    local_python = working_dir / ".venv" / "bin" / "python"
    if local_python.exists():
        return local_python
    return Path(sys.executable)


def _systemd_exec_arg(value: str | int) -> str:
    """Serialize a simple ExecStart argument without shell expansion."""
    text = str(value)
    if (
        not text
        or any(ch.isspace() for ch in text)
        or '"' in text
        or "\n" in text
        or "\r" in text
    ):
        raise ValueError(
            "ExecStart argument must be a non-empty token without whitespace or quotes"
        )
    return text.replace("%", "%%")


def render_service_unit(spec: ServiceSpec) -> str:
    """Render a systemd service unit."""
    if spec.scope not in {"user", "system"}:
        raise ValueError("scope must be 'user' or 'system'")

    env_file = spec.env_file if spec.env_file is not None else default_env_file()
    wanted_by = "default.target" if spec.scope == "user" else "multi-user.target"
    lines = [
        "[Unit]",
        f"Description={spec.description}",
        "After=network-online.target",
        "Wants=network-online.target",
        "",
        "[Service]",
        "Type=simple",
        f"WorkingDirectory={_systemd_unit_path(spec.working_dir)}",
        f"EnvironmentFile=-{_systemd_unit_path(env_file)}",
    ]
    lines.append("Environment=PYTHONUNBUFFERED=1")

    if spec.scope == "system":
        run_as = spec.run_as or getpass.getuser()
        lines.append(f"User={run_as}")

    if spec.app_runner == "module":
        app_command = "python -m qatlas.server"
    elif spec.app_runner == "uvicorn":
        host = _systemd_exec_arg(spec.host)
        port = _systemd_exec_arg(spec.port)
        app_command = f"uvicorn qatlas.server.main:app --host {host} --port {port}"
    else:
        raise ValueError("app_runner must be 'uvicorn' or 'module'")

    if spec.runner == "uv":
        exec_start = f"{_systemd_unit_path(spec.uv_executable)} run {app_command}"
    elif spec.runner == "python":
        if spec.app_runner == "uvicorn":
            host = _systemd_exec_arg(spec.host)
            port = _systemd_exec_arg(spec.port)
            app_command = f"-m uvicorn qatlas.server.main:app --host {host} --port {port}"
        else:
            app_command = "-m qatlas.server"
        exec_start = f"{_systemd_unit_path(spec.python_executable)} {app_command}"
    else:
        raise ValueError("runner must be 'uv' or 'python'")

    lines.extend(
        [
            f"ExecStart={exec_start}",
            "Restart=on-failure",
            "RestartSec=5",
            "KillSignal=SIGINT",
            "",
            "[Install]",
            f"WantedBy={wanted_by}",
            "",
        ]
    )
    return "\n".join(lines)


def _user_commands(unit_name: str, *, enable: bool, now: bool) -> List[List[str]]:
    commands: List[List[str]] = [["systemctl", "--user", "daemon-reload"]]
    if enable and now:
        commands.append(["systemctl", "--user", "enable", "--now", unit_name])
    elif enable:
        commands.append(["systemctl", "--user", "enable", unit_name])
    elif now:
        commands.append(["systemctl", "--user", "start", unit_name])
    return commands


def _system_sudo_commands(
    unit_name: str, staged_path: Path, *, enable: bool, now: bool
) -> List[List[str]]:
    commands: List[List[str]] = [
        [
            "sudo",
            "install",
            "-m",
            "0644",
            str(staged_path),
            f"/etc/systemd/system/{unit_name}",
        ],
        ["sudo", "systemctl", "daemon-reload"],
    ]
    if enable and now:
        commands.append(["sudo", "systemctl", "enable", "--now", unit_name])
    elif enable:
        commands.append(["sudo", "systemctl", "enable", unit_name])
    elif now:
        commands.append(["sudo", "systemctl", "start", unit_name])
    return commands


def install_user_service(
    spec: ServiceSpec,
    *,
    unit_path: Optional[Path] = None,
    enable: bool = False,
    now: bool = False,
    dry_run: bool = False,
    runner: Runner = subprocess.run,
) -> ServiceInstallResult:
    """Install a user-scope systemd unit and optionally enable/start it."""
    if spec.scope != "user":
        raise ValueError("install_user_service requires scope='user'")

    target = unit_path or default_user_service_path(spec.unit_name)
    unit_text = render_service_unit(spec)
    commands = _user_commands(spec.unit_name, enable=enable, now=now)
    if not dry_run:
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(unit_text, encoding="utf-8")
        for command in commands:
            runner(command, check=True)

    return ServiceInstallResult(
        unit_path=target,
        unit_text=unit_text,
        commands=commands,
        sudo_commands=[],
    )


def stage_system_service(
    spec: ServiceSpec,
    *,
    output_path: Optional[Path] = None,
    enable: bool = False,
    now: bool = False,
    dry_run: bool = False,
) -> ServiceInstallResult:
    """Stage a system-scope unit and return the sudo commands needed to install it."""
    if spec.scope != "system":
        raise ValueError("stage_system_service requires scope='system'")

    target = output_path or default_system_stage_path(spec.unit_name)
    unit_text = render_service_unit(spec)
    sudo_commands = _system_sudo_commands(spec.unit_name, target, enable=enable, now=now)
    if not dry_run:
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(unit_text, encoding="utf-8")

    return ServiceInstallResult(
        unit_path=target,
        unit_text=unit_text,
        commands=[],
        sudo_commands=sudo_commands,
    )


def _shell_join(command: Sequence[str]) -> str:
    return " ".join(_sh_quote(part) for part in command)


def _sh_quote(value: str) -> str:
    if not value:
        return "''"
    safe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-"
    if all(ch in safe for ch in value):
        return value
    return "'" + value.replace("'", "'\"'\"'") + "'"


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Install QuantumAtlas as a systemd service")
    sub = parser.add_subparsers(dest="command", required=True)

    install = sub.add_parser("install", help="install or stage a systemd service")
    install.add_argument("--scope", choices=["user", "system"], default="user")
    install.add_argument("--name", default="quantum-atlas")
    install.add_argument("--runner", choices=["auto", "uv", "python"], default="auto")
    install.add_argument("--app-runner", choices=["uvicorn", "module"], default="uvicorn")
    install.add_argument("--uv", dest="uv_executable", default=None)
    install.add_argument("--python", dest="python_executable", default=None)
    install.add_argument("--working-dir", default=str(get_project_root()))
    install.add_argument("--env-file", default=str(default_env_file()))
    install.add_argument("--host", default=None)
    install.add_argument("--port", type=int, default=None)
    install.add_argument("--run-as", default=None, help="system-scope Unix user")
    install.add_argument("--output", default=None, help="system-scope staging path")
    install.add_argument(
        "--enable",
        action=argparse.BooleanOptionalAction,
        default=None,
        help="enable the service (user default: off; system hint default: on)",
    )
    install.add_argument(
        "--now",
        action=argparse.BooleanOptionalAction,
        default=None,
        help="start after enable (user default: off; system hint default: on)",
    )
    install.add_argument(
        "--dry-run", action="store_true", help="print without writing or running commands"
    )
    return parser


def main(argv: Optional[Sequence[str]] = None) -> int:
    parser = _build_parser()
    args = parser.parse_args(argv)

    if args.command != "install":
        parser.error("unknown command")

    env_config = ServerConfig.from_env()
    working_dir = Path(args.working_dir).resolve()
    detected_runner, uv_executable, python_executable = detect_runner(
        working_dir,
        uv_arg=args.uv_executable,
        python_arg=args.python_executable,
    )
    runner = detected_runner if args.runner == "auto" else args.runner
    enable, now = _resolve_enable_now(args.scope, args.enable, args.now)
    spec = ServiceSpec(
        name=args.name,
        scope=args.scope,
        runner=runner,
        app_runner=args.app_runner,
        python_executable=python_executable,
        uv_executable=uv_executable,
        working_dir=working_dir,
        env_file=Path(args.env_file).resolve() if args.env_file else None,
        run_as=args.run_as,
        host=args.host or env_config.host,
        port=args.port or env_config.port,
    )

    if args.scope == "user":
        result = install_user_service(spec, enable=enable, now=now, dry_run=args.dry_run)
        print(f"Wrote user service: {result.unit_path}")
        if spec.app_runner == "uvicorn":
            print(
                f"Service bind is fixed in this unit as {spec.host}:{spec.port} "
                "from the current environment/CLI options. To change it, update .env "
                "or pass --host/--port, then regenerate the unit."
            )
        if args.dry_run:
            print(result.unit_text)
            if result.commands:
                print("Commands:")
                for command in result.commands:
                    print(_shell_join(command))
    else:
        output_path = Path(args.output).resolve() if args.output else None
        result = stage_system_service(
            spec,
            output_path=output_path,
            enable=enable,
            now=now,
            dry_run=args.dry_run,
        )
        print(f"Staged system service: {result.unit_path}")
        if spec.app_runner == "uvicorn":
            print(
                f"Service bind is fixed in this unit as {spec.host}:{spec.port} "
                "from the current environment/CLI options. To change it, update .env "
                "or pass --host/--port, then regenerate the unit."
            )
        if args.dry_run:
            print(result.unit_text)
        print("Run these commands to install it:")
        for command in result.sudo_commands:
            print(_shell_join(command))

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
