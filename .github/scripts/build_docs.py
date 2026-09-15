# /// script
# requires-python = ">=3.12"
# dependencies = [
#     "sphinx==8.2.3",
#     "furo==2025.12.19",
#     "myst-parser==5.1.0",
#     "jieba==0.42.1",
#     "sphinx-collections==0.3.2",
#     "sphinx-design==0.7.0",
#     "sphinxcontrib-mermaid==2.1.1",
# ]
# ///
"""Build both Sphinx sites into web/public. Git checkout is a separate step."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[2]


def sphinx(*args: str) -> None:
    subprocess.check_call([sys.executable, "-m", "sphinx", *args], cwd=ROOT)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release", action="store_true")
    args = parser.parse_args()
    if not os.environ.get("SOURCE_DATE_EPOCH"):
        raise SystemExit("set SOURCE_DATE_EPOCH to the source commit timestamp")
    os.environ["TZ"] = "UTC"
    os.environ["PYTHONHASHSEED"] = "0"
    os.chdir(ROOT)
    sys.path.insert(0, str(Path(__file__).resolve().parent))
    import check_docs
    import docs_sources

    docs_sources.validate()
    (ROOT / "build").mkdir(exist_ok=True)
    (ROOT / "web/public").mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix="docsite-run.", dir=ROOT / "build"))
    sphinx("-W", "--keep-going", "-n", "-b", "html", "-d", str(stage / "doctrees/doc"),
           "docsite", str(stage / "doc"))
    sphinx("-W", "--keep-going", "-n", "-b", "html", "-d", str(stage / "doctrees/devdoc"),
           "-t", "devdocs", "-D", "root_doc=dev/index",
           "-D", "html_title=QuantumAtlas 开发文档",
           "docsite", str(stage / "devdoc"))
    docs_sources.stamp(stage)
    (stage / "verification.json").write_text(
        json.dumps(check_docs.check(stage), ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    public = ROOT / "web/public"
    for name in ("doc", "devdoc"):
        dest = public / name
        if dest.exists() or dest.is_symlink():
            dest.rename(stage / f"previous-{name}")
        (stage / name).rename(dest)
    if args.release:
        print(json.dumps(check_docs.check(public, release=True), ensure_ascii=False, indent=2))
    print(f"Built and verified Sphinx/Furo sites; logs/provenance staging: {stage}")


if __name__ == "__main__":
    main()
