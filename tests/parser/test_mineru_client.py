"""Tests for MinerU error classification.

Mirror Go-side internal/mineru/errors_test.go; the two classifier
implementations must stay byte-identical in behaviour so that daemon-mode
retry/quota handling looks the same regardless of which side hits the API.
"""

from __future__ import annotations

import pytest

from qatlas.parser.mineru_client import (
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
        ],
    )
    def test_quota_hints(self, msg: str) -> None:
        err = classify_mineru_error(msg=msg)
        assert isinstance(err, MinerUDailyLimitError)

    @pytest.mark.parametrize("msg", ["something went wrong", "", "unrelated error"])
    def test_non_hints(self, msg: str) -> None:
        err = classify_mineru_error(msg=msg)
        assert type(err) is MinerUError


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
