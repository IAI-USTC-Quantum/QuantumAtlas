"""Keep the retired PyPI name from reclaiming the maintained CLI's namespace."""

from __future__ import annotations

import importlib.util
import shutil
import subprocess
import sys
import tarfile
import tomllib
import zipfile
from pathlib import Path

import pytest
import yaml

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location(
    "check_legacy_dist", ROOT / "scripts/check_legacy_dist.py"
)
CHECKER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECKER)
FINAL_TAG = "refs/tags/quantum-atlas-v0.21.0"
FINAL_GATE = f"github.ref == '{FINAL_TAG}'"


def test_package_metadata_retires_the_namespace():
    data = tomllib.loads((ROOT / "pyproject.toml").read_text())
    project = data["project"]
    assert project["version"] == CHECKER.FINAL_VERSION
    assert project["dependencies"] == []
    assert project["readme"] == "PYPI_README.md"
    assert "Development Status :: 7 - Inactive" in project["classifiers"]
    for field in ("scripts", "gui-scripts", "entry-points", "optional-dependencies"):
        assert not project.get(field)
    assert not list((ROOT / "qatlas").rglob("*.py"))
    assert "commitizen" not in data["tool"]
    assert "dev" in data["dependency-groups"]


@pytest.fixture(scope="session")
def distributions(tmp_path_factory):
    directory = tmp_path_factory.mktemp("legacy-dists")
    subprocess.run(
        [sys.executable, "-m", "build", "--no-isolation", "--outdir", str(directory)],
        cwd=ROOT,
        check=True,
    )
    return directory


def test_wheel_and_sdist_are_metadata_only(distributions):
    CHECKER.check_distributions(distributions)


def test_sdist_can_rebuild_without_the_repository(distributions, tmp_path):
    with tarfile.open(next(distributions.glob("*.tar.gz")), "r:gz") as archive:
        archive.extractall(tmp_path, filter="data")
    source = tmp_path / CHECKER.SDIST_ROOT
    output = tmp_path / "rebuilt"
    subprocess.run(
        [sys.executable, "-m", "build", "--wheel", "--no-isolation", "--outdir", str(output)],
        cwd=source,
        check=True,
    )
    shutil.copy2(next(distributions.glob("*.tar.gz")), output)
    CHECKER.check_distributions(output)


@pytest.mark.parametrize("member", ["qatlas/__init__.py", f"{CHECKER.DIST_INFO}/entry_points.txt"])
def test_artifact_guard_rejects_namespace_or_entry_points(distributions, tmp_path, member):
    for artifact in distributions.iterdir():
        shutil.copy2(artifact, tmp_path)
    with zipfile.ZipFile(next(tmp_path.glob("*.whl")), "a") as archive:
        archive.writestr(member, "[console_scripts]\nqatlas = qatlas.cli:main\n")
    with pytest.raises(ValueError, match="only distribution metadata"):
        CHECKER.check_distributions(tmp_path)


def test_artifact_guard_rejects_redirect_dependency(distributions):
    with zipfile.ZipFile(next(distributions.glob("*.whl"))) as archive:
        raw = archive.read(f"{CHECKER.DIST_INFO}/METADATA")
    raw = b"Requires-Dist: qatlas-cli\n" + raw
    with pytest.raises(ValueError, match="no dependencies"):
        CHECKER.check_metadata(raw)


def test_legacy_and_server_releases_are_separate():
    workflow = yaml.safe_load((ROOT / ".github/workflows/release.yml").read_text())
    # PyYAML's YAML 1.1 loader reads the Actions key 'on' as boolean True.
    triggers = workflow.get("on", workflow.get(True))
    assert triggers["push"]["tags"] == ["v*.*.*", "quantum-atlas-v0.21.0"]
    jobs = workflow["jobs"]
    assert jobs["prep"]["if"] == "startsWith(github.ref, 'refs/tags/v')"
    for name in ("python-build", "pypi-publish", "legacy-python-release"):
        assert jobs[name]["if"] == FINAL_GATE
        assert "prep" not in jobs[name].get("needs", [])
    assert jobs["pypi-publish"]["needs"] == "python-build"
    assert jobs["pypi-publish"]["environment"]["name"] == "pypi"
    assert jobs["legacy-python-release"]["needs"] == ["python-build", "pypi-publish"]
    publish = jobs["legacy-python-release"]["steps"][-1]["with"]
    assert publish["make_latest"] is False
    assert publish["prerelease"] is False
    assert "python-build" not in jobs["create-release"]["needs"]
    for name in ("docs-build", "binary-build", "create-release", "docker-image"):
        assert "prep" in jobs[name]["needs"]
        for step in jobs[name]["steps"]:
            serialized = yaml.safe_dump(step)
            assert "release-dists-python" not in serialized
            assert "*.whl" not in serialized
            assert "*.tar.gz" not in serialized


def test_build_validation_precedes_pypi():
    workflow = yaml.safe_load((ROOT / ".github/workflows/release.yml").read_text())
    steps = workflow["jobs"]["python-build"]["steps"]
    commands = "\n".join(step.get("run", "") for step in steps)
    assert "uv sync --locked" in commands
    assert "python -m pytest" in commands
    assert "scripts/check_legacy_dist.py dist" in commands
    assert "twine check --strict" in commands
