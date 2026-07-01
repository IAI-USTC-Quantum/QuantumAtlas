"""Client-side plugin system for the ``qatlas`` CLI.

A plugin contributes commands to the CLI without the core having to hard-code
them. Two contribution surfaces:

* **top-level commands** — ``qatlas <name> ...`` (e.g. the ``lean`` plugin
  exposes ``qatlas lean <subcommand>``);
* **contrib subcommands** — ``qatlas contrib <name> ...`` (a plugin may add a
  subcommand under the existing ``qatlas contrib`` group, e.g. the ``claim``
  builtin when enabled via the config ``plugins`` list).

Plugins are discovered from a built-in list plus ``qatlas.plugins`` Python
entry points; each declares whether it is ``available()`` in the current
environment (config / a sibling checkout present / a feature flag), so the CLI
surface adapts without code changes.
"""

from __future__ import annotations
