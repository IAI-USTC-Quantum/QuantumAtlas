"""Offline CI fixtures; never authenticate, publish, or contact a live server."""

import io
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import stat
import subprocess
import sys
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
    def test_semver_not_pep440(self):
        for version, prerelease in (("0.35.0", False), ("1.2.3-rc.1", True), ("1.2.3-alpha.beta-2", True)):
            with self.subTest(version=version):
                self.assertEqual(release_gate.validate_version("v" + version), (version, prerelease))
        for tag in ("v1.2.3a1", "v01.2.3", "v1.2.3-rc.01", "v1.2.3+build", "v1.2.3-", "v1.2.3\ninjected=yes", "1.2.3", "vv1.2.3", " v1.2.3", "v1.2.3\n"):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                release_gate.validate_version(tag)

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
        expected = tag[1:] if tag else f"0.0.0-ci.g{self.sha if sha is None else sha}"
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

    def test_tag_only_version_cli_needs_no_file_or_api(self):
        result = self.cli("release_gate.py", "version", "v1.2.3-rc.1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "version=1.2.3-rc.1\ntag=v1.2.3-rc.1\nis_prerelease=true\n")
        self.assertEqual(self.output.read_text(), result.stdout)
        self.assertFalse((self.root / "VERSION").exists())
        obsolete = self.cli("release_gate.py", "version", "v1.2.3", "--version-file", "VERSION")
        self.assertNotEqual(obsolete.returncode, 0)
        self.assertIn("unrecognized arguments", obsolete.stderr)

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
        self.reject("v8.8.8")  # nonexistent tag, never fall back to nearest tag

    def test_malformed_inputs_cannot_inject_outputs_or_shell_commands(self):
        for tag in ("1.2.3", "v1.2.3a1", "v1.2.3+build", "v1.2.3-rc.01", "v01.2.3", "v1.2.3\ninjected=yes", "v1.2.3;touch injected", "v1.2.3$(touch injected)"):
            with self.subTest(tag=tag):
                self.reject(tag)
        for sha in ("", self.sha[:12], "A" * 40, "0" * 39, "0" * 41, "0" * 63, "0" * 65, self.sha + "\ninjected=yes"):
            with self.subTest(sha=sha):
                self.assertIn("source SHA", self.reject("", sha))
        self.assertFalse((self.root / "injected").exists())

    def test_sha1_sha256_and_numeric_hashes_form_canonical_ci_semver(self):
        # Fake Git covers SHA-256 and improbable numeric hashes without mining
        # commits. The 'g' prefix prevents leading-zero numeric identifiers.
        for sha in ("a1" * 20, "a1" * 32, "0" + "1" * 39, "0" + "1" * 63):
            with self.subTest(sha=sha), patch("artifacts.subprocess.check_output", return_value=sha + "\n") as git:
                version = artifacts.ui_version("", sha)
                self.assertEqual(version, f"0.0.0-ci.g{sha}")
                self.assertEqual(release_gate.validate_version("v" + version), (version, True))
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

    def test_tag_version_and_validated_goreleaser_chain_remain_connected(self):
        root = Path(__file__).resolve().parents[2]
        workflow = (root / ".github/workflows/release.yml").read_text()
        self.assertNotIn("--version-file", (root / ".github/scripts/release_gate.py").read_text())
        self.assertIn('git rev-parse "refs/tags/$GITHUB_REF_NAME^{commit}"', workflow)
        self.assertIn("release_tag: ${{ needs.prep.outputs.tag }}", workflow)
        self.assertIn("GORELEASER_CURRENT_TAG: ${{ needs.prep.outputs.tag }}", workflow)
        for required in (
            "tags: ['v*.*.*']",
            'run: python3 .github/scripts/release_gate.py version "$GITHUB_REF_NAME"',
            "uses: ./.github/workflows/go.yml",
            "needs: [prep, checks]",
            "uses: goreleaser/goreleaser-action@v6",
            "version: v2.18.1",
            "args: release --clean\n",
            "fetch-depth: 0",
            "needs: [prep, release, native-smoke, docker-image]",
            'run: python3 .github/scripts/release_gate.py publish "$GITHUB_REF_NAME"',
        ):
            with self.subTest(required=required):
                self.assertIn(required, workflow)
        self.assertGreaterEqual(workflow.count('release_gate.py guard "$GITHUB_REF_NAME"'), 3)
        self.assertIn("draft: true", (root / ".goreleaser.yaml").read_text())

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
