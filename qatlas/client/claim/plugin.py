"""The Claim-drafting WebUI as a qatlas client plugin (ADR 0005).

Registers ``qatlas contrib claim``. OFF by default (the contrib flow is moving
to the upstream lean side); enable with env ``QATLAS_CLAIM_PLUGIN=1`` or
``qatlas config set claim_plugin_enabled true``.
"""

from __future__ import annotations

import os

from qatlas.client.plugins.base import CommandSpec, QatlasPlugin


class ClaimPlugin(QatlasPlugin):
    name = "claim"

    def available(self) -> bool:
        return claim_plugin_enabled()

    def contrib_subcommands(self) -> dict[str, CommandSpec]:
        from qatlas.client.claim import cli as claim_cli

        return {
            "claim": CommandSpec(
                claim_cli.main,
                "Draft Claims in a localhost WebUI and file gitea issues "
                "(ADR 0005; needs the qatlas[contrib] extras)",
            )
        }


def claim_plugin_enabled() -> bool:
    env = os.getenv("QATLAS_CLAIM_PLUGIN")
    if env is not None:
        return env.strip().lower() in ("1", "true", "yes", "on")
    try:
        from qatlas.config import ServerConfig

        return bool(ServerConfig.from_env().claim_plugin_enabled)
    except Exception:  # noqa: BLE001
        return False
