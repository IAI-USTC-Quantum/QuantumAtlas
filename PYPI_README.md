# quantum-atlas — retired; install qatlas-cli instead

**`quantum-atlas 0.21.0` is the final migration release of this PyPI package.**
The maintained `qatlas` CLI is now published as
[**qatlas-cli**](https://pypi.org/project/qatlas-cli/) from its
[own repository](https://github.com/IAI-USTC-Quantum/qatlas-cli).

This final distribution contains **metadata and this notice only**:

- No `qatlas` command or other executable entry point.
- No `qatlas` Python package, legacy parser helpers, or compatibility layer.
- No runtime dependencies and **no automatic installation of `qatlas-cli`**.
- No changes to your CLI configuration, credentials, or server data.

The [QuantumAtlas server](https://github.com/IAI-USTC-Quantum/QuantumAtlas)
(`qatlasd`) remains maintained. Its releases are independent of this retired
PyPI package. This is a package retirement, not a shutdown of QuantumAtlas.

## Migrate explicitly

Use **the same installer/environment that installed the old package**. Uninstall
`quantum-atlas` **before** installing `qatlas-cli`; older releases owned the same
`qatlas` command and Python paths. Do not run all three alternatives below.
If the old package is already absent, skip its uninstall command.

### uv tools (recommended for the CLI)

```bash
uv tool uninstall quantum-atlas
uv tool install qatlas-cli
qatlas --version
qatlas --help
```

### pipx

```bash
pipx uninstall quantum-atlas
pipx install qatlas-cli
qatlas --help
```

### pip in your existing Python environment

```bash
python -m pip uninstall quantum-atlas
python -m pip install qatlas-cli
qatlas --help
```

If both packages were already installed, uninstall the old package first and
**reinstall `qatlas-cli` afterward** to restore any shared files or command that
uninstalling the old distribution may have removed. Depending on your installer,
use `uv tool install --reinstall qatlas-cli`, `pipx reinstall qatlas-cli`, or
`python -m pip install --force-reinstall qatlas-cli`.

Keep your existing CLI configuration (for example,
`~/.config/qatlas/config.yaml` on Linux); migration does not require deleting it.
For current commands, configuration, and compatibility information, see the
[qatlas-cli documentation](https://github.com/IAI-USTC-Quantum/qatlas-cli#readme).

## What happens to old installations?

Publishing this release does **not** modify an already-installed old version.
Historical releases remain available for explicitly pinned legacy environments;
they do not receive new maintenance through this package name.

Upgrading to `quantum-atlas 0.21.0` removes the old distribution's code and entry
points; it does **not** give you the replacement CLI. `uv tool install quantum-atlas`
/ `pipx install quantum-atlas` are no longer supported installation commands:
the final package provides no executable. Install `qatlas-cli` explicitly instead.

The `Development Status :: 7 - Inactive` classifier describes this final release;
it is not a claim that PyPI has applied a project-wide deprecated or archived status.
