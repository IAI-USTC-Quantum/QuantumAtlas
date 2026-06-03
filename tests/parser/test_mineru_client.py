"""Tests for MinerU error classification.

Mirror Go-side internal/mineru/errors_test.go; the two classifier
implementations must stay byte-identical in behaviour so that daemon-mode
retry/quota handling looks the same regardless of which side hits the API.
"""

from __future__ import annotations

import json
from typing import Any, Dict, List
from unittest.mock import MagicMock

import pytest
import requests

from qatlas.parser.mineru_client import (
    MAX_BATCH_SIZE,
    BatchFile,
    BatchTaskState,
    MinerUClient,
    MinerUDailyLimitError,
    MinerUError,
    MinerUFatalError,
    MinerURetryableError,
    classify_mineru_error,
)


class TestClassifyByCode:
    @pytest.mark.parametrize(
        "code,want",
        [
            ("-60018", MinerUDailyLimitError),
            ("-60019", MinerUDailyLimitError),
            ("-10001", MinerURetryableError),
            ("-60008", MinerURetryableError),
            ("-60022", MinerURetryableError),
            ("A0202", MinerUFatalError),
            ("A0211", MinerUFatalError),
            ("-60006", MinerUFatalError),
            ("-60013", MinerUFatalError),
        ],
    )
    def test_known_codes(self, code: str, want: type) -> None:
        err = classify_mineru_error(code=code, msg="test msg", http_status=0)
        assert isinstance(err, want)
        assert err.code == code

    @pytest.mark.parametrize("code", ["-99999", "ZZZ", "0"])
    def test_unknown_codes_fall_through(self, code: str) -> None:
        err = classify_mineru_error(code=code, msg="???", http_status=0)
        # Plain MinerUError (not one of the typed subclasses) — caller decides.
        assert type(err) is MinerUError


class TestClassifyByHTTPStatus:
    @pytest.mark.parametrize(
        "status,want",
        [
            (429, MinerUDailyLimitError),
            (401, MinerUFatalError),
            (403, MinerUFatalError),
            (408, MinerURetryableError),
            (500, MinerURetryableError),
            (502, MinerURetryableError),
            (504, MinerURetryableError),
        ],
    )
    def test_status_only(self, status: int, want: type) -> None:
        err = classify_mineru_error(http_status=status)
        assert isinstance(err, want)
        assert err.http_status == status

    def test_400_unclassified(self) -> None:
        err = classify_mineru_error(http_status=400, msg="bad request")
        assert type(err) is MinerUError


class TestPrecedence:
    def test_code_beats_status_for_quota(self) -> None:
        # A 200 with a daily-limit code in the envelope must still classify
        # as DailyLimit (this is the most important precedence case for the
        # daemon's "sleep until tomorrow" logic).
        err = classify_mineru_error(code="-60018", msg="gone", http_status=200)
        assert isinstance(err, MinerUDailyLimitError)

    def test_fatal_code_beats_401(self) -> None:
        err = classify_mineru_error(code="-60006", msg="too long", http_status=401)
        assert isinstance(err, MinerUFatalError)
        # Fatal hint should be appended to original msg.
        assert "too long" in err.msg
        assert "200" in err.msg  # the page-limit hint


class TestKeywordScan:
    @pytest.mark.parametrize(
        "msg",
        [
            "daily quota exceeded",
            "please try again tomorrow",
            "you have hit the 5000/day limit",
            "免费额度已用尽",
            "请于次日重试",
            "请明天再试",
            # Canonical Chinese daily-limit phrasing from real MinerU
            # responses — uses 上限 ("upper limit"). The fatal-first
            # classifier protects against 上限 false-firing on per-paper
            # "页数上限" because fatalFreeTextPatterns ("页数超过" etc)
            # short-circuit before this list is consulted.
            "每日解析任务数量已达上限",
        ],
    )
    def test_quota_hints(self, msg: str) -> None:
        err = classify_mineru_error(msg=msg)
        assert isinstance(err, MinerUDailyLimitError)

    @pytest.mark.parametrize("msg", ["something went wrong", "", "unrelated error"])
    def test_non_hints(self, msg: str) -> None:
        err = classify_mineru_error(msg=msg)
        assert type(err) is MinerUError

    @pytest.mark.parametrize(
        "msg",
        [
            # Real EN phrasing observed from MinerU batch-result failures.
            "number of pages exceeds limit (200 pages), please split the file and try again",
            # Variants kept in the pattern table.
            "Number of pages exceeds 200",
            "exceeds the page limit",
            "Please split the file into smaller chunks.",
            "页数超过限制（最多 200 页）",
            "文件大小超出限制（最大 200MB）",
            "file size exceeds 200MB",
        ],
    )
    def test_per_paper_fatal_not_daily_limit(self, msg: str) -> None:
        """Regression: oversize PDF must not trip daily-limit shutdown.

        Before the fatal pre-check, the words 'limit'/'exceed' alone in
        daily-limit keywords classified e.g. 'number of pages exceeds
        limit (200 pages)' as MinerUDailyLimitError, putting the watch
        daemon to sleep for ~20h over a single bad PDF.
        """
        err = classify_mineru_error(msg=msg)
        assert isinstance(err, MinerUFatalError), (
            f"Expected MinerUFatalError for per-paper page/size failure, "
            f"got {type(err).__name__}: {err}"
        )
        # Crucially, must NOT be MinerUDailyLimitError (sibling subclass would
        # still be a Fatal — both inherit from MinerUError — but they're
        # disjoint runtime types).
        assert not isinstance(err, MinerUDailyLimitError)


class TestHintInjection:
    def test_empty_msg_replaced_by_hint(self) -> None:
        err = classify_mineru_error(code="A0202")
        assert isinstance(err, MinerUFatalError)
        assert "Token" in err.msg

    def test_non_empty_msg_keeps_original_and_appends_hint(self) -> None:
        err = classify_mineru_error(code="-60006", msg="user-visible message")
        assert isinstance(err, MinerUFatalError)
        assert "user-visible message" in err.msg
        assert "200 页" in err.msg


class TestExceptionAttributes:
    def test_str_uses_msg(self) -> None:
        err = classify_mineru_error(code="-60018", msg="quota gone", http_status=200)
        assert str(err) == "quota gone"
        assert err.code == "-60018"
        assert err.http_status == 200

    def test_unknown_isolated_from_sentinels(self) -> None:
        # An unclassified error must NOT be an instance of any typed subclass
        # (false positive on this would route a transient bug into "sleep
        # until tomorrow" — catastrophic).
        err = classify_mineru_error(code="-99999", msg="random")
        assert not isinstance(err, MinerUDailyLimitError)
        assert not isinstance(err, MinerUFatalError)
        assert not isinstance(err, MinerURetryableError)


# ---------------------------------------------------------------------------
# Batch API tests (Phase 2b)
# ---------------------------------------------------------------------------


def _fake_response(payload: Dict[str, Any], status: int = 200) -> MagicMock:
    """Build a MagicMock that quacks like requests.Response for our client."""
    resp = MagicMock(spec=requests.Response)
    resp.status_code = status
    resp.text = json.dumps(payload)
    resp.json.return_value = payload
    resp.raise_for_status.return_value = None
    return resp


def _capture_session(client: MinerUClient) -> Dict[str, Any]:
    """Swap client.session for a recording mock; return the call captures."""
    captures: Dict[str, Any] = {}
    session = MagicMock(spec=requests.Session)
    captures["post"] = session.post
    captures["get"] = session.get
    client.session = session
    return captures


class TestSubmitURLBatch:
    def test_happy_path(self) -> None:
        client = MinerUClient("tok")
        caps = _capture_session(client)
        caps["post"].return_value = _fake_response(
            {"code": 0, "msg": "ok", "data": {"batch_id": "batch-9"}}
        )

        batch_id = client.submit_url_batch(
            [
                BatchFile(url="https://example.com/a.pdf", data_id="paper-a"),
                BatchFile(url="https://example.com/b.pdf", data_id="paper-b"),
            ],
            model_version="vlm",
            language="ch",
            enable_formula=True,
            enable_table=True,
        )
        assert batch_id == "batch-9"
        # Verify URL + body shape.
        call_args = caps["post"].call_args
        assert call_args[0][0].endswith("/api/v4/extract/task/batch")
        body = call_args[1]["json"]
        assert body["model_version"] == "vlm"
        assert body["enable_formula"] is True
        assert len(body["files"]) == 2
        assert body["files"][0] == {
            "url": "https://example.com/a.pdf",
            "is_ocr": False,
            "data_id": "paper-a",
        }

    def test_empty_files_rejected(self) -> None:
        client = MinerUClient("tok")
        with pytest.raises(MinerUError, match="no files"):
            client.submit_url_batch([])

    def test_too_many_files_rejected_pre_request(self) -> None:
        client = MinerUClient("tok")
        caps = _capture_session(client)
        files = [BatchFile(url="https://x", data_id=f"p{i}") for i in range(MAX_BATCH_SIZE + 1)]
        with pytest.raises(MinerUError, match=str(MAX_BATCH_SIZE)):
            client.submit_url_batch(files)
        # Must reject before any HTTP round-trip.
        caps["post"].assert_not_called()

    def test_empty_url_rejected(self) -> None:
        client = MinerUClient("tok")
        with pytest.raises(MinerUError, match="empty url"):
            client.submit_url_batch([BatchFile(url="", data_id="p1")])

    def test_missing_batch_id_in_response(self) -> None:
        client = MinerUClient("tok")
        caps = _capture_session(client)
        caps["post"].return_value = _fake_response({"code": 0, "data": {}})
        with pytest.raises(MinerUError, match="batch_id"):
            client.submit_url_batch([BatchFile(url="https://x", data_id="p1")])

    def test_daily_limit_classified(self) -> None:
        client = MinerUClient("tok")
        caps = _capture_session(client)
        caps["post"].return_value = _fake_response(
            {"code": -60018, "msg": "每日解析任务数量已达上限", "data": None}
        )
        with pytest.raises(MinerUDailyLimitError):
            client.submit_url_batch([BatchFile(url="https://x", data_id="p1")])

    def test_data_id_omitted_when_empty(self) -> None:
        client = MinerUClient("tok")
        caps = _capture_session(client)
        caps["post"].return_value = _fake_response({"code": 0, "data": {"batch_id": "b"}})
        client.submit_url_batch([BatchFile(url="https://x")])
        body = caps["post"].call_args[1]["json"]
        assert "data_id" not in body["files"][0]


class TestGetBatch:
    def test_happy_path_with_progress_and_failure(self) -> None:
        client = MinerUClient("tok")
        caps = _capture_session(client)
        caps["get"].return_value = _fake_response(
            {
                "code": 0,
                "data": {
                    "batch_id": "batch-9",
                    "extract_result": [
                        {
                            "file_name": "a.pdf",
                            "data_id": "paper-a",
                            "state": "done",
                            "full_zip_url": "https://z/a.zip",
                        },
                        {
                            "file_name": "b.pdf",
                            "data_id": "paper-b",
                            "state": "running",
                            "extract_progress": {
                                "extracted_pages": 12,
                                "total_pages": 40,
                                "start_time": "2026-05-31 10:00:00",
                            },
                        },
                        {
                            "file_name": "c.pdf",
                            "data_id": "paper-c",
                            "state": "failed",
                            "err_msg": "corrupted pdf",
                        },
                    ],
                },
            }
        )
        results = client.get_batch("batch-9")
        assert caps["get"].call_args[0][0].endswith("/api/v4/extract-results/batch/batch-9")
        assert len(results) == 3
        assert results[0].state == "done"
        assert results[0].full_zip_url == "https://z/a.zip"
        assert results[1].progress.extracted_pages == 12
        assert results[1].progress.total_pages == 40
        assert results[2].state == "failed"
        assert results[2].err_msg == "corrupted pdf"

    def test_null_extract_result_returns_empty(self) -> None:
        client = MinerUClient("tok")
        caps = _capture_session(client)
        caps["get"].return_value = _fake_response(
            {"code": 0, "data": {"batch_id": "b", "extract_result": None}}
        )
        assert client.get_batch("b") == []

    def test_empty_batch_id_rejected(self) -> None:
        client = MinerUClient("tok")
        with pytest.raises(MinerUError, match="empty batch id"):
            client.get_batch("")

    def test_fatal_classified(self) -> None:
        client = MinerUClient("tok")
        caps = _capture_session(client)
        caps["get"].return_value = _fake_response(
            {"code": "A0211", "msg": "token expired", "data": None}
        )
        with pytest.raises(MinerUFatalError):
            client.get_batch("batch-x")
