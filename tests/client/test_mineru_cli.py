"""Tests for the `qatlas contrib mineru` CLI batch / daily-limit handling.

These focus on the new (v0.15.0) queue-drain flow that submits a single
MinerU batch per pass and back-offs cleanly when the daily quota is
exhausted. The unit tests stub out HTTP entirely (claim / list /
release / upload-mineru endpoints) and the MinerUClient (submit_url_batch
/ get_batch / download_full_zip) — we're testing the orchestration
state machine, not the wire format (that's covered in
test_mineru_client.py).
"""

from __future__ import annotations

import argparse
from pathlib import Path
from typing import Any, Dict, List
from unittest.mock import MagicMock, patch

import pytest

from qatlas.client import mineru as cli
from qatlas.parser.mineru_client import (
    BatchTaskState,
    MinerUDailyLimitError,
    MinerUFatalError,
)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_args(**overrides: Any) -> argparse.Namespace:
    """Build a Namespace with every attribute _drain_queue_once touches."""
    defaults: Dict[str, Any] = {
        "arxiv_id": None,
        "batch_size": 50,
        "max_alias": None,
        "continue_on_error": True,
        "ttl_seconds": None,
        "no_cache": False,
        "overwrite": False,
        "no_push": False,
        "watch": False,
        "watch_interval": 1,
        "request_timeout": 5.0,
        "insecure": False,
        "server": None,
        "token": "test-token",
    }
    defaults.update(overrides)
    return argparse.Namespace(**defaults)


def _make_config() -> Any:
    """Minimal ServerConfig stand-in with the fields _drain_queue_once reads."""
    cfg = MagicMock()
    cfg.mineru_api_token = "mineru-tok"
    cfg.mineru_api_base_url = "https://mineru.example.com"
    cfg.mineru_model_version = "vlm"
    cfg.mineru_language = "ch"
    cfg.mineru_enable_formula = True
    cfg.mineru_enable_table = True
    cfg.mineru_is_ocr = False
    cfg.mineru_poll_interval = 0.01  # fast tests
    cfg.mineru_timeout = 60
    return cfg


def _arxiv_url(arxiv_id: str) -> str:
    return f"https://arxiv.org/pdf/{arxiv_id}"


def _claim_response(arxiv_id: str, claim_id: str = "c1") -> Dict[str, Any]:
    return {
        "claim_id": claim_id,
        "pdf_url": _arxiv_url(arxiv_id),
        "pdf_sha256": "",  # legacy: skip hash verification
    }


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestSecondsUntilNextDaily:
    def test_in_future(self) -> None:
        s = cli._seconds_until_next_daily_run()
        assert 60.0 <= s <= 24 * 3600 + 120


class TestPrepareBatchJobs:
    def test_skips_non_arxiv_url(self) -> None:
        args = _make_args()
        with patch.object(cli, "_claim_one") as claim, \
             patch.object(cli, "_release_claim") as release:
            claim.return_value = (
                {"claim_id": "c1", "pdf_url": "https://evil.example.com/x.pdf"},
                None,
            )
            jobs, failures = cli._prepare_batch_jobs(
                args, "http://server", [{"arxiv_id": "2501.0001v1"}], True, {}
            )
        assert jobs == []
        assert failures == 1
        release.assert_called_once()

    def test_skip_reason_from_409_not_counted_as_failure(self) -> None:
        args = _make_args()
        with patch.object(cli, "_claim_one") as claim:
            claim.return_value = (None, "skip (HTTP 409): already claimed")
            jobs, failures = cli._prepare_batch_jobs(
                args, "http://server", [{"arxiv_id": "2501.0001v1"}], True, {}
            )
        assert jobs == []
        assert failures == 0

    def test_transport_error_counts_as_failure(self) -> None:
        args = _make_args()
        with patch.object(cli, "_claim_one") as claim:
            claim.return_value = (None, "claim request errored: connection refused")
            jobs, failures = cli._prepare_batch_jobs(
                args, "http://server", [{"arxiv_id": "2501.0001v1"}], True, {}
            )
        assert jobs == []
        assert failures == 1

    def test_happy_path_produces_one_job_per_candidate(self) -> None:
        args = _make_args()
        candidates = [{"arxiv_id": f"2501.000{i}v1"} for i in range(3)]
        with patch.object(cli, "_claim_one") as claim, \
             patch.object(cli, "_release_claim"):
            claim.side_effect = [
                (_claim_response(c["arxiv_id"], f"claim-{i}"), None)
                for i, c in enumerate(candidates)
            ]
            jobs, failures = cli._prepare_batch_jobs(
                args, "http://server", candidates, True, {}
            )
        assert failures == 0
        assert [j.arxiv_id for j in jobs] == [c["arxiv_id"] for c in candidates]
        assert all(j.workdir.exists() for j in jobs)
        cli._cleanup_workdirs(jobs)
        assert all(not j.workdir.exists() for j in jobs)


def _mock_needs_mineru(candidates: List[Dict[str, Any]]) -> MagicMock:
    resp = MagicMock()
    resp.ok = True
    resp.json.return_value = {
        "papers": candidates,
        "total_unclaimed": len(candidates),
        "total_claimed": 0,
    }
    return resp


@pytest.fixture(autouse=True)
def _skip_version_check() -> Any:
    """check_response_version expects a real Response with .headers; bypass."""
    with patch("qatlas.client.mineru.check_response_version"):
        yield


class TestDrainQueueDailyLimit:
    """Daily-limit detection in submit + poll + per-entry failure paths."""

    def test_submit_raises_daily_limit_releases_all(self) -> None:
        args = _make_args(batch_size=2)
        config = _make_config()
        candidates = [{"arxiv_id": f"id{i}v1"} for i in range(2)]

        with patch("qatlas.client.mineru.requests.get") as req_get, \
             patch.object(cli, "_claim_one") as claim, \
             patch.object(cli, "_release_claim") as release, \
             patch("qatlas.client.mineru.MinerUClient") as client_cls:
            req_get.return_value = _mock_needs_mineru(candidates)
            claim.side_effect = [
                (_claim_response(c["arxiv_id"], f"c{i}"), None)
                for i, c in enumerate(candidates)
            ]
            mineru = client_cls.return_value
            mineru.submit_url_batch.side_effect = MinerUDailyLimitError(
                "quota exhausted", code="-60018"
            )
            outcome = cli._drain_queue_once(args, "http://server", config, True, {})

        assert outcome.daily_limit_hit is True
        assert outcome.failures == 2
        assert release.call_count == 2
        mineru.get_batch.assert_not_called()

    def test_submit_fatal_releases_all_but_not_daily_limit(self) -> None:
        args = _make_args(batch_size=2)
        config = _make_config()
        candidates = [{"arxiv_id": "id0v1"}]

        with patch("qatlas.client.mineru.requests.get") as req_get, \
             patch.object(cli, "_claim_one") as claim, \
             patch.object(cli, "_release_claim") as release, \
             patch("qatlas.client.mineru.MinerUClient") as client_cls:
            req_get.return_value = _mock_needs_mineru(candidates)
            claim.return_value = (_claim_response("id0v1"), None)
            mineru = client_cls.return_value
            mineru.submit_url_batch.side_effect = MinerUFatalError(
                "bad token", code="A0202"
            )
            outcome = cli._drain_queue_once(args, "http://server", config, True, {})

        assert outcome.daily_limit_hit is False
        assert outcome.failures == 1
        release.assert_called()

    def test_poll_classifies_per_entry_daily_limit(self) -> None:
        """Mid-batch: one entry's err_msg signals quota; we abort + release rest."""
        args = _make_args(batch_size=3)
        config = _make_config()
        candidates = [{"arxiv_id": f"id{i}v1"} for i in range(3)]

        with patch("qatlas.client.mineru.requests.get") as req_get, \
             patch.object(cli, "_claim_one") as claim, \
             patch.object(cli, "_release_claim") as release, \
             patch.object(cli, "_finalise_done_entry", return_value=True), \
             patch("qatlas.client.mineru.MinerUClient") as client_cls:
            req_get.return_value = _mock_needs_mineru(candidates)
            claim.side_effect = [
                (_claim_response(c["arxiv_id"], f"c{i}"), None)
                for i, c in enumerate(candidates)
            ]
            mineru = client_cls.return_value
            mineru.submit_url_batch.return_value = "batch-9"
            mineru.get_batch.return_value = [
                BatchTaskState(
                    file_name="a.pdf", data_id="id0v1", state="done",
                    full_zip_url="https://z/a.zip",
                ),
                BatchTaskState(
                    file_name="b.pdf", data_id="id1v1", state="failed",
                    err_msg="每日解析任务数量已达上限",
                ),
                BatchTaskState(
                    file_name="c.pdf", data_id="id2v1", state="running",
                ),
            ]
            outcome = cli._drain_queue_once(args, "http://server", config, True, {})

        assert outcome.daily_limit_hit is True
        # processed counts id0 (done) + id1 (failed); failures = id1 + id2 (released).
        assert outcome.failures == 2
        assert release.call_count == 3

    def test_get_batch_raises_daily_limit_releases_remaining(self) -> None:
        args = _make_args(batch_size=2)
        config = _make_config()
        candidates = [{"arxiv_id": f"id{i}v1"} for i in range(2)]

        with patch("qatlas.client.mineru.requests.get") as req_get, \
             patch.object(cli, "_claim_one") as claim, \
             patch.object(cli, "_release_claim") as release, \
             patch("qatlas.client.mineru.MinerUClient") as client_cls:
            req_get.return_value = _mock_needs_mineru(candidates)
            claim.side_effect = [
                (_claim_response(c["arxiv_id"], f"c{i}"), None)
                for i, c in enumerate(candidates)
            ]
            mineru = client_cls.return_value
            mineru.submit_url_batch.return_value = "batch-X"
            mineru.get_batch.side_effect = MinerUDailyLimitError(
                "quota gone", http_status=429
            )
            outcome = cli._drain_queue_once(args, "http://server", config, True, {})

        assert outcome.daily_limit_hit is True
        assert outcome.failures == 2
        assert release.call_count == 2

    def test_per_paper_fatal_skips_release_to_avoid_queue_poison(self) -> None:
        """Regression: oversize PDF (per-paper fatal) must NOT release the
        server-side claim immediately.

        Releasing it would put the bad PDF straight back at the top of
        ``needs-mineru`` (ORDER BY pdf_uploaded_at DESC). Keeping the
        30-minute claim lease lets the lease expire naturally and bounds
        per-day retries on the same poison PDF to ~48. Pair test with
        ``test_poll_classifies_per_entry_daily_limit`` which DOES release
        on daily-limit because that's a global signal, not per-paper.
        """
        args = _make_args(batch_size=2)
        config = _make_config()
        candidates = [{"arxiv_id": "ok1v1"}, {"arxiv_id": "badv1"}]

        with patch("qatlas.client.mineru.requests.get") as req_get, \
             patch.object(cli, "_claim_one") as claim, \
             patch.object(cli, "_release_claim") as release, \
             patch.object(cli, "_finalise_done_entry", return_value=True), \
             patch("qatlas.client.mineru.MinerUClient") as client_cls:
            req_get.return_value = _mock_needs_mineru(candidates)
            claim.side_effect = [
                (_claim_response(c["arxiv_id"], f"c{i}"), None)
                for i, c in enumerate(candidates)
            ]
            mineru = client_cls.return_value
            mineru.submit_url_batch.return_value = "batch-FATAL"
            mineru.get_batch.return_value = [
                BatchTaskState(
                    file_name="ok1.pdf", data_id="ok1v1", state="done",
                    full_zip_url="https://z/ok1.zip",
                ),
                BatchTaskState(
                    file_name="bad.pdf", data_id="badv1", state="failed",
                    err_msg="number of pages exceeds limit (200 pages), please split the file and try again",
                ),
            ]
            outcome = cli._drain_queue_once(args, "http://server", config, True, {})

        # No daily-limit shutdown (the bug fix being asserted).
        assert outcome.daily_limit_hit is False
        # 1 done + 1 fatal = 1 failure recorded.
        assert outcome.failures == 1
        assert outcome.processed == 2
        # Crucially: _release_claim called only for the done paper (ok1v1),
        # NOT for the fatal paper (badv1). Keeping the 30-min lease on
        # badv1 prevents immediate re-queue (queue-poison mitigation).
        released_ids = {call.args[1] for call in release.call_args_list}
        assert "ok1v1" in released_ids
        assert "badv1" not in released_ids, (
            f"Per-paper fatal must NOT release its claim "
            f"(release was called with: {released_ids})"
        )


class TestDrainQueueHappy:
    def test_all_done_no_failures(self) -> None:
        args = _make_args(batch_size=2)
        config = _make_config()
        candidates = [{"arxiv_id": f"id{i}v1"} for i in range(2)]

        with patch("qatlas.client.mineru.requests.get") as req_get, \
             patch.object(cli, "_claim_one") as claim, \
             patch.object(cli, "_release_claim") as release, \
             patch.object(cli, "_finalise_done_entry", return_value=True), \
             patch("qatlas.client.mineru.MinerUClient") as client_cls:
            req_get.return_value = _mock_needs_mineru(candidates)
            claim.side_effect = [
                (_claim_response(c["arxiv_id"], f"c{i}"), None)
                for i, c in enumerate(candidates)
            ]
            mineru = client_cls.return_value
            mineru.submit_url_batch.return_value = "batch-OK"
            mineru.get_batch.return_value = [
                BatchTaskState(file_name=f"{c['arxiv_id']}.pdf",
                               data_id=c["arxiv_id"],
                               state="done",
                               full_zip_url=f"https://z/{c['arxiv_id']}.zip")
                for c in candidates
            ]
            outcome = cli._drain_queue_once(args, "http://server", config, True, {})

        assert outcome.daily_limit_hit is False
        assert outcome.failures == 0
        assert outcome.processed == 2
        assert release.call_count == 2

    def test_empty_queue_returns_zero(self) -> None:
        args = _make_args()
        config = _make_config()
        with patch("qatlas.client.mineru.requests.get") as req_get:
            req_get.return_value.ok = True
            req_get.return_value.json.return_value = {
                "papers": [], "total_unclaimed": 0, "total_claimed": 0,
            }
            outcome = cli._drain_queue_once(args, "http://server", config, True, {})
        assert outcome == cli._BatchOutcome(
            processed=0, failures=0, daily_limit_hit=False
        )


class TestCmdMineruExitCodes:
    """Exit code contract: success=0, hard error=1, daily-limit=EX_TEMPFAIL."""

    def test_daily_limit_returns_ex_tempfail(self) -> None:
        args = _make_args(arxiv_id=None, watch=False, batch_size=1)
        config = _make_config()
        with patch.object(cli.ServerConfig, "from_env", return_value=config), \
             patch.object(cli, "base_url_from_args", return_value="http://server"), \
             patch.object(cli, "request_verify", return_value=True), \
             patch.object(cli, "auth_headers", return_value={}), \
             patch.object(cli, "_drain_queue_once") as drain:
            drain.return_value = cli._BatchOutcome(
                processed=0, failures=1, daily_limit_hit=True
            )
            rc = cli.cmd_mineru(args)
        assert rc == cli.EXIT_DAILY_LIMIT

    def test_failures_returns_1(self) -> None:
        args = _make_args(arxiv_id=None, watch=False, batch_size=1)
        config = _make_config()
        with patch.object(cli.ServerConfig, "from_env", return_value=config), \
             patch.object(cli, "base_url_from_args", return_value="http://server"), \
             patch.object(cli, "request_verify", return_value=True), \
             patch.object(cli, "auth_headers", return_value={}), \
             patch.object(cli, "_drain_queue_once") as drain:
            drain.return_value = cli._BatchOutcome(
                processed=2, failures=1, daily_limit_hit=False
            )
            rc = cli.cmd_mineru(args)
        assert rc == 1

    def test_success_returns_0(self) -> None:
        args = _make_args(arxiv_id=None, watch=False, batch_size=1)
        config = _make_config()
        with patch.object(cli.ServerConfig, "from_env", return_value=config), \
             patch.object(cli, "base_url_from_args", return_value="http://server"), \
             patch.object(cli, "request_verify", return_value=True), \
             patch.object(cli, "auth_headers", return_value={}), \
             patch.object(cli, "_drain_queue_once") as drain:
            drain.return_value = cli._BatchOutcome(
                processed=2, failures=0, daily_limit_hit=False
            )
            rc = cli.cmd_mineru(args)
        assert rc == 0


class TestBatchSizeAlias:
    def test_max_alias_propagates_when_batch_size_default(self) -> None:
        parser = cli.build_parser()
        args = parser.parse_args(["--max", "7"])
        if args.max_alias is not None and args.batch_size == cli.MAX_BATCH_SIZE:
            args.batch_size = args.max_alias
        assert args.batch_size == 7

    def test_batch_size_wins_when_both_given(self) -> None:
        parser = cli.build_parser()
        args = parser.parse_args(["--max", "7", "--batch-size", "3"])
        if args.max_alias is not None and args.batch_size == cli.MAX_BATCH_SIZE:
            args.batch_size = args.max_alias
        assert args.batch_size == 3

    def test_batch_size_capped_to_max(self) -> None:
        # --batch-size 200 is accepted at parse time but clamped in _drain_queue_once.
        args = _make_args(batch_size=200)
        config = _make_config()
        with patch("qatlas.client.mineru.requests.get") as req_get:
            req_get.return_value.ok = True
            req_get.return_value.json.return_value = {
                "papers": [], "total_unclaimed": 0, "total_claimed": 0,
            }
            cli._drain_queue_once(args, "http://server", config, True, {})
            call = req_get.call_args
            assert call[1]["params"]["limit"] == cli.MAX_BATCH_SIZE


class TestFinaliseDoneEntry:
    def test_no_full_zip_url_is_failure(self, tmp_path: Path) -> None:
        args = _make_args()
        mineru = MagicMock()
        job = cli._BatchJob(
            arxiv_id="a", claim_id="c", pdf_url="x",
            server_pdf_sha256=None, client_pdf_sha256=None, workdir=tmp_path,
        )
        entry = BatchTaskState(data_id="a", state="done", full_zip_url="")
        assert cli._finalise_done_entry(args, "http://s", mineru, job, entry, True, {}) is False

    def test_no_push_skips_download(self, tmp_path: Path) -> None:
        args = _make_args(no_push=True)
        mineru = MagicMock()
        job = cli._BatchJob(
            arxiv_id="a", claim_id="c", pdf_url="x",
            server_pdf_sha256=None, client_pdf_sha256=None, workdir=tmp_path,
        )
        entry = BatchTaskState(data_id="a", state="done", full_zip_url="https://z/a.zip")
        assert cli._finalise_done_entry(args, "http://s", mineru, job, entry, True, {}) is True
        mineru.download_full_zip.assert_not_called()


class TestIsAcceptablePDFURL:
    """Whitelist for the URL the claim handler hands us — refuse anything
    not arxiv / our edge / our S3 public endpoint, so a hostile or
    misconfigured server can't make this CLI fetch attacker.com."""

    @pytest.mark.parametrize(
        "url,server,want",
        [
            # arxiv (always accepted regardless of edge)
            ("https://arxiv.org/pdf/2401.0001v1", "https://quantum-atlas.ai", True),
            ("https://export.arxiv.org/pdf/2401.0001v1", "https://quantum-atlas.ai", True),

            # IP-based edge: dedicated raw.* subdomain in front of the bucket
            ("https://raw.quantum-atlas.ai/qatlas-pdf/0207/0207065v3.pdf?X-Amz-Signature=ABC",
             "https://quantum-atlas.ai", True),

            # Any *.quantum-atlas.ai subdomain (suffix match)
            ("https://other.quantum-atlas.ai/anything", "https://quantum-atlas.ai", True),

            # Main edge host itself matches (same-host rule)
            ("https://quantum-atlas.ai/qatlas-pdf/x.pdf", "https://quantum-atlas.ai", True),

            # IP+port edge: API and S3 public endpoint share one IP (same-host rule)
            ("https://203.0.113.10:9000/qatlas-pdf/x.pdf", "https://203.0.113.10", True),

            # Rejections
            ("https://evil.example.com/anything", "https://quantum-atlas.ai", False),
            ("http://attacker.com/x.pdf", "https://quantum-atlas.ai", False),
            ("https://attacker-quantum-atlas.ai/x.pdf",  # not a suffix match, prefix-spoofed
             "https://quantum-atlas.ai", False),
            ("not-a-url", "https://quantum-atlas.ai", False),
            ("", "https://quantum-atlas.ai", False),
        ],
    )
    def test_cases(self, url: str, server: str, want: bool) -> None:
        assert cli._is_acceptable_pdf_url(url, server) is want

    def test_attacker_cannot_spoof_subdomain(self) -> None:
        # Sanity: somebody.evil-quantum-atlas.ai must NOT match — suffix
        # check is on ".quantum-atlas.ai" with a leading dot precisely to
        # prevent this attack.
        assert cli._is_acceptable_pdf_url(
            "https://evil-quantum-atlas.ai/x.pdf", "https://quantum-atlas.ai"
        ) is False
