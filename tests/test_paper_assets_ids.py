import pytest

from atlas.paper_assets import safe_paper_key, wiki_source_page_id


@pytest.mark.parametrize(
    ("arxiv_id", "expected_page_id", "expected_key"),
    [
        (
            "quant-ph/9508027v1",
            "paper-arxiv-quant-ph-9508027v1",
            "quant-ph__9508027v1",
        ),
        (
            "2401.00001v1",
            "paper-arxiv-2401.00001v1",
            "2401.00001v1",
        ),
    ],
)
def test_versioned_arxiv_ids_preserve_version_in_page_ids_and_asset_keys(
    arxiv_id, expected_page_id, expected_key
):
    assert wiki_source_page_id(arxiv_id) == expected_page_id
    assert safe_paper_key(arxiv_id) == expected_key
