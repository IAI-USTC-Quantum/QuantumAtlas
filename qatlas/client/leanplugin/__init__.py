"""The ``lean`` plugin — wires a local ``qatlas-lean`` checkout into the qatlas
client CLI as ``qatlas lean <subcommand>`` (ADR 0002/0005 transitional).

The qatlas-lean repo is NOT modified: this adapter drives its existing
``./qatlas-lean`` launcher as a subprocess (passthrough). The plugin is
available only when a checkout is configured (env ``QATLAS_LEAN_DIR`` or
``qatlas config set lean_dir <path>``) and contains the launcher.
"""

from __future__ import annotations
