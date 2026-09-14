"""CI-only artifact checks. No build framework and no network/production access."""

import argparse
import hashlib
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess
import tarfile
import tempfile
import zipfile

from release_gate import emit, validate_version


PLATFORMS = ("linux_amd64", "linux_arm64", "darwin_arm64")
REQUIRED_UI = ("index.html", "doc/index.html", "devdoc/dev/index.html")
# Match web/bundle.go: a CI-accepted zip must be usable by go install clients.
MAX_BUNDLE_SIZE = 64 << 20
MAX_EXPANDED_SIZE = 256 << 20
MAX_FILE_SIZE = 16 << 20
MAX_BUNDLE_FILES = 20000


def ui_version(release_tag, source_sha):
    """Exact release tag, or a SHA-only identifier for unpublished CI fixtures."""
    if not re.fullmatch(r"(?:[0-9a-f]{40}|[0-9a-f]{64})", source_sha):
        raise ValueError("source SHA must be a full lowercase 40/64-digit Git commit ID")
    version = validate_version(release_tag)[0] if release_tag else f"0.0.0-ci.g{source_sha}"
    # No shell interpolation or tag discovery: branches/PRs need no existing
    # tags, and a release must use the caller's exact tag even with many at HEAD.
    def resolve(ref):
        return subprocess.check_output(
            ["git", "rev-parse", "--verify", ref], text=True, timeout=30,
        ).strip()

    if resolve("HEAD") != source_sha:
        raise ValueError("checkout HEAD does not match source SHA")
    if release_tag and resolve(f"refs/tags/{release_tag}^{{commit}}") != source_sha:
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


def checksums(directory):
    manifests = list(Path(directory).glob("*_checksums.txt"))
    if len(manifests) != 1:
        raise ValueError(f"expected exactly one default *_checksums.txt, got {len(manifests)}")
    records = {}
    for line in manifests[0].read_text().splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9_.+-]+)", line)
        if not match or match[2] in records:
            raise ValueError("invalid or duplicate checksum record")
        records[match[2]] = match[1]
    if not records:
        raise ValueError("empty checksums")
    return manifests[0], records


def verify_file(path, records):
    path = Path(path)
    if path.name not in records or digest(path) != records[path.name]:
        raise ValueError(f"missing or mismatched checksum: {path.name}")


def verify_release(directory, ui_directory, version):
    manifest, records = checksums(directory)
    expected = {f"qatlasd_{version}_{platform}.tar.gz" for platform in PLATFORMS}
    ui_name = f"qatlasd_{version}_web.zip"
    expected.add(ui_name)
    if set(records) != expected:
        raise ValueError(f"unexpected release asset set: {sorted(records)}; want {sorted(expected)}")
    for name in sorted(expected):
        base = ui_directory if name == ui_name else directory
        verify_file(Path(base) / name, records)
    return manifest


def smoke(directory, version, platform):
    _, records = checksums(directory)
    archive = Path(directory) / f"qatlasd_{version}_{platform}.tar.gz"
    verify_file(archive, records)
    with tempfile.TemporaryDirectory() as temporary, tarfile.open(archive, "r:gz") as bundle:
        seen, binary = set(), None
        for member in bundle:
            name = safe_name(member.name)
            if name in seen or not (member.isfile() or member.isdir()):
                raise ValueError(f"duplicate or non-regular archive member: {name}")
            seen.add(name)
            if name == "qatlasd":
                if not member.isfile() or member.size > 512 * 1024 * 1024:
                    raise ValueError("invalid qatlasd archive member")
                binary = Path(temporary) / "qatlasd"
                with bundle.extractfile(member) as source, binary.open("xb") as target:
                    shutil.copyfileobj(source, target)
                binary.chmod(0o755)
        if binary is None:
            raise ValueError("archive missing root-level qatlasd binary")
        # No config/DB/server invocation. A HOME with no user settings also
        # catches accidental initialization before the --version fast path.
        # Draft download needs a privileged token, but the executable must
        # inherit neither it nor runner configuration/production credentials.
        env = {"PATH": os.defpath, "HOME": temporary, "XDG_CONFIG_HOME": temporary, "XDG_CACHE_HOME": temporary}
        actual = subprocess.check_output([str(binary), "--version"], env=env, cwd=temporary, text=True, timeout=30).strip()
        expected = f"qatlasd version {version}"
        if actual != expected:
            raise ValueError(f"binary version {actual!r}, expected {expected!r}")
        print(actual)


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
    verify = sub.add_parser("verify-release")
    verify.add_argument("directory")
    verify.add_argument("ui_directory")
    verify.add_argument("version")
    native = sub.add_parser("smoke")
    native.add_argument("directory")
    native.add_argument("version")
    native.add_argument("platform", choices=PLATFORMS)
    args = parser.parse_args()
    if args.command == "ui-version":
        emit("version", ui_version(os.environ.get("RELEASE_TAG", ""), os.environ["SOURCE_SHA"]))
    elif args.command == "compare":
        compare(args.first, args.second)
    elif args.command == "restore-ui":
        unique_ui_archive(args.archive, args.version)
        restore_ui(args.archive, args.destination, args.version)
    elif args.command == "verify-release":
        print(verify_release(args.directory, args.ui_directory, args.version))
    else:
        smoke(args.directory, args.version, args.platform)


if __name__ == "__main__":
    main()
