"""QuantumAtlas's normal Sphinx/Furo build, with pinned component collections.

User site: sphinx-build -W --keep-going -n -b html docsite build/doc
Admin site: add -t devdocs -D root_doc=dev/index, output build/devdoc.
Prepare component checkouts with .github/scripts/docs_sources.py first.
"""
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / ".github/scripts"))
from docs_sources import IGNORE, component_root, validate

project = "QuantumAtlas"
author = "QuantumAtlas Team"
copyright = "2026, QuantumAtlas Team"
language = "zh_CN"
html_theme = "furo"
html_title = "QuantumAtlas 使用文档"
html_static_path = []
html_use_index = True
extensions = [
    "myst_parser",
    "sphinx_collections",
    "sphinx_design",
    "sphinxcontrib.mermaid",
]
myst_heading_anchors = 6
myst_enable_extensions = ["dollarmath"]
# Mermaid uses the standard extension's rendering and browser dependency defaults.
exclude_patterns = ["_build", "Thumbs.db", ".DS_Store"]

# Preserve the existing user/admin split, not separate public/private components.
# All three selected components are part of the normal user-site aggregation.
if tags.has("devdocs"):
    exclude_patterns += ["guide", "manual", "index.rst", "_collections"]
    collections = {}
else:
    exclude_patterns += ["dev"]
    components = validate()
    collections = {
        item["name"]: {
            "driver": "copy_folder",
            "source": str(component_root() / item["name"] / item["docs_dir"]),
            "ignore": sorted(IGNORE),
        }
        for item in components
    }
# collections_target/clean/final_clean deliberately retain official defaults.
# Only docsite/_collections/<component> is generated/cleaned, never originals.
