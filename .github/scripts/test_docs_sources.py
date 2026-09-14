"""Offline checks for the documentation source lock."""
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import docs_sources


class DocsSourceTests(unittest.TestCase):
    def test_lock_requires_three_full_shas(self):
        entries = docs_sources.load_lock()
        self.assertEqual([item["name"] for item in entries], list(docs_sources.NAMES))
        for item in entries:
            self.assertEqual(len(item["sha"]), 40)
            self.assertTrue(all(c in "0123456789abcdef" for c in item["sha"]))

    def test_rejects_branch_names(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "components.lock.json"
            path.write_text(json.dumps({
                "schema": 1,
                "components": [
                    {"name": "qatlas-cli", "repository": "IAI-USTC-Quantum/qatlas-cli", "sha": "main", "docs_dir": "docs"},
                    {"name": "qatlas-search", "repository": "IAI-USTC-Quantum/qatlas-search", "sha": "a" * 40, "docs_dir": "docs"},
                    {"name": "qatlas-rag", "repository": "IAI-USTC-Quantum/qatlas-rag", "sha": "b" * 40, "docs_dir": "docs"},
                ],
            }))
            with self.assertRaisesRegex(ValueError, "full immutable"):
                docs_sources.load_lock(path)

    def test_component_root_must_stay_under_build(self):
        with patch.dict("os.environ", {"QATLAS_DOCS_COMPONENTS": str(docs_sources.ROOT / "docs")}):
            with self.assertRaisesRegex(ValueError, "build/"):
                docs_sources.component_root()


if __name__ == "__main__":
    unittest.main()
