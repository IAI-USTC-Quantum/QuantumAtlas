#!/usr/bin/env python3
"""Validate the final quantum-atlas wheel/sdist without installing or executing them."""

from __future__ import annotations

import sys
import tarfile
import tomllib
import zipfile
from email.parser import BytesParser
from pathlib import Path, PurePosixPath

FINAL_VERSION = "0.21.0"
DIST_INFO = f"quantum_atlas-{FINAL_VERSION}.dist-info"
SDIST_ROOT = f"quantum_atlas-{FINAL_VERSION}"


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def check_metadata(raw: bytes) -> None:
    metadata = BytesParser().parsebytes(raw)
    require(metadata["Name"] == "quantum-atlas", "unexpected distribution name")
    require(metadata["Version"] == FINAL_VERSION, "not the final migration version")
    require(not metadata.get_all("Requires-Dist"), "final distribution must have no dependencies")
    require(not metadata.get_all("Provides-Extra"), "dev tooling must not be published as extras")
    require(
        "Development Status :: 7 - Inactive" in metadata.get_all("Classifier", []),
        "missing Inactive classifier",
    )
    require(metadata["Description-Content-Type"] == "text/markdown", "README must be Markdown")
    description = metadata.get_payload()
    for text in (
        "final migration release",
        "no automatic installation",
        "uv tool uninstall quantum-atlas",
    ):
        require(text in description, f"missing migration notice: {text}")


def check_distributions(directory: Path) -> None:
    wheels = sorted(directory.glob("*.whl"))
    sdists = sorted(directory.glob("*.tar.gz"))
    require(len(wheels) == len(sdists) == 1, "expected exactly one wheel and one sdist")
    require(
        wheels[0].name == f"quantum_atlas-{FINAL_VERSION}-py3-none-any.whl", "unexpected wheel name"
    )
    require(sdists[0].name == f"{SDIST_ROOT}.tar.gz", "unexpected sdist name")

    with zipfile.ZipFile(wheels[0]) as archive:
        names = archive.namelist()
        allowed = {
            f"{DIST_INFO}/METADATA",
            f"{DIST_INFO}/WHEEL",
            f"{DIST_INFO}/RECORD",
            f"{DIST_INFO}/licenses/LICENSE",
        }
        require(
            set(names) == allowed and len(names) == len(allowed),
            f"wheel must contain only distribution metadata/license, got {names}",
        )
        check_metadata(archive.read(f"{DIST_INFO}/METADATA"))

    with tarfile.open(sdists[0], "r:gz") as archive:
        members = archive.getmembers()
        require(
            all(member.isfile() or member.isdir() for member in members),
            "sdist contains links or special files",
        )
        files = [member.name for member in members if member.isfile()]
        expected = {
            f"{SDIST_ROOT}/{name}"
            for name in ("pyproject.toml", "PYPI_README.md", "LICENSE", "PKG-INFO")
        }
        # Hatchling always includes a checkout's VCS ignore file so source
        # rebuilds preserve file selection. It is build metadata, not code.
        allowed = expected | {f"{SDIST_ROOT}/.gitignore"}
        require(
            expected <= set(files) <= allowed and len(files) == len(set(files)),
            f"sdist contains unexpected or missing files: {files}",
        )
        for member in members:
            path = PurePosixPath(member.name)
            require(
                not path.is_absolute() and ".." not in path.parts and path.parts[0] == SDIST_ROOT,
                "unsafe sdist path",
            )
        with archive.extractfile(f"{SDIST_ROOT}/PKG-INFO") as stream:
            check_metadata(stream.read())
        with archive.extractfile(f"{SDIST_ROOT}/pyproject.toml") as stream:
            project = tomllib.loads(stream.read().decode())["project"]
        require(
            project["name"] == "quantum-atlas" and project["version"] == FINAL_VERSION,
            "sdist project mismatch",
        )
        require(project.get("dependencies") == [], "sdist must have no dependencies")
        for key in ("scripts", "gui-scripts", "entry-points", "optional-dependencies"):
            require(not project.get(key), f"sdist must not define {key}")

    print(
        f"Validated {wheels[0].name} and {sdists[0].name}: metadata-only, no commands or dependencies"
    )


if __name__ == "__main__":
    check_distributions(Path(sys.argv[1] if len(sys.argv) > 1 else "dist"))
