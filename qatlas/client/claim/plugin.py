"""The Claim-drafting WebUI as a qatlas client plugin.

Ships with the package as a first-party (builtin) plugin, **OFF by default**: it
contributes ``qatlas contrib claim`` only when ``claim`` is in the config-driven
enabled list (config.yaml ``plugins: [..., claim]`` — see the registry). ADR 0008
moved the maintained claim-authoring flow to the upstream qatlas-lean repo
(``qatlas-lean contrib claim``); this plugin remains as an optional local fallback /
reference implementation.
"""

from __future__ import annotations

from qatlas.client.plugins.base import CommandSpec, QatlasPlugin


class ClaimPlugin(QatlasPlugin):
    name = "claim"

    def available(self) -> bool:
        # Enablement is config-driven (the `plugins` list decides whether this plugin
        # is instantiated at all); it has no hard environment prerequisite, so once
        # enabled it is available. The command surfaces a clear hint if the optional
        # qatlas[contrib] deps (FastAPI/uvicorn) are missing.
        return True

    def contrib_subcommands(self) -> dict[str, CommandSpec]:
        from qatlas.client.claim import cli as claim_cli

        return {
            "claim": CommandSpec(
                claim_cli.main,
                "Draft Claims in a localhost WebUI and file gitea issues "
                "(off by default; enable via config `plugins: [claim]`; needs qatlas[contrib])",
            )
        }
