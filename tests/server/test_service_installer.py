from atlas.server.service import (
    ServiceSpec,
    _resolve_enable_now,
    detect_runner,
    main,
    stage_system_service,
)


def test_cli_stages_system_service_with_uvicorn_uv_runner(tmp_path):
    output = tmp_path / "quantum-atlas.service"

    rc = main(
        [
            "install",
            "--scope",
            "system",
            "--uv",
            "/usr/bin/uv",
            "--working-dir",
            str(tmp_path),
            "--env-file",
            str(tmp_path / ".env"),
            "--run-as",
            "quantum",
            "--output",
            str(output),
            "--host",
            "0.0.0.0",
            "--port",
            "9000",
        ]
    )

    unit = output.read_text(encoding="utf-8")

    assert rc == 0
    assert "User=quantum" in unit
    assert f'WorkingDirectory={tmp_path}' in unit
    assert f'EnvironmentFile=-{tmp_path / ".env"}' in unit
    assert "EnvironmentFile=-" in unit
    assert "env.service.defaults" in unit
    assert "Environment=SERVER_HOST=0.0.0.0" in unit
    assert "Environment=SERVER_PORT=9000" in unit
    assert (
        "ExecStart=/usr/bin/uv run uvicorn atlas.server.main:app --host ${SERVER_HOST} --port ${SERVER_PORT}"
        in unit
    )
    assert "WantedBy=multi-user.target" in unit


def test_resolve_enable_now_system_defaults_both_on_when_unspecified():
    assert _resolve_enable_now("system", None, None) == (True, True)


def test_resolve_enable_now_system_respects_explicit_opt_out():
    assert _resolve_enable_now("system", False, False) == (False, False)


def test_resolve_enable_now_user_defaults_both_off_when_unspecified():
    assert _resolve_enable_now("user", None, None) == (False, False)


def test_system_cli_prints_enable_now_by_default(tmp_path, capsys):
    output = tmp_path / "quantum-atlas.service"
    rc = main(
        [
            "install",
            "--scope",
            "system",
            "--runner",
            "python",
            "--working-dir",
            str(tmp_path),
            "--env-file",
            str(tmp_path / ".env"),
            "--output",
            str(output),
        ]
    )
    assert rc == 0
    stdout = capsys.readouterr().out
    assert "sudo systemctl enable --now quantum-atlas.service" in stdout


def test_detect_runner_falls_back_to_local_venv_python_when_uv_is_missing(tmp_path, monkeypatch):
    python_path = tmp_path / ".venv" / "bin" / "python"
    python_path.parent.mkdir(parents=True)
    python_path.write_text("#!/bin/sh\n", encoding="utf-8")
    monkeypatch.setattr("atlas.server.service.shutil.which", lambda name: None)

    runner, uv_path, detected_python = detect_runner(tmp_path)

    assert runner == "python"
    assert str(uv_path) == "uv"
    assert detected_python == python_path


def test_system_service_returns_install_commands(tmp_path):
    output = tmp_path / "quantum-atlas.service"

    result = stage_system_service(
        ServiceSpec(
            scope="system",
            run_as="quantum",
            working_dir=tmp_path,
            env_file=tmp_path / ".env",
            uv_executable="/usr/bin/uv",
            host="0.0.0.0",
            port=9000,
        ),
        output_path=output,
        enable=True,
        now=True,
    )

    assert output.is_file()
    assert result.sudo_commands == [
        [
            "sudo",
            "install",
            "-m",
            "0644",
            str(output),
            "/etc/systemd/system/quantum-atlas.service",
        ],
        ["sudo", "systemctl", "daemon-reload"],
        ["sudo", "systemctl", "enable", "--now", "quantum-atlas.service"],
    ]
