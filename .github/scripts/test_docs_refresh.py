"""Exercise staged docs refresh without Docker or production files."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class DocsRefreshTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / 'repo'
        self.target = self.root / 'docs'
        self.repo.mkdir()
        self.target.mkdir()
        for name, entry in [('doc', 'index.html'), ('devdoc', 'dev/index.html')]:
            for base, value in [(self.target, 'previous'), (self.repo / 'web/public', 'new')]:
                path = base / name / entry
                path.parent.mkdir(parents=True)
                path.write_text(value)
        (self.target / 'VERSION').write_text('a' * 40 + '\n')
        subprocess.run(['git', 'init', '-q', str(self.repo)], check=True)
        subprocess.run(['git', '-C', str(self.repo), '-c', 'user.name=Test', '-c',
                        'user.email=test@example.test', 'commit', '--allow-empty', '-qm', 'test'], check=True)

    def refresh(self, **extra):
        return subprocess.run(['bash', str(ROOT / 'deploy/update-docs.sh'), '--build-local'],
                              env={**os.environ, 'REPO_DIR': str(self.repo),
                                   'DOCS_DIR': str(self.target), **extra},
                              capture_output=True, text=True)

    def assert_previous(self):
        self.assertEqual((self.target / 'doc/index.html').read_text(), 'previous')
        self.assertEqual((self.target / 'devdoc/dev/index.html').read_text(), 'previous')
        self.assertEqual((self.target / 'VERSION').read_text(), 'a' * 40 + '\n')

    def test_missing_second_site_preserves_serving_tree(self):
        (self.repo / 'web/public/devdoc/dev/index.html').unlink()
        self.assertNotEqual(self.refresh().returncode, 0)
        self.assert_previous()

    def test_success_retains_both_previous_sites_and_version(self):
        result = self.refresh()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual((self.target / 'doc/index.html').read_text(), 'new')
        backup, = self.target.glob('.previous.*')
        self.assertEqual((backup / 'doc/index.html').read_text(), 'previous')
        self.assertEqual((backup / 'devdoc/dev/index.html').read_text(), 'previous')
        self.assertEqual((backup / 'VERSION').read_text(), 'a' * 40 + '\n')
        self.assertEqual(len((self.target / 'VERSION').read_text().strip()), 40)

    def test_swap_failure_rolls_back_first_site(self):
        # Fail the second install once. All rollback mv calls use the real tool.
        tools = self.root / 'tools'
        tools.mkdir()
        mv = tools / 'mv'
        mv.write_text('''#!/bin/bash
if [[ "$1" == */.update.*/devdoc && ! -f "$FAULT_STAMP" ]]; then
  touch "$FAULT_STAMP"
  exit 1
fi
exec /bin/mv "$@"
''')
        mv.chmod(0o755)
        result = self.refresh(PATH=str(tools) + os.pathsep + os.environ['PATH'],
                              FAULT_STAMP=str(self.root / 'failed'))
        self.assertNotEqual(result.returncode, 0)
        self.assert_previous()


if __name__ == '__main__':
    unittest.main()
