#!/usr/bin/env bash
# CI-only clean Sphinx build. npm remains an explicit working-directory: web
# workflow step. Keep doctree/pickle caches OUT of the shipped UI tree.
set -euo pipefail
: "${SOURCE_DATE_EPOCH:?set SOURCE_DATE_EPOCH to the source commit timestamp}"
export TZ=UTC PYTHONHASHSEED=0
rm -rf web/public/doc web/public/devdoc web/dist build/doctrees web/node_modules/.tmp
sphinx-build -W --keep-going -b html -d build/doctrees/public docsite web/public/doc
sphinx-build -W --keep-going -b html -d build/doctrees/dev -t devdocs -D root_doc=dev/index \
  -D html_title="QuantumAtlas 开发文档" docsite web/public/devdoc
