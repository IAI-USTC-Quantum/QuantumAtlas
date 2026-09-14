"""Offline CI fixtures; never authenticate, publish, or contact a live server."""

import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile

import artifacts


class UIVersionCLITests(unittest.TestCase):
    """Real isolated Git histories: no edits/tags in the working repository."""

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name) / "repo"
        self.home = Path(temporary.name) / "home"
        self.root.mkdir()
        self.home.mkdir()
        self.output = Path(temporary.name) / "github-output"
        # Neither user's Git hooks/config/signing nor business/API credentials
        # may enter the CLI fixtures. All commits and tags are local and empty.
        self.env = {
            "PATH": os.defpath, "HOME": str(self.home), "XDG_CONFIG_HOME": str(self.home),
            "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull,
            "PYTHONDONTWRITEBYTECODE": "1", "GITHUB_OUTPUT": str(self.output),
        }
        self.git("-c", "init.defaultBranch=fixture", "init", "-q", "--object-format=sha1")
        self.git("commit", "--allow-empty", "-qm", "fixture source")
        self.sha = self.git("rev-parse", "HEAD").strip()

    def git(self, *args):
        return subprocess.check_output(
            ["git", "-c", "user.name=Offline fixture", "-c", "user.email=fixture@example.invalid",
             "-c", "core.hooksPath=" + os.devnull, "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", *args],
            cwd=self.root, env=self.env, text=True, timeout=30,
        )

    def cli(self, script, *args, **env):
        self.output.write_text("")
        return subprocess.run(
            [sys.executable, str(Path(__file__).resolve().parent / script), *args],
            cwd=self.root, env=self.env | env, capture_output=True, text=True, timeout=30,
        )

    def select(self, tag="", sha=None):
        result = self.cli("artifacts.py", "ui-version", RELEASE_TAG=tag, SOURCE_SHA=self.sha if sha is None else sha)
        self.assertEqual(result.returncode, 0, result.stderr)
        expected = tag.removeprefix("v") if tag else f"0.0.0-ci.g{self.sha if sha is None else sha}"
        self.assertEqual(result.stdout, f"version={expected}\n")
        self.assertEqual(self.output.read_text(), result.stdout)
        self.assertFalse((self.root / "VERSION").exists())
        return expected

    def reject(self, tag, sha=None):
        result = self.cli("artifacts.py", "ui-version", RELEASE_TAG=tag, SOURCE_SHA=self.sha if sha is None else sha)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertEqual(self.output.read_text(), "")
        return result.stderr

    def test_ui_version_cli_needs_no_version_file_or_api(self):
        self.git("tag", "v1.2.3-rc.1")
        self.assertEqual(self.select("v1.2.3-rc.1"), "1.2.3-rc.1")
        obsolete = self.cli("artifacts.py", "ui-version", "--version-file", "VERSION", RELEASE_TAG="v1.2.3-rc.1", SOURCE_SHA=self.sha)
        self.assertNotEqual(obsolete.returncode, 0)
        self.assertIn("unrecognized arguments", obsolete.stderr)
        self.assertEqual(obsolete.stdout, "")
        self.assertEqual(self.output.read_text(), "")

    def test_real_tags_select_version_spelling_without_a_semver_gate(self):
        # This is UI handoff spelling only, not a claim that GoReleaser, the UI
        # packager or Docker accepts every spelling for full distribution.
        for tag, version in (
            ("1.2.3", "1.2.3"),
            ("v1.2.3+build", "1.2.3+build"),
            ("v1.2.3a1", "1.2.3a1"),
            ("v1.2.3-rc.01", "1.2.3-rc.01"),
            ("v01.2.3", "01.2.3"),
            ("vv1.2.3", "v1.2.3"),
        ):
            with self.subTest(tag=tag):
                self.git("tag", tag)
                self.assertEqual(self.select(tag), version)

    def test_branch_without_tags_still_selects_and_packages_ci_version(self):
        self.assertEqual(self.git("tag", "--list"), "")
        self.restore_selected_zip(self.select())
        self.assertEqual(self.git("tag", "--list"), "")
        # A normal branch run must not silently become a release at a tagged SHA.
        self.git("tag", "v9.9.9")
        self.assertEqual(self.select(), f"0.0.0-ci.g{self.sha}")

    def test_exact_lightweight_or_annotated_tag_even_with_multiple_at_head(self):
        self.git("tag", "v1.2.3")
        self.git("tag", "-a", "v1.2.4-rc.1", "-m", "annotated fixture")
        self.git("tag", "v9.9.9")
        self.assertNotEqual(self.git("rev-parse", "refs/tags/v1.2.4-rc.1").strip(), self.sha)
        for tag in ("v1.2.3", "v1.2.4-rc.1"):
            with self.subTest(tag=tag):
                self.assertEqual(self.select(tag), tag[1:])

    def test_checkout_tag_and_source_sha_must_all_agree(self):
        self.git("tag", "v1.2.3")
        old_sha = self.sha
        self.git("commit", "--allow-empty", "-qm", "different source")
        self.sha = self.git("rev-parse", "HEAD").strip()
        self.assertIn("release tag commit", self.reject("v1.2.3"))
        self.assertIn("checkout HEAD", self.reject("v1.2.3", old_sha))
        self.assertIn("checkout HEAD", self.reject("", old_sha))
        self.git("branch", "v8.8.8")
        self.reject("v8.8.8")  # never fall back to a branch or nearest tag

    def test_event_checkout_resolves_annotated_object_to_commit(self):
        self.git("tag", "-a", "v1.2.3-rc.1", "-m", "event fixture")
        event_object = self.git("rev-parse", "refs/tags/v1.2.3-rc.1").strip()
        self.assertNotEqual(event_object, self.sha)
        self.git("checkout", "--quiet", "--detach", event_object)
        self.assertEqual(self.git("rev-parse", "HEAD").strip(), self.sha)
        self.assertEqual(self.select("v1.2.3-rc.1"), "1.2.3-rc.1")

    def test_moved_tag_cannot_replace_the_pinned_event_commit(self):
        self.git("tag", "v1.2.3")
        event_sha = self.sha
        self.git("commit", "--allow-empty", "-qm", "later source")
        self.git("tag", "--force", "v1.2.3")
        self.git("checkout", "--quiet", "--detach", event_sha)
        self.assertIn("release tag commit", self.reject("v1.2.3", event_sha))

    def test_invalid_git_refs_cannot_select_revisions_or_inject_outputs(self):
        self.git("tag", "v1.2.3")
        # Some invalid tag names are valid rev-parse expressions. Checking a
        # missing tag alone would not catch accidentally accepting these.
        self.git("commit", "--allow-empty", "-qm", "revision fixture")
        self.sha = self.git("rev-parse", "HEAD").strip()
        self.git("tag", "v1.2.4")
        for tag in (
            "v1.2.3^{}", "v1.2.4^0", "v1.2.4~0", "v1.2.4^{commit}",
            "v1.2.3\ninjected=yes", "v1.2.3\rinjected=yes", "v1.2.3\tbad", "v1.2.3\x7f",
            " v1.2.3", "v1.2.3 ", "v1..2.3", "v1.2.3:bad", "v1.2.3\\bad",
            "v1.2.3[bad", "v1.2.3?bad", "v1.2.3*", "v1.2.3.lock", "v1.2.3/",
            "v1.2.3;touch injected", "v1.2.3$(touch injected)",
        ):
            with self.subTest(tag=tag):
                ref = subprocess.run(
                    ["git", "check-ref-format", f"refs/tags/{tag}"],
                    cwd=self.root, env=self.env, capture_output=True, text=True, timeout=30,
                )
                self.assertNotEqual(ref.returncode, 0, "fixture must be a genuinely invalid Git ref")
                self.reject(tag)
        self.assertFalse((self.root / "injected").exists())

    def test_real_tag_path_and_shell_characters_are_literal(self):
        # Git permits these literal characters. The UI selector is not a shell
        # or a distribution validator; version/path compatibility is downstream.
        for tag in (
            "release/1.2.3", "v1.2.3;touch${IFS}injected",
            "v1.2.3$(touch${IFS}injected)", "v1.2.3`touch${IFS}injected`",
        ):
            with self.subTest(tag=tag):
                self.git("tag", tag)
                self.select(tag)
                self.assertFalse((self.root / "injected").exists())

    def test_malformed_source_sha_cannot_inject_outputs(self):
        for sha in ("", self.sha[:12], "A" * 40, "0" * 39, "0" * 41, "0" * 63, "0" * 65, self.sha + "\ninjected=yes"):
            with self.subTest(sha=sha):
                self.assertIn("source SHA", self.reject("", sha))

    def test_sha1_sha256_and_numeric_hashes_keep_unpublished_ci_spelling(self):
        # Fake Git covers SHA-256 and improbable numeric hashes without mining
        # commits. The 'g' prefix prevents leading-zero numeric identifiers.
        for sha in ("a1" * 20, "a1" * 32, "0" + "1" * 39, "0" + "1" * 63):
            with self.subTest(sha=sha), patch("artifacts.subprocess.check_output", return_value=sha + "\n") as git:
                version = artifacts.ui_version("", sha)
                self.assertEqual(version, f"0.0.0-ci.g{sha}")
                git.assert_called_once_with(["git", "rev-parse", "--verify", "HEAD"], text=True, timeout=30)

    def restore_selected_zip(self, version):
        archive = self.root / f"qatlasd_{version}_web.zip"
        with zipfile.ZipFile(archive, "w") as bundle:
            bundle.comment = f"qatlas-ui-v1:{version}".encode()
            for name in (*artifacts.REQUIRED_UI, "assets/app.js", "doc/.buildinfo"):
                bundle.writestr(name, "offline UI fixture " + name)
        restored = self.root / "restored"
        result = self.cli("artifacts.py", "restore-ui", str(archive), str(restored), version)
        self.assertEqual(result.returncode, 0, result.stderr)
        artifacts.tree(restored)
        return archive

    def test_zip_name_and_metadata_require_exact_tag_derived_version(self):
        self.git("tag", "v1.2.3-rc.1")
        version = self.select("v1.2.3-rc.1")
        archive = self.restore_selected_zip(version)
        destination = self.root / "rejected"
        # A correct comment cannot compensate for a different archive name.
        result = self.cli("artifacts.py", "restore-ui", str(archive), str(destination), "1.2.3")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("exactly one UI zip", result.stderr)
        # A correct archive name cannot compensate for a different ZIP comment.
        for wrong in ("1.2.3", "v" + version, f"0.0.0-ci.g{self.sha}"):
            with self.subTest(comment=wrong):
                with zipfile.ZipFile(archive, "a") as bundle:
                    bundle.comment = f"qatlas-ui-v1:{wrong}".encode()
                result = self.cli("artifacts.py", "restore-ui", str(archive), str(destination), version)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("version/comment mismatch", result.stderr)
                self.assertFalse(destination.exists())


class ArtifactTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)

    def ui(self, root):
        for name in (*artifacts.REQUIRED_UI, "assets/app.js", "doc/.buildinfo", "doc/_static/search.js"):
            target = root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("fixture " + name)
        return root

    def test_complete_tree_changes_include_sphinx_and_hidden(self):
        first, second = self.ui(self.root / "a"), self.ui(self.root / "b")
        artifacts.compare(first, second)
        for name in ("doc/.buildinfo", "doc/_static/search.js"):
            with self.subTest(name=name):
                target = second / name
                original = target.read_text()
                target.write_text("timestamp drift")
                with self.assertRaisesRegex(ValueError, "non-reproducible"):
                    artifacts.compare(first, second)
                target.write_text(original)
        extra = second / "assets/new-hash.js"
        extra.write_text("new untracked hash file")
        with self.assertRaises(ValueError):
            artifacts.compare(first, second)
        extra.unlink()
        (second / "doc/_static/search.js").unlink()
        with self.assertRaises(ValueError):
            artifacts.compare(first, second)

    def test_missing_docs_and_symlink_rejected(self):
        root = self.ui(self.root / "a")
        (root / "devdoc/dev/index.html").unlink()
        with self.assertRaises(ValueError):
            artifacts.tree(root)
        self.ui(root)
        (root / "link").symlink_to(root / "index.html")
        with self.assertRaises(ValueError):
            artifacts.tree(root)

    def test_ui_requires_nonempty_direct_js_and_regular_entrypoints(self):
        root = self.ui(self.root / "a")
        js = root / "assets/app.js"
        js.write_bytes(b"")
        with self.assertRaisesRegex(ValueError, "JavaScript|assets"):
            artifacts.tree(root)
        js.unlink()
        js.mkdir()
        with self.assertRaises(ValueError):
            artifacts.tree(root)
        js.rmdir()
        nested = root / "assets/nested/app.js"
        nested.parent.mkdir()
        nested.write_text("nested only")
        with self.assertRaises(ValueError):
            artifacts.tree(root)
        js.write_text("nonempty")
        (root / "index.html").unlink()
        (root / "index.html").mkdir()
        with self.assertRaises(ValueError):
            artifacts.tree(root)
        for name in ("nul\x00file", "cr\rfile", "lf\nfile"):
            with self.subTest(name=name), self.assertRaises(ValueError):
                artifacts.safe_name(name)

    def zip(self, extra=None, comment=b"qatlas-ui-v1:0.35.0"):
        source = self.ui(self.root / "source")
        target = self.root / "ui.zip"
        with zipfile.ZipFile(target, "w") as bundle:
            bundle.comment = comment
            for path in source.rglob("*"):
                if path.is_file():
                    bundle.write(path, path.relative_to(source).as_posix())
            if extra:
                bundle.writestr(*extra)
        return target

    def test_exactly_one_versioned_ui_input_before_release(self):
        archive = self.root / "qatlasd_0.35.0_web.zip"
        archive.write_bytes(b"fixture")
        artifacts.unique_ui_archive(archive, "0.35.0")
        with self.assertRaises(ValueError):
            artifacts.unique_ui_archive(archive, "0.36.0")
        (self.root / "qatlasd_0.34.0_web.zip").write_bytes(b"stale")
        with self.assertRaisesRegex(ValueError, "exactly one"):
            artifacts.unique_ui_archive(archive, "0.35.0")

    def test_restore_preserves_complete_tree(self):
        archive = self.zip()
        artifacts.restore_ui(archive, self.root / "restored", "0.35.0")
        artifacts.compare(self.root / "source", self.root / "restored")
        with self.assertRaises(ValueError):
            artifacts.restore_ui(archive, self.root / "restored", "0.35.0")

    def test_ui_zip_limits_match_go_and_fail_before_install(self):
        self.assertEqual((artifacts.MAX_BUNDLE_SIZE, artifacts.MAX_EXPANDED_SIZE, artifacts.MAX_FILE_SIZE, artifacts.MAX_BUNDLE_FILES), (64 << 20, 256 << 20, 16 << 20, 20000))
        archive = self.zip()
        for limit in ("MAX_BUNDLE_SIZE", "MAX_EXPANDED_SIZE", "MAX_FILE_SIZE", "MAX_BUNDLE_FILES"):
            with self.subTest(limit=limit), patch.object(artifacts, limit, 1), self.assertRaises(ValueError):
                artifacts.restore_ui(archive, self.root / "restored", "0.35.0")
            self.assertFalse((self.root / "restored").exists())

    def test_ui_wrong_version_traversal_and_link_rejected(self):
        with self.assertRaises(ValueError):
            artifacts.restore_ui(self.zip(comment=b"qatlas-ui-v1:wrong"), self.root / "restored", "0.35.0")
        for name in ("../escape", "/absolute", "a/../escape", "a\\escape", "C:escape"):
            with self.subTest(name=name), self.assertRaises(ValueError):
                artifacts.restore_ui(self.zip((name, b"bad")), self.root / "restored", "0.35.0")
            self.assertFalse((self.root / "restored").exists())
        link = zipfile.ZipInfo("symlink")
        link.create_system = 3
        link.external_attr = (stat.S_IFLNK | 0o777) << 16
        with self.assertRaises(ValueError):
            artifacts.restore_ui(self.zip((link, "index.html")), self.root / "restored", "0.35.0")


class SourceContractTests(unittest.TestCase):
    @staticmethod
    def configuration_text(relative):
        # These are lightweight source contracts, not a YAML/template evaluator.
        # Explanatory full-line comments must not count as configured behavior.
        root = Path(__file__).resolve().parents[2]
        return "".join(line for line in (root / relative).read_text().splitlines(keepends=True) if not line.lstrip().startswith("#"))

    def test_no_handwritten_root_changelog_or_version(self):
        root = Path(__file__).resolve().parents[2]
        for name in ("CHANGELOG.md", "VERSION"):
            with self.subTest(name=name):
                path = root / name
                self.assertFalse(path.exists() or path.is_symlink())
        # Deliberately root-only: GoReleaser's dist/CHANGELOG.md, historical
        # build output, docs commit stamps and third-party files are not inputs.

    def test_current_configs_scripts_and_docs_have_no_legacy_version_tooling(self):
        root = Path(__file__).resolve().parents[2]
        # Only current first-party policy surfaces. Do not traverse root build/
        # dist/, web/node_modules or generated web/public documentation.
        paths = set(root.glob("*")) | set((root / "web").glob("*"))
        for directory in (".github", "docs", "docsite", "hooks", "scripts", "deploy"):
            paths.update((root / directory).rglob("*"))
        text_suffixes = {".md", ".rst", ".toml", ".yaml", ".yml", ".json", ".cfg", ".ini", ".py", ".sh", ".txt", ".pages"}
        for path in sorted(paths):
            if not path.is_file() or path.name.startswith("test_"):
                continue  # regression fixtures name the forbidden tools on purpose
            with self.subTest(path=str(path.relative_to(root))):
                self.assertNotRegex(path.name.lower(), r"^\.?cz(?:\.|rc|$)|commitizen")
                if path.suffix in text_suffixes:
                    self.assertNotRegex(path.read_text(), r"(?i)commitizen|\bcz\b|\bcz_conventional_commits\b")

    def test_release_body_is_goreleaser_git_output_only(self):
        root = Path(__file__).resolve().parents[2]
        config = (root / ".goreleaser.yaml").read_text()
        self.assertRegex(config, r"(?m)^changelog:\n  use: git\n(?:\n|$)")
        for forbidden in ("header:", "footer:"):
            self.assertNotIn(forbidden, config)
        # Production workflows/helpers must not supply manual notes or replace
        # the generated body. No blanket ban on the word 'changelog' in docs.
        paths = list((root / ".github/workflows").glob("*.y*ml"))
        paths += [p for p in (root / ".github/scripts").iterdir() if p.suffix in {".py", ".sh"} and not p.name.startswith("test_")]
        for path in paths:
            content = path.read_text()
            with self.subTest(path=str(path.relative_to(root))):
                self.assertNotRegex(content, r"--(?:release-(?:notes|header|footer)|notes(?:-file|-from-tag)?|generate-notes)\b")
                self.assertNotRegex(content, r"(?:generate_release_notes|release_notes|body_path)\s*:|[\"']?body[\"']?\s*:")
                self.assertNotRegex(content, r"(?i)(?<![\w./-])(?:\./)?CHANGELOG\.md\b")

    def test_tag_source_and_single_goreleaser_release_chain_remain_connected(self):
        workflow = self.configuration_text(".github/workflows/release.yml")
        jobs = workflow.split("\njobs:\n", 1)[1]
        self.assertEqual(re.findall(r"(?m)^  ([\w-]+):\s*$", jobs), ["prep", "checks", "release"])
        prep, following = jobs.split("  checks:\n", 1)
        checks, release = following.split("  release:\n", 1)
        self.assertIn("tags: ['v*.*.*']", workflow)
        self.assertIn("cancel-in-progress: false", workflow)
        self.assertIn("if: github.ref_type == 'tag'", prep)
        # Pin the event object instead of looking up a potentially moved tag,
        # then hand off the resolved commit to every reusable check.
        self.assertIn("ref: ${{ github.sha }}", prep)
        self.assertNotIn("ref: ${{ github.ref }}", prep)
        self.assertIn('run: echo "sha=$(git rev-parse HEAD)" >> "$GITHUB_OUTPUT"', prep)
        self.assertIn("source_sha: ${{ steps.source.outputs.sha }}", prep)
        for required in (
            "needs: prep", "uses: ./.github/workflows/go.yml",
            "source_sha: ${{ needs.prep.outputs.source_sha }}",
            "release_tag: ${{ github.ref_name }}",
        ):
            with self.subTest(required=required):
                self.assertIn(required, checks)
        for required in (
            "needs: [prep, checks]", "ref: ${{ needs.prep.outputs.source_sha }}",
            "fetch-depth: 0", "persist-credentials: false",
            "name: ${{ needs.checks.outputs.ui_artifact }}", "path: build/ui",
            'VERSION="${GITHUB_REF_NAME#v}"',
            'artifacts.py restore-ui "build/ui/qatlasd_${VERSION}_web.zip" web/dist "$VERSION"',
            "registry: ghcr.io", "username: ${{ github.actor }}", "password: ${{ github.token }}",
            "contents: write", "packages: write", "distribution: goreleaser",
            "version: v2.18.1", "args: release --clean\n",
            "GITHUB_TOKEN: ${{ github.token }}", "GORELEASER_CURRENT_TAG: ${{ github.ref_name }}",
        ):
            with self.subTest(required=required):
                self.assertIn(required, release)
        steps = (
            "uses: actions/download-artifact@v4", "artifacts.py restore-ui",
            "uses: docker/setup-buildx-action@v3", "uses: docker/login-action@v3",
            "uses: goreleaser/goreleaser-action@v6",
        )
        self.assertEqual([release.index(step) for step in steps], sorted(release.index(step) for step in steps))
        self.assertEqual(release.count("uses: goreleaser/goreleaser-action@v6"), 1)
        for forbidden in ("release_gate.py", "verify-release", "artifacts.py smoke", "docker/build-push-action"):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, workflow)

    def test_official_attestations_follow_publish_with_default_manifest_names(self):
        workflow = self.configuration_text(".github/workflows/release.yml")
        release = workflow.split("  release:\n", 1)[1]
        self.assertIn("id-token: write", release)
        self.assertIn("attestations: write", release)
        self.assertIn('echo "version=$VERSION" >> "$GITHUB_OUTPUT"', release)
        self.assertIn("id: ui", release)
        self.assertEqual(release.count("uses: actions/attest@v4.2.2"), 2)
        self.assertLess(release.index("uses: goreleaser/goreleaser-action@v6"), release.index("uses: actions/attest@v4.2.2"))
        self.assertIn("subject-checksums: dist/qatlasd_${{ steps.ui.outputs.version }}_checksums.txt", release)
        self.assertIn("subject-checksums: dist/digests.txt", release)
        # Official defaults register attestations in GitHub, without a second
        # registry upload or optional artifact storage records/permissions.
        for extra in ("push-to-registry:", "create-storage-record:", "artifact-metadata:", "continue-on-error:"):
            self.assertNotIn(extra, release)
        config = self.configuration_text(".goreleaser.yaml")
        self.assertNotIn("name_template:", config)
        self.assertNotIn("docker_digest:", config)

    def test_local_docker_builds_source_with_complete_ui_and_keeps_runtime_contract(self):
        root = Path(__file__).resolve().parents[2]
        dockerfile = (root / "Dockerfile").read_text()
        instructions = [line.strip() for line in dockerfile.splitlines() if line.strip() and not line.lstrip().startswith("#")]
        self.assertEqual(sum(line.startswith("FROM ") for line in instructions), 2)
        self.assertNotIn("npm ", "\n".join(instructions))
        self.assertIn("COPY web/dist/ ./web/dist/", instructions)
        self.assertIn("-tags=embedui", "\n".join(instructions))
        self.assertIn("main.version=${VERSION}", dockerfile)
        self.assertNotIn("main.Version=", dockerfile)
        self.assertIn('VOLUME ["/data/raw", "/data/pb_data"]', instructions)
        self.assertIn("USER nonroot:nonroot", instructions)
        self.assertTrue((root / ".dockerignore").read_text().endswith("!web/dist/\n!web/dist/**\n"))
        self.assertEqual((root / "web/.node-version").read_text().strip(), "24.20.0")

    def test_goreleaser_dockerfile_reuses_binaries_without_building_source(self):
        dockerfile = self.configuration_text("Dockerfile.goreleaser")
        instructions = [line.strip() for line in dockerfile.splitlines() if line.strip()]
        self.assertEqual([line for line in instructions if line.startswith("FROM ")], ["FROM gcr.io/distroless/static-debian12:nonroot"])
        self.assertIn("ARG TARGETPLATFORM", instructions)
        self.assertEqual([line for line in instructions if line.startswith("COPY ")], ["COPY $TARGETPLATFORM/qatlasd /qatlasd"])
        self.assertFalse(any(line.startswith(("RUN ", "ADD ")) for line in instructions))
        local = self.configuration_text("Dockerfile")
        self.assertIn("RUN go build", local)
        self.assertIn("COPY --from=builder /out/qatlasd /qatlasd", local)
        for required in (
            'VOLUME ["/data/raw", "/data/pb_data"]', "EXPOSE 4200", "USER nonroot:nonroot",
            'ENTRYPOINT ["/qatlasd"]', 'CMD ["serve", "--http=0.0.0.0:4200"]',
        ):
            with self.subTest(required=required):
                self.assertIn(required, instructions)
                self.assertIn(required, local)

    def test_goreleaser_defaults_and_explicit_extra_files(self):
        config = self.configuration_text(".goreleaser.yaml")
        # Native preflight warnings and direct public releases are defaults, not
        # a handwritten gate or draft-then-promote configuration. Comments/docs
        # may explain those defaults without becoming settings themselves.
        for forbidden in ("name_template", "ldflags", "flags", "formats", "binary", "draft", "make_latest", "preflight", "fail_on_error"):
            with self.subTest(forbidden=forbidden):
                self.assertNotRegex(config, rf"(?m)^\s*{forbidden}:")
        self.assertEqual(config.count("glob: build/ui/qatlasd_*_web.zip"), 2)
        self.assertIn("checksum:\n  extra_files:\n    - glob: build/ui/qatlasd_*_web.zip", config)
        self.assertIn("release:\n  prerelease: auto\n  extra_files:\n    - glob: build/ui/qatlasd_*_web.zip", config)
        self.assertIn("tags: [embedui]", config)
        self.assertIn("targets: [linux_amd64, linux_arm64, darwin_arm64]", config)
        self.assertIn("env: [CGO_ENABLED=0]", config)
        self.assertIn("ignore_tags: [quantum-atlas-v0.21.0]", config)
        self.assertNotIn("quantum-atlas-*", config)

    def test_goreleaser_owns_docker_tags_and_default_linux_platforms(self):
        config = self.configuration_text(".goreleaser.yaml")
        self.assertNotRegex(config, r"(?m)^dockers:")
        docker = config.split("\ndockers_v2:\n", 1)[1]
        self.assertIn("- images:\n      - ghcr.io/iai-ustc-quantum/qatlasd", docker)
        self.assertIn("dockerfile: Dockerfile.goreleaser", docker)
        # No platform override: the pinned GoReleaser uses linux/amd64+arm64.
        # This checks configuration intent, not a substitute template evaluator.
        self.assertNotRegex(docker, r"(?m)^\s*(?:platforms|ids):")
        self.assertEqual(re.findall(r'(?m)^\s+- "(.*)"$', docker), [
            "{{ .Tag }}", "{{ .Version }}",
            "{{ if and (not .Prerelease) (not .IsSnapshot) }}latest{{ end }}",
        ])

    def test_reusable_ci_checks_go_formatting_and_both_docs_systems(self):
        root = Path(__file__).resolve().parents[2]
        workflow = (root / ".github/workflows/go.yml").read_text()
        self.assertIn("workflow_call:", workflow)
        self.assertRegex(workflow, r"release_tag:\n        description: [^\n]+\n        type: string\n        required: false\n        default: ''")
        self.assertIn("RELEASE_TAG: ${{ inputs.release_tag }}", workflow)
        self.assertIn("python3 .github/scripts/artifacts.py ui-version", workflow)
        self.assertIn("VERSION: ${{ steps.source.outputs.version }}", workflow)
        self.assertNotIn("< VERSION", workflow)
        self.assertIn("fetch-depth: 0", workflow.split("  web:\n", 1)[1])
        self.assertIn('go run ./internal/cmd/uibundle -version "$VERSION" -output build/ui', workflow)
        self.assertIn('artifacts.py restore-ui "build/ui/qatlasd_${VERSION}_web.zip" build/ui-restored "$VERSION"', workflow)
        self.assertIn("artifacts.py compare web/dist build/ui-restored", workflow)
        self.assertEqual(workflow.count("ref: ${{ inputs.source_sha || github.sha }}"), 3)
        self.assertEqual(workflow.count('test "$(git rev-parse HEAD)" = "$SOURCE_SHA"'), 3)
        self.assertIn("git ls-files -z -- '*.go' | xargs -0 -r gofmt -l", workflow)
        self.assertIn("go test -tags=integration ./internal/... ./cmd/... -run '^$'", workflow)
        self.assertIn("run: go mod tidy -diff", workflow)
        self.assertIn("run: go vet ./internal/... ./cmd/... ./tests/... ./web", workflow)
        self.assertIn("run: go test ./internal/... ./cmd/... ./tests/... ./web", workflow)
        self.assertIn("run: go build -o build/qatlasd ./cmd/qatlasd", workflow)
        self.assertIn("python3 -m unittest discover -s .github/scripts -p 'test_*.py' -v", workflow)
        self.assertIn("uses: goreleaser/goreleaser-action@v6", workflow)
        self.assertIn("version: v2.18.1\n          args: check", workflow)
        self.assertIn("go test -tags=embedui ./web ./cmd/qatlasd/...", workflow)
        self.assertIn("go build -tags=embedui -o build/qatlasd-embedui ./cmd/qatlasd", workflow)
        self.assertIn("dist build qatlasd downloaderproxy downloaderworker", workflow)
        self.assertIn("python -m mkdocs build --strict --site-dir build/mkdocs", workflow)
        self.assertIn("cache-dependency-path: docs/requirements.txt", workflow)
        self.assertIn("cache-dependency-path: docsite/requirements.txt", workflow)
        self.assertEqual(workflow.count("run: bash .github/scripts/build-docs.sh"), 2)
        # Audit the full locked graph, not only browser/runtime dependencies.
        self.assertIn("working-directory: web\n        run: npm audit\n", workflow)
        self.assertIn("working-directory: web\n        run: npm run lint\n", workflow)
        self.assertIn("working-directory: web\n        run: npm test\n", workflow)
        self.assertIn("run: npm exec -- playwright install --with-deps chromium", workflow)
        self.assertIn("working-directory: web\n        run: npm run test:browser\n", workflow)
        release = (root / ".github/workflows/release.yml").read_text()
        self.assertIn("uses: ./.github/workflows/go.yml", release)
        self.assertIn("source_sha: ${{ needs.prep.outputs.source_sha }}", release)
        self.assertIn("needs: [prep, checks]", release)

    def test_docs_image_reuses_clean_sphinx_build_without_legacy_publish(self):
        root = Path(__file__).resolve().parents[2]
        workflow = (root / ".github/workflows/docs.yml").read_text()
        self.assertIn("'.github/scripts/build-docs.sh'", workflow)
        self.assertIn('SOURCE_DATE_EPOCH="$(git show -s --format=%ct HEAD)"', workflow)
        self.assertIn("export SOURCE_DATE_EPOCH\n", workflow)
        self.assertIn("bash .github/scripts/build-docs.sh", workflow)
        self.assertNotIn("sphinx-build ", workflow)
        self.assertIn("cp -a web/public/doc web/public/devdoc docs-dist/", workflow)
        builder = (root / ".github/scripts/build-docs.sh").read_text()
        self.assertEqual(builder.count("sphinx-build -W --keep-going -b html"), 2)
        self.assertIn("ghcr.io/iai-ustc-quantum/qatlas-docs:latest", workflow)
        self.assertIn("timeout-minutes:", workflow)
        self.assertIn("concurrency:", workflow)
        for path in (root / ".github/workflows").glob("*.yml"):
            content = path.read_text()
            for forbidden in ("pypa/gh-action-pypi-publish", "uv publish", "uv build", "python -m build", "twine upload"):
                with self.subTest(workflow=path.name, forbidden=forbidden):
                    self.assertNotIn(forbidden, content)


if __name__ == "__main__":
    unittest.main()
