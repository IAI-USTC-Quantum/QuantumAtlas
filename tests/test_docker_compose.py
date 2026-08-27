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
    """deploy/docker-compose.yml — qatlasd-only flavour; PostgreSQL is an
    external shared/general-purpose instance reached via QATLAS_POSTGRES_DSN."""

    @pytest.fixture
    def doc(self) -> dict:
        return _load("docker-compose.yml")

    def test_qatlasd_only_service(self, doc: dict) -> None:
        services = doc.get("services", {})
        assert set(services) == {"qatlasd", "qatlas-search"}, (
            f"unexpected service set {set(services)}; postgres is an external "
            "shared instance and must not be defined in this template, and "
            "qatlas-search is the only sanctioned sibling (agentic search "
            "microservice, profile-gated)"
        )

    def test_qatlas_search_is_profile_gated_and_internal_only(self, doc: dict) -> None:
        # qatlas-search is optional (compose --profile search) and must
        # stay internal-only: qatlasd is its only legitimate caller (it
        # meters and quotas every agentic search), so no host ports.
        svc = doc["services"]["qatlas-search"]
        assert "search" in svc.get("profiles", []), (
            "qatlas-search must be profile-gated (profiles: [search]) so the "
            "default `docker compose up -d` stays qatlasd-only"
        )
        assert "ports" not in svc, (
            "qatlas-search must not publish host ports; only qatlasd calls it"
        )

    def test_qatlas_search_image_is_ghcr(self, doc: dict) -> None:
        # Deploy hosts pull the image published by the qatlas-search
        # repo's release workflow — they never `docker build` locally.
        # The tag is pinned via QATLAS_SEARCH_VERSION (same interpolation
        # pattern as QATLAS_VERSION for qatlasd).
        image = doc["services"]["qatlas-search"]["image"]
        assert image.startswith("ghcr.io/iai-ustc-quantum/qatlas-search:"), (
            f"qatlas-search image = {image!r}; must come from ghcr "
            "(release-built), not a local build tag"
        )
        assert "QATLAS_SEARCH_VERSION" in image, (
            f"qatlas-search image = {image!r}; tag must interpolate "
            "QATLAS_SEARCH_VERSION so deploys can pin the version"
        )

    def test_no_neo4j_service(self, doc: dict) -> None:
        # Neo4j was removed when the project repositioned to
        # paper collection + search + Postgres registry. It must not
        # creep back into the compose templates.
        services = doc.get("services", {})
        assert "neo4j" not in services

    def test_no_depends_on_backing_services(self, doc: dict) -> None:
        # Backing services (postgres, rustfs) are external by design;
        # depends_on would reference services that don't exist here.
        assert "depends_on" not in doc["services"]["qatlasd"]

    def test_host_gateway_for_external_postgres(self, doc: dict) -> None:
        # The canonical DSN targets a postgres listening on the docker
        # host's loopback; that only works with the host-gateway mapping.
        extra = doc["services"]["qatlasd"].get("extra_hosts", [])
        assert any("host.docker.internal" in h for h in extra), (
            f"extra_hosts = {extra}; must map host.docker.internal so the "
            "external-postgres DSN keeps working from inside the container"
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

    def test_qatlasd_has_no_environment_block(self, doc: dict) -> None:
        # qatlasd rejects ALL environment-variable configuration at
        # startup (env mode was removed in favour of the YAML config
        # file). An `environment:` block here would make the container
        # fail to boot with an env-rejection error.
        svc = doc["services"]["qatlasd"]
        assert "environment" not in svc, (
            f"qatlasd environment block crept back: {svc.get('environment')!r}; "
            "configuration belongs in the bind-mounted config.yaml"
        )

    def test_config_yaml_bind_mounted_readonly(self, doc: dict) -> None:
        # The host's ~/.qatlas/config.yaml is the single source of truth,
        # mounted read-only at the distroless nonroot user's home (the
        # default path qatlasd resolves inside the container).
        mounts = doc["services"]["qatlasd"]["volumes"]
        cfg = [m for m in mounts if m.split(":")[1] == "/home/nonroot/.qatlas/config.yaml"]
        assert cfg, (
            f"volumes = {mounts}; must bind-mount the host config.yaml at "
            "/home/nonroot/.qatlas/config.yaml"
        )
        assert cfg[0].endswith(":ro"), (
            f"config mount = {cfg[0]!r}; must be read-only (:ro)"
        )
        assert cfg[0].startswith("${HOME}/.qatlas/config.yaml:"), (
            f"config mount source = {cfg[0]!r}; must come from the host's "
            "${HOME}/.qatlas/config.yaml"
        )

    def test_volumes_match_dockerfile_volume_directive(self, doc: dict) -> None:
        # The compose bind mounts here MUST target the Dockerfile VOLUME
        # paths or operator data ends up in an anonymous docker volume
        # on every `docker compose down`.
        mounts = doc["services"]["qatlasd"]["volumes"]
        targets = {m.split(":", 1)[1].split(":", 1)[0] for m in mounts}
        assert "/data/raw" in targets
        assert "/data/pb_data" in targets

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

    def test_no_environment_block_config_file_instead(self, doc: dict) -> None:
        # External endpoints are the whole point of the standalone
        # flavour, and they now live in the host's config.yaml — env
        # configuration was removed, so an environment block here would
        # make qatlasd refuse to boot.
        svc = doc["services"]["qatlasd"]
        assert "environment" not in svc, (
            f"standalone qatlasd environment block = {svc.get('environment')!r}; "
            "connection details belong in the bind-mounted config.yaml"
        )
        mounts = svc["volumes"]
        assert any(
            m.split(":")[1] == "/home/nonroot/.qatlas/config.yaml" and m.endswith(":ro")
            for m in mounts
        ), f"standalone volumes = {mounts}; must bind-mount config.yaml read-only"

    def test_no_depends_on_backing_services(self, doc: dict) -> None:
        # Standalone explicitly defers backing services to the
        # operator; depends_on would mean "wait for an internal service
        # that doesn't exist", which compose interprets as a config error.
        assert "depends_on" not in doc["services"]["qatlasd"]


class TestEnvDockerExampleStaysInSyncWithCompose:
    """The .env.docker.example exists only for compose-side variable
    interpolation (image tag). Application config moved to the YAML
    config file, so the example must stay minimal and must NOT
    reintroduce app-side QATLAS_* variables (they would make qatlasd
    refuse to boot if they ever got passed through).
    """

    def test_only_compose_interpolation_vars(self) -> None:
        example = (DEPLOY_DIR / ".env.docker.example").read_text()
        assert "QATLAS_VERSION" in example, (
            ".env.docker.example must keep QATLAS_VERSION (image tag interpolation)"
        )

    def test_no_app_config_vars(self) -> None:
        example = (DEPLOY_DIR / ".env.docker.example").read_text()
        for var in [
            "QATLAS_POSTGRES_DSN",
            "QATLAS_S3_ACCESS_KEY_ID",
            "QATLAS_S3_SECRET_ACCESS_KEY",
            "GITHUB_CLIENT_ID",
            "GITHUB_CLIENT_SECRET",
            "MINERU_API_TOKENS",
            "QATLAS_SYSTEM_PAT",
        ]:
            assert var not in example, (
                f".env.docker.example re-introduced {var}; app config lives in "
                "config.yaml now, not in compose interpolation"
            )

    def test_no_neo4j_leftovers(self) -> None:
        # Neo4j is gone from the stack; the example env must not keep
        # documenting variables nothing reads anymore.
        example = (DEPLOY_DIR / ".env.docker.example").read_text()
        assert "NEO4J" not in example


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
        # 65532. This is documented in docs/server/docker.md
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
        # /data/wiki was removed together with the wiki subsystem.
        assert "/data/wiki" not in dockerfile
