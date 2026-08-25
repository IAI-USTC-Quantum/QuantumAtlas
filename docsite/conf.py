# Sphinx configuration for the QuantumAtlas public docs site.
# Built with the Furo theme and deployed as static files under /doc
# (web/public/doc is copied into web/dist by `npm run build` and embedded
# into the qatlasd binary).

project = "QuantumAtlas"
author = "QuantumAtlas Team"
copyright = "2026, QuantumAtlas Team"

extensions = []

templates_path = []
exclude_patterns = ["_build", "Thumbs.db", ".DS_Store"]

language = "zh_CN"

html_theme = "furo"
html_title = "QuantumAtlas 使用文档"
html_static_path = []

# /doc is served under a sub-path of the main site; relative URLs keep it
# location-agnostic.
html_use_index = True
