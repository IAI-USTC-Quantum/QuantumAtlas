"""S3 key parsing + sanity policy tests (no boto/RustFS needed)."""

from __future__ import annotations

import pytest

from qatlas_rag.ingest.s3 import ObjectMeta, policy_for_list_delta


def _mk(key: str) -> ObjectMeta:
    return ObjectMeta(bucket="qatlas-md", key=key, etag="x", last_modified="t", size=1)


def test_key_parse_modern() -> None:
    o = _mk("2401/2401.00001v1.md")
    assert o.yymm == "2401"
    assert o.canonical == "2401.00001"
    assert o.version == 1
    assert o.arxiv_id == "2401.00001v1"


def test_key_parse_legacy() -> None:
    o = _mk("9508/9508027v2.md")
    assert o.yymm == "9508"
    assert o.canonical == "9508027"
    assert o.version == 2
    assert o.arxiv_id == "9508027v2"


def test_key_parse_invalid() -> None:
    bad = _mk("garbage")
    with pytest.raises(ValueError):
        _ = bad.arxiv_id


@pytest.mark.parametrize(
    "delta,expected",
    [
        (0, (True, True)),
        (1, (True, False)),
        (5, (True, False)),
        (-5, (True, False)),
        (6, (False, False)),
        (-100, (False, False)),
    ],
)
def test_policy_for_list_delta(delta: int, expected: tuple[bool, bool]) -> None:
    assert policy_for_list_delta(delta) == expected
