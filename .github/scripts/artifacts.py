"""CI-only UI handoff helpers. Release validation/publishing belongs to GoReleaser."""

import argparse
import hashlib
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import tempfile
import zipfile


REQUIRED_UI = ("index.html", "doc/index.html", "devdoc/dev/index.html")
# Match web/bundle.go: a CI-accepted zip must be usable by go install clients.
MAX_BUNDLE_SIZE = 64 << 20
MAX_EXPANDED_SIZE = 256 << 20
MAX_FILE_SIZE = 16 << 20
MAX_BUNDLE_FILES = 20000


def emit(key, value):
    print(f"{key}={value}")
    if output := os.environ.get("GITHUB_OUTPUT"):
        with open(output, "a") as stream:
            stream.write(f"{key}={value}\n")


def ui_version(release_tag, source_sha):
    """Use GoReleaser's version spelling, or a SHA-only unpublished CI fixture."""
    if not re.fullmatch(r"(?:[0-9a-f]{40}|[0-9a-f]{64})", source_sha):
        raise ValueError("source SHA must be a full lowercase 40/64-digit Git commit ID")
    # Deliberately no parallel SemVer parser or release policy here. GoReleaser
    # strips one leading v; the UI handoff must retain that exact version text.
    version = release_tag.removeprefix("v") if release_tag else f"0.0.0-ci.g{source_sha}"

    def resolve(ref):
        return subprocess.check_output(
            ["git", "rev-parse", "--verify", ref], text=True, timeout=30,
        ).strip()

    if resolve("HEAD") != source_sha:
        raise ValueError("checkout HEAD does not match source SHA")
    if release_tag:
        # Require a literal ref, not a revision expression such as tag^{}.
        subprocess.run(["git", "check-ref-format", f"refs/tags/{release_tag}"], check=True, timeout=30)
        if resolve(f"refs/tags/{release_tag}^{{commit}}") != source_sha:
            raise ValueError("release tag commit does not match checkout HEAD/source SHA")
    return version


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def safe_name(name):
    parts = PurePosixPath(name).parts
    if not parts or name.startswith("/") or any(char in name for char in "\\:\x00\r\n") or any(
        part in ("", ".", "..") for part in name.split("/")
    ):
        raise ValueError(f"unsafe artifact path: {name!r}")
    return name


def tree(root):
    root = Path(root)
    if not root.is_dir() or root.is_symlink():
        raise ValueError(f"missing or symlink UI directory: {root}")
    result = {}
    for path in sorted(root.rglob("*")):
        name = safe_name(path.relative_to(root).as_posix())
        if path.is_symlink():
            raise ValueError(f"symlink in UI: {name}")
        if path.is_dir():
            result[name] = "directory"
        elif path.is_file():
            result[name] = digest(path)
        else:
            raise ValueError(f"special file in UI: {name}")
    for name in REQUIRED_UI:
        if not (root / name).is_file() or not (root / name).stat().st_size:
            raise ValueError(f"incomplete UI: missing {name}")
    if not any(path.is_file() and path.stat().st_size > 0 for path in root.glob("assets/*.js")):
        raise ValueError("incomplete UI: missing assets/*.js")
    return result


def compare(first, second):
    a, b = tree(first), tree(second)
    changed = sorted(name for name in a.keys() | b.keys() if a.get(name) != b.get(name))
    if changed:
        raise ValueError("non-reproducible UI (added/deleted/changed): " + ", ".join(changed))
    print(f"Identical complete UI trees: {len(a)} entries (including hidden files)")


def unique_ui_archive(archive, version):
    archive = Path(archive)
    expected = f"qatlasd_{version}_web.zip"
    matches = list(archive.parent.glob("qatlasd_*_web.zip"))
    if archive.name != expected or matches != [archive]:
        raise ValueError(f"require exactly one UI zip for the selected version: {expected}")


def restore_ui(archive, destination, version):
    archive = Path(archive)
    if archive.is_symlink() or not archive.is_file() or archive.stat().st_size > MAX_BUNDLE_SIZE:
        raise ValueError("UI zip is not a regular file within the 64 MiB compressed size limit")
    destination = Path(destination)
    if destination.exists() or destination.is_symlink():
        raise ValueError(f"UI destination must not exist: {destination}")
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=destination.parent) as temporary:
        stage = Path(temporary) / "dist"
        stage.mkdir()
        with zipfile.ZipFile(archive) as bundle:
            if bundle.comment != f"qatlas-ui-v1:{version}".encode():
                raise ValueError("UI zip version/comment mismatch")
            members = bundle.infolist()
            if len(members) > MAX_BUNDLE_FILES:
                raise ValueError("UI zip exceeds 20000 files")
            seen = set()
            total = 0
            for member in members:
                name = safe_name(member.filename)
                mode = member.external_attr >> 16
                kind = stat.S_IFMT(mode)
                if name in seen or kind not in (0, stat.S_IFREG) or member.is_dir():
                    raise ValueError(f"duplicate or non-regular UI member: {name}")
                if member.flag_bits & 1:
                    raise ValueError("encrypted UI member")
                seen.add(name)
                total += member.file_size
                if member.file_size > MAX_FILE_SIZE or total > MAX_EXPANDED_SIZE:
                    raise ValueError("UI zip exceeds 16 MiB per file / 256 MiB expanded size")
                target = stage / name
                target.parent.mkdir(parents=True, exist_ok=True)
                with bundle.open(member) as source, target.open("xb") as output:
                    data = source.read(MAX_FILE_SIZE + 1)
                    if len(data) > MAX_FILE_SIZE or len(data) != member.file_size:
                        raise ValueError("UI member size mismatch")
                    output.write(data)
                target.chmod(0o644)
        tree(stage)
        stage.rename(destination)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("ui-version", help="select UI version from RELEASE_TAG and SOURCE_SHA environment")
    diff = sub.add_parser("compare")
    diff.add_argument("first")
    diff.add_argument("second")
    restore = sub.add_parser("restore-ui")
    restore.add_argument("archive")
    restore.add_argument("destination")
    restore.add_argument("version")
    args = parser.parse_args()
    if args.command == "ui-version":
        emit("version", ui_version(os.environ.get("RELEASE_TAG", ""), os.environ["SOURCE_SHA"]))
    elif args.command == "compare":
        compare(args.first, args.second)
    else:
        unique_ui_archive(args.archive, args.version)
        restore_ui(args.archive, args.destination, args.version)


if __name__ == "__main__":
    main()
