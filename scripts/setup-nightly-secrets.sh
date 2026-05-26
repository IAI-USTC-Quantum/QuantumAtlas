#!/usr/bin/env bash
# Set the GitHub Actions secrets used by the nightly production smoke workflow.
#
# Usage (with TIMIDLY_GH_TOKEN already exported by your shell/direnv):
#   ./scripts/setup-nightly-secrets.sh
#
# Or, one-shot:
#   TIMIDLY_GH_TOKEN=ghp_xxx ./scripts/setup-nightly-secrets.sh
#
# Optional overrides:
#   REPO=owner/name              (default: IAI-USTC-Quantum/QuantumAtlas)
#   EXPECTED_USER=TIMidlY        (default: TIMidlY, case-insensitive)
#
# What it does:
#   1. Verifies that $TIMIDLY_GH_TOKEN really belongs to EXPECTED_USER
#      (refuses to touch the repo otherwise).
#   2. Writes the QATLAS_SERVER_TARGETS secret (two lines: quantum-atlas.ai
#      and the 47.102.36.175 host with |insecure).
#   3. Leaves MINERU_API_TOKEN alone (it is already configured).

set -euo pipefail

REPO="${REPO:-IAI-USTC-Quantum/QuantumAtlas}"
EXPECTED_USER="${EXPECTED_USER:-TIMidlY}"

if [[ -z "${TIMIDLY_GH_TOKEN:-}" ]]; then
    echo "ERROR: TIMIDLY_GH_TOKEN is not set in the environment." >&2
    exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
    echo "ERROR: gh (GitHub CLI) is not installed or not on PATH." >&2
    exit 1
fi

export GH_TOKEN="$TIMIDLY_GH_TOKEN"

echo "[1/3] Verifying gh auth identity..."
actual_user="$(gh api user --jq .login)"
echo "      logged in as: $actual_user"

shopt -s nocasematch
if [[ "$actual_user" != "$EXPECTED_USER" ]]; then
    shopt -u nocasematch
    echo "ERROR: expected user '$EXPECTED_USER' but token belongs to '$actual_user'." >&2
    echo "       Refusing to modify $REPO secrets." >&2
    exit 1
fi
shopt -u nocasematch
echo "      identity OK, proceeding to set secrets on $REPO."

echo "[2/3] Setting QATLAS_SERVER_TARGETS (two production hosts)..."
TARGETS=$'https://quantum-atlas.ai\nhttps://47.102.36.175|insecure'
printf '%s' "$TARGETS" | gh secret set QATLAS_SERVER_TARGETS \
    --repo "$REPO" \
    --body -
echo "      QATLAS_SERVER_TARGETS set."

echo "[3/3] Listing nightly-related secrets currently on $REPO:"
gh secret list --repo "$REPO" | grep -E '^(QATLAS_SERVER_TARGETS|MINERU_API_TOKEN)' || true

echo
echo "Done. You can manually trigger the workflow with:"
echo "  gh workflow run 'Nightly production smoke' --repo $REPO"
