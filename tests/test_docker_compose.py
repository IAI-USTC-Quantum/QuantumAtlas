"""Sanity tests for the docker-compose templates in ``deploy/``.

We can't actually run docker locally on every developer machine, so
these tests are deliberately structural — they catch the regressions
that hurt most when nobody notices for a few weeks:

* Missing service name (e.g. someone deletes the qatlasd block).
* Image tag drift between compose and the rest of the release pipeline.
* Required env var pulled out without updating the compose `:?` guards.
* Bind-mount path renamed without updating Dockerfile VOLUME or
  install docs.

For real "does the image actually start" coverage we rely on the
ghcr.io smoke job in .github/workflows/release.yml + manual `docker
compose up -d` from operators.
"""

from __future__ import annotations

from pathlib import Path

import pytest
import yaml

DEPLOY_DIR = Path(__file__).resolve().parent.parent / "deploy"


def _load(name: str) -> dict:
    """Parse one compose YAML file from deploy/ into a dict."""
    with (DEPLOY_DIR / name).open() as fh:
        return yaml.safe_load(fh)


class TestFullStackCompose:
    """deploy/docker-compose.yml — the three-service all-in-one flavour."""

    @pytest.fixture
    def doc(self) -> dict:
        return _load("docker-compose.yml")

    def test_has_three_services(self, doc: dict) -> None:
        services = doc.get("services", {})
        assert set(services) == {"rustfs", "neo4j", "qatlasd"}, (
            f"unexpected service set {set(services)}; full-stack template "
            "must always include rustfs + neo4j + qatlasd together"
        )

    def test_qatlasd_image_is_ghcr(self, doc: dict) -> None:
        # If we ever change the image registry, this test forces the
        # docs / release.yml / install.md / .env.docker.example to be
        # updated in lockstep.
        image = doc["services"]["qatlasd"]["image"]
        assert image.startswith("ghcr.io/iai-ustc-quantum/qatlasd:"), (
            f"qatlasd image = {image!r}; must stay on ghcr.io/iai-ustc-quantum "
            "so install docs + release.yml stay in sync"
        )

    def test_qatlasd_depends_on_backing_services(self, doc: dict) -> None:
        depends = doc["services"]["qatlasd"].get("depends_on", [])
        assert "rustfs" in depends
        assert "neo4j" in depends

    def test_required_env_vars_have_failure_guards(self, doc: dict) -> None:
        # The `${VAR:?error message}` syntax makes compose fail fast at
        # `up` time with a readable error when the operator forgets to
        # populate .env. This guard is too easy to lose in a refactor;
        # the test pins the set of fields that MUST stay required.
        env = doc["services"]["qatlasd"]["environment"]
        for required_var in [
            "QATLAS_S3_ACCESS_KEY_ID",
            "QATLAS_S3_SECRET_ACCESS_KEY",
            "NEO4J_PASSWORD",
        ]:
            interpolation = env.get(required_var)
            assert interpolation is not None, (
                f"env block missing {required_var}; compose will boot with empty "
                "value and qatlasd will fail in a more confusing way later"
            )
            assert ":?" in interpolation, (
                f"{required_var} interpolation = {interpolation!r}; "
                "use ${VAR:?error message} so compose fails fast with a readable msg"
            )

    def test_volumes_match_dockerfile_volume_directive(self, doc: dict) -> None:
        # The Dockerfile declares VOLUME ["/data/raw","/data/pb_data","/data/wiki"].
        # The compose bind mounts here MUST target the same paths or
        # operator data ends up in an anonymous docker volume on every
        # `docker compose down`.
        mounts = doc["services"]["qatlasd"]["volumes"]
        targets = {m.split(":", 1)[1].split(":", 1)[0] for m in mounts}
        assert "/data/raw" in targets
        assert "/data/pb_data" in targets
        assert "/data/wiki" in targets

    def test_qatlasd_loopback_bind(self, doc: dict) -> None:
        # Public exposure is operator-controlled (reverse proxy), so the
        # default port binding stays loopback-only. This guard exists to
        # catch a careless `4200:4200` change that would suddenly publish
        # the API on every interface.
        ports = doc["services"]["qatlasd"].get("ports", [])
        assert any(p.startswith("127.0.0.1:") for p in ports), (
            f"qatlasd ports = {ports}; default must bind loopback, not 0.0.0.0"
        )


class TestStandaloneCompose:
    """deploy/docker-compose.standalone.yml — qatlasd-only flavour."""

    @pytest.fixture
    def doc(self) -> dict:
        return _load("docker-compose.standalone.yml")

    def test_only_qatlasd_service(self, doc: dict) -> None:
        services = doc.get("services", {})
        assert set(services) == {"qatlasd"}, (
            f"standalone template must not include backing services; got {set(services)}"
        )

    def test_external_endpoints_required(self, doc: dict) -> None:
        env = doc["services"]["qatlasd"]["environment"]
        # External endpoints are the whole point of the standalone
        # flavour; if either has a default we've quietly turned this
        # back into the all-in-one variant.
        for var in ("QATLAS_S3_ENDPOINT", "NEO4J_URI"):
            assert ":?" in env[var], (
                f"{var} must be required in the standalone template (no default makes sense); "
                f"got interpolation {env[var]!r}"
            )

    def test_no_depends_on_backing_services(self, doc: dict) -> None:
        # Standalone explicitly defers backing services to the
        # operator; depends_on would mean "wait for an internal service
        # that doesn't exist", which compose interprets as a config error.
        assert "depends_on" not in doc["services"]["qatlasd"]


class TestEnvDockerExampleStaysInSyncWithCompose:
    """The .env.docker.example must mention every required compose var
    so a fresh operator hitting `cp .env.docker.example .env` doesn't
    immediately get a `compose: variable not set` error.
    """

    def test_required_vars_documented(self) -> None:
        example = (DEPLOY_DIR / ".env.docker.example").read_text()
        for required_var in [
            "RUSTFS_ROOT_ACCESS_KEY",
            "RUSTFS_ROOT_SECRET_KEY",
            "QATLAS_S3_ACCESS_KEY_ID",
            "QATLAS_S3_SECRET_ACCESS_KEY",
            "NEO4J_PASSWORD",
            "GITHUB_CLIENT_ID",
            "GITHUB_CLIENT_SECRET",
        ]:
            assert required_var in example, (
                f".env.docker.example missing {required_var}; operators will be "
                "surprised by a compose interpolation error on `up`"
            )


class TestDockerfileSanity:
    """High-signal grep over the Dockerfile so structural assumptions
    (multi-stage, distroless base, nonroot UID) don't silently regress.
    """

    @pytest.fixture
    def dockerfile(self) -> str:
        return (DEPLOY_DIR.parent / "Dockerfile").read_text()

    def test_multi_stage_build(self, dockerfile: str) -> None:
        # Three named stages: web, builder, (runtime stays unnamed at
        # the end). Loss of multi-stage = ballooning final image size.
        assert "AS web" in dockerfile
        assert "AS builder" in dockerfile

    def test_distroless_static_base(self, dockerfile: str) -> None:
        # Distroless static keeps the image at ~50 MB AND eliminates
        # the shell. Bumping to debian-slim / alpine without telling
        # anyone breaks the docs that promise a minimal image.
        assert "gcr.io/distroless/static-debian12:nonroot" in dockerfile

    def test_static_link_flags(self, dockerfile: str) -> None:
        # CGO_ENABLED=0 + -extldflags=-static together are what make
        # the binary work under distroless static. Either being dropped
        # produces a runtime "no such file or directory" on the binary.
        assert "CGO_ENABLED=0" in dockerfile
        assert "-extldflags=-static" in dockerfile

    def test_runtime_user_is_nonroot(self, dockerfile: str) -> None:
        # USER nonroot:nonroot makes the container default to UID
        # 65532. This is documented in docs/deployment/docker.md
        # (chown 65532:65532 ./data/* before first start); dropping it
        # to default root silently makes the warnings in docs wrong.
        assert "USER nonroot:nonroot" in dockerfile

    def test_volumes_declared(self, dockerfile: str) -> None:
        # docker-compose bind mounts target these paths; the VOLUME
        # directive is what tells `docker run` to manage the data dirs
        # at all when the operator uses neither bind mount nor named
        # volume. Don't lose them silently.
        assert "/data/raw" in dockerfile
        assert "/data/pb_data" in dockerfile
        assert "/data/wiki" in dockerfile
