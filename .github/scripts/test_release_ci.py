"""Offline CI fixtures; never authenticate, publish, or contact a live server."""

import io
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import stat
import tarfile
import tempfile
import threading
import unittest
from unittest.mock import Mock, patch
import urllib.error
import zipfile

import artifacts
import release_gate


class VersionAndReleaseTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.version = Path(self.temporary.name) / "VERSION"

    def validate(self, version):
        self.version.write_text(version + "\n")
        return release_gate.validate_version("v" + version, self.version)

    def test_semver_not_pep440(self):
        for version, prerelease in (("0.35.0", False), ("1.2.3-rc.1", True), ("1.2.3-alpha.beta-2", True)):
            with self.subTest(version=version):
                self.assertEqual(self.validate(version), (version, prerelease))
        for version in ("1.2.3a1", "01.2.3", "1.2.3-rc.01", "1.2.3+build", "1.2.3-", "1.2.3\ninjected=yes"):
            with self.subTest(version=version), self.assertRaises(ValueError):
                self.validate(version)
        self.version.write_text("0.35.0")
        with self.assertRaises(ValueError):
            release_gate.validate_version("v0.34.0", self.version)
        with self.assertRaises(ValueError):
            release_gate.validate_version("0.35.0", self.version)

    def release(self, draft=True, prerelease=False):
        return {"id": 123, "tag_name": "v0.35.0", "draft": draft, "prerelease": prerelease, "html_url": "https://example.test/release"}

    def test_guard_absent_draft_public(self):
        for state in (None, self.release()):
            api = Mock()
            api.find.return_value = state
            self.assertEqual(release_gate.guard(api, "v0.35.0"), state)
        api.find.return_value = self.release(draft=False)
        with self.assertRaisesRegex(ValueError, "already public"):
            release_gate.guard(api, "v0.35.0")
        api.request.assert_not_called()

    def test_query_failures_never_mean_absent(self):
        api = Mock()
        api.find.side_effect = RuntimeError("HTTP 503")
        with self.assertRaises(RuntimeError):
            release_gate.guard(api, "v0.35.0")
        api.request.assert_not_called()
        env = {"GH_TOKEN": "fixture-only", "GITHUB_REPOSITORY": "example/repo", "GITHUB_API_URL": "https://api.github.test"}
        for code in (401, 403, 404, 429, 500, 503):
            with self.subTest(code=code), patch.dict("os.environ", env), patch("urllib.request.OpenerDirector.open", side_effect=urllib.error.HTTPError("https://api.github.test", code, "fixture", {}, None)):
                with self.assertRaisesRegex(RuntimeError, f"HTTP {code}"):
                    release_gate.guard(release_gate.GitHub(), "v0.35.0")

    def test_redirect_never_forwards_authorization(self):
        received = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                received.append((self.path, self.headers.get("Authorization")))
                self.send_response(302)
                self.send_header("Location", "/must-not-receive-token")
                self.end_headers()

            def log_message(self, *_args):
                pass

        # Deliberately local HTTP fixture, never a real token or live API. The
        # production constructor requires HTTPS; bypass only for this test.
        with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
            worker = threading.Thread(target=server.serve_forever, daemon=True)
            worker.start()
            try:
                api = object.__new__(release_gate.GitHub)
                api.base = f"http://127.0.0.1:{server.server_port}"
                api.repo, api.token = "example/repo", "fixture-only-not-a-secret"
                with self.assertRaisesRegex(RuntimeError, "HTTP 302"):
                    api.request("releases")
            finally:
                server.shutdown()
                worker.join()
        self.assertEqual(received, [("/repos/example/repo/releases", "Bearer fixture-only-not-a-secret")])

    def test_paginated_drafts_and_malformed_response(self):
        api = object.__new__(release_gate.GitHub)
        api.request = Mock(side_effect=[[{"tag_name": "other"}] * 100, [self.release()]])
        self.assertEqual(api.find("v0.35.0"), self.release())
        self.assertIn("page=2", api.request.call_args.args[0])
        api.request = Mock(return_value=[])
        self.assertIsNone(api.find("v0.35.0"))
        for response in ({"message": "API error"}, [{"tag_name": "v0.35.0", "draft": "false"}], [self.release(), self.release(draft=False)]):
            api.request = Mock(return_value=response)
            with self.assertRaises(ValueError):
                api.find("v0.35.0")

    def test_publish_requires_draft_and_defers_latest(self):
        api = Mock()
        api.find.return_value = self.release()
        api.request.return_value = self.release(draft=False)
        release_gate.publish(api, "v0.35.0", False)
        api.request.assert_called_once_with("releases/123", {"draft": False, "make_latest": "false"})
        for state in (None, self.release(draft=False), self.release(prerelease=True)):
            api = Mock()
            api.find.return_value = state
            with self.assertRaises(ValueError):
                release_gate.publish(api, "v0.35.0", False)
            api.request.assert_not_called()

    def test_latest_only_for_public_stable(self):
        for state in (None, self.release(), self.release(draft=False, prerelease=True)):
            api = Mock()
            api.find.return_value = state
            with self.assertRaises(ValueError):
                release_gate.public_stable(api, "v0.35.0")
            api.request.assert_not_called()
        api.find.return_value = self.release(draft=False)
        self.assertEqual(release_gate.public_stable(api, "v0.35.0"), api.find.return_value)


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

    def manifest(self, files):
        result = self.root / "qatlasd_0.35.0_checksums.txt"
        result.write_text("".join(f"{artifacts.digest(path)}  {path.name}\n" for path in files))
        return result

    def test_default_checksum_contract_includes_zip(self):
        files = []
        for platform in (*artifacts.PLATFORMS, "web"):
            suffix = ".zip" if platform == "web" else ".tar.gz"
            path = self.root / f"qatlasd_0.35.0_{platform}{suffix}"
            path.write_bytes(platform.encode())
            files.append(path)
        manifest = self.manifest(files)
        self.assertEqual(artifacts.verify_release(self.root, self.root, "0.35.0"), manifest)
        files[-1].write_text("modified UI zip")
        with self.assertRaisesRegex(ValueError, "checksum"):
            artifacts.verify_release(self.root, self.root, "0.35.0")
        self.manifest(files[:-1])
        with self.assertRaisesRegex(ValueError, "asset set"):
            artifacts.verify_release(self.root, self.root, "0.35.0")
        manifest.write_text(manifest.read_text() * 2)
        with self.assertRaisesRegex(ValueError, "duplicate"):
            artifacts.checksums(self.root)
        (self.root / "other_checksums.txt").write_text("another manifest")
        with self.assertRaisesRegex(ValueError, "exactly one"):
            artifacts.checksums(self.root)

    def tar(self, extra=None):
        path = self.root / "qatlasd_0.35.0_linux_amd64.tar.gz"
        with tarfile.open(path, "w:gz") as bundle:
            data = b"binary fixture, not executed"
            member = tarfile.TarInfo("qatlasd")
            member.size = len(data)
            bundle.addfile(member, io.BytesIO(data))
            if extra:
                bundle.addfile(extra, io.BytesIO(b""))
        self.manifest([path])
        return path

    @patch("subprocess.check_output", return_value="qatlasd version 0.35.0\n")
    def test_smoke_checksum_extract_and_only_version(self, run):
        self.tar()
        artifacts.smoke(self.root, "0.35.0", "linux_amd64")
        self.assertEqual(run.call_args.args[0][1:], ["--version"])
        self.assertEqual(run.call_args.kwargs["timeout"], 30)
        self.assertEqual(set(run.call_args.kwargs["env"]), {"PATH", "HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME"})
        self.assertEqual(run.call_args.kwargs["cwd"], run.call_args.kwargs["env"]["HOME"])
        run.return_value = "qatlasd version dev\n"
        with self.assertRaisesRegex(ValueError, "binary version"):
            artifacts.smoke(self.root, "0.35.0", "linux_amd64")

    @patch("subprocess.check_output")
    def test_smoke_never_executes_unsafe_or_corrupt_archives(self, run):
        link = tarfile.TarInfo("link")
        link.type = tarfile.SYMTYPE
        link.linkname = "qatlasd"
        for member in (link, tarfile.TarInfo("../escape"), tarfile.TarInfo("qatlasd")):
            self.tar(member)
            with self.assertRaises(ValueError):
                artifacts.smoke(self.root, "0.35.0", "linux_amd64")
        path = self.tar()
        path.write_bytes(b"corrupt")
        with self.assertRaisesRegex(ValueError, "checksum"):
            artifacts.smoke(self.root, "0.35.0", "linux_amd64")
        run.assert_not_called()


class SourceContractTests(unittest.TestCase):
    def test_docker_reuses_complete_ui_and_keeps_runtime_contract(self):
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

    def test_goreleaser_defaults_and_explicit_extra_files(self):
        root = Path(__file__).resolve().parents[2]
        config = (root / ".goreleaser.yaml").read_text()
        for forbidden in ("name_template:", "ldflags:", "flags:", "formats:", "binary:"):
            self.assertNotIn(forbidden, config)
        self.assertEqual(config.count("glob: build/ui/qatlasd_*_web.zip"), 2)
        self.assertIn("tags: [embedui]", config)
        self.assertIn("ignore_tags: [quantum-atlas-v0.21.0]", config)
        self.assertNotIn("quantum-atlas-*", config)

    def test_reusable_ci_checks_go_formatting_and_both_docs_systems(self):
        root = Path(__file__).resolve().parents[2]
        workflow = (root / ".github/workflows/go.yml").read_text()
        self.assertIn("workflow_call:", workflow)
        self.assertEqual(workflow.count("ref: ${{ inputs.source_sha || github.sha }}"), 3)
        self.assertEqual(workflow.count('test "$(git rev-parse HEAD)" = "$SOURCE_SHA"'), 3)
        self.assertIn("git ls-files -z -- '*.go' | xargs -0 -r gofmt -l", workflow)
        self.assertIn("go test -tags=integration ./internal/... ./cmd/... -run '^$'", workflow)
        self.assertIn("run: go mod tidy -diff", workflow)
        self.assertIn("dist build qatlasd downloaderproxy downloaderworker", workflow)
        self.assertIn("python -m mkdocs build --strict --site-dir build/mkdocs", workflow)
        self.assertIn("cache-dependency-path: docs/requirements.txt", workflow)
        self.assertIn("cache-dependency-path: docsite/requirements.txt", workflow)
        self.assertEqual(workflow.count("run: bash .github/scripts/build-docs.sh"), 2)
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
