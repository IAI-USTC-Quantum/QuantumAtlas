#!/usr/bin/env bash
# Normal Sphinx CLI builds; Git checkout is a separate, explicit preparation step.
# Fresh output/doctrees per build, validate both sites before replacing UI inputs.
set -euo pipefail
: "${SOURCE_DATE_EPOCH:?set SOURCE_DATE_EPOCH to the source commit timestamp}"
export TZ=UTC PYTHONHASHSEED=0
PYTHON="${PYTHON:-python3}"
"$PYTHON" .github/scripts/docs_sources.py validate > /dev/null
mkdir -p build web/public
stage="$(mktemp -d build/docsite-run.XXXXXXXX)"
"$PYTHON" -m sphinx -W --keep-going -n -b html -d "$stage/doctrees/doc" \
  docsite "$stage/doc"
"$PYTHON" -m sphinx -W --keep-going -n -b html -d "$stage/doctrees/devdoc" \
  -t devdocs -D root_doc=dev/index -D html_title="QuantumAtlas 开发文档" \
  docsite "$stage/devdoc"
"$PYTHON" .github/scripts/docs_sources.py stamp "$stage"
"$PYTHON" .github/scripts/check_docs.py "$stage" > "$stage/verification.json"
# Keep previous build products in build/ instead of deleting arbitrary paths.
for name in doc devdoc; do
  if [ -e "web/public/$name" ] || [ -L "web/public/$name" ]; then
    mv "web/public/$name" "$stage/previous-$name"
  fi
  mv "$stage/$name" "web/public/$name"
done
printf 'Built and verified Sphinx/Furo sites; logs/provenance staging: %s\n' "$stage"
