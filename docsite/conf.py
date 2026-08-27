# Sphinx configuration for the QuantumAtlas docs sites.
# Built with the Furo theme and deployed as static files: the public site
# goes to web/public/doc (served at /doc), the admin-only dev site goes to
# web/public/devdoc (served at /devdoc behind the admin ticket gate).
# Both are copied into web/dist by `npm run build` and embedded into the
# qatlasd binary.

project = "QuantumAtlas"
author = "QuantumAtlas Team"
copyright = "2026, QuantumAtlas Team"

extensions = []

templates_path = []
exclude_patterns = ["_build", "Thumbs.db", ".DS_Store"]

# Two build flavours share this config, selected by the `devdocs` tag
# (the `tags` object is available in conf.py):
#
#   public (default, no tag):
#       sphinx-build -b html docsite web/public/doc
#     The dev/ subtree (开发指南) is EXCLUDED — dev docs are admin-only
#     and ship in the second flavour.
#   dev docs (-t devdocs -D root_doc=dev/index):
#       sphinx-build -b html -t devdocs -D root_doc=dev/index \
#           docsite web/public/devdoc
#     Only the dev/ subtree, rooted at dev/index; the user guide and the
#     public landing page are excluded.
if tags.has("devdocs"):
    exclude_patterns += ["guide", "index.rst"]
else:
    exclude_patterns += ["dev"]

language = "zh_CN"

html_theme = "furo"
html_title = "QuantumAtlas 使用文档"
html_static_path = []

# /doc is served under a sub-path of the main site; relative URLs keep it
# location-agnostic.
html_use_index = True
