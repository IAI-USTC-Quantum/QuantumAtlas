"""Plugin contract for the qatlas client CLI."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Callable


@dataclass(frozen=True)
class CommandSpec:
    """One CLI command a plugin contributes.

    ``handler`` receives the remaining argv (everything after the command
    name) and returns a process exit code.
    """

    handler: Callable[[list[str]], int]
    summary: str


class QatlasPlugin:
    """Base class for a qatlas client plugin.

    Subclasses set ``name`` and override the contribution hooks they use.
    ``available`` gates whether the plugin's commands appear in the CLI at all
    (e.g. the lean plugin is only available when a checkout is configured).
    """

    name: str = ""

    def available(self) -> bool:
        return True

    def top_level_commands(self) -> dict[str, CommandSpec]:
        """Commands mounted as ``qatlas <name>``."""
        return {}

    def contrib_subcommands(self) -> dict[str, CommandSpec]:
        """Subcommands mounted as ``qatlas contrib <name>``."""
        return {}
