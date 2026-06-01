"""
Wiki Ingester

Handles the ingest workflow: raw sources → wiki pages → Neo4j sync.

Ingestion Pipeline:
1. Fetch: Download paper from arXiv (reuse ArxivFetcher)
2. Parse: Convert PDF to Markdown (reuse PDFParser)
3. Extract: Use LLM to extract structured info (reuse AlgorithmExtractor)
4. Create Wiki Pages: Generate wiki pages from extracted info
5. Sync: Push to Neo4j (if enabled)
"""

import json
import logging
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, List, Optional

from qatlas.paper_assets import (
    normalize_arxiv_identifier,
    resolve_paper_assets,
    wiki_source_page_id,
)

from .page import WikiFrontmatter, WikiPage
from .templates import DOIInfo, PageTemplate

logger = logging.getLogger(__name__)


def _wiki_algorithm_id(value: str) -> str:
    slug = value.replace("_", "-").replace(" ", "-").lower()
    return slug if slug.startswith("algo-") else f"algo-{slug}"


def _wiki_primitive_id(value: str) -> str:
    slug = value.replace("primitive_", "").replace("_", "-").lower()
    return slug if slug.startswith("prim-") else f"prim-{slug}"


def _doi_info_from_metadata(metadata: Dict[str, Any]) -> DOIInfo:
    """Translate raw arXiv metadata into a DOIInfo carrier.

    arXiv self-reported DOIs come from the paper's own claim in the
    abstract page; we trust it as `confidence=high, source=arxiv`. When
    arXiv has no DOI on file we still emit a marker (``unresolved``) so
    ``qatlas wiki enrich-doi`` can tell pages that need re-checking from
    pages that were never wired up.
    """
    doi = metadata.get("doi")
    if doi:
        return DOIInfo.from_arxiv_self(str(doi).strip())
    return DOIInfo.unresolved()


class WikiIngester:
    """
    Handles ingestion of external sources into the wiki.

    The ingester orchestrates the flow from raw sources to wiki pages,
    reusing existing components (ArxivFetcher, PDFParser, AlgorithmExtractor).

    Usage:
        engine = WikiEngine()
        result = engine.ingester.ingest_paper("quant-ph/9508027")
    """

    def __init__(self, wiki_engine):
        """
        Initialize ingester.

        Args:
            wiki_engine: Parent WikiEngine instance
        """
        self.engine = wiki_engine

        # Lazy-loaded components
        self._arxiv_fetcher = None
        self._extractor = None

    @property
    def arxiv_fetcher(self):
        """Lazy initialization of ArxivFetcher."""
        if self._arxiv_fetcher is None:
            from qatlas.parser.arxiv_fetcher import ArxivFetcher

            pdf_dir = self.engine.get_paper_asset_dir("pdf")
            self._arxiv_fetcher = ArxivFetcher(output_dir=str(pdf_dir))
        return self._arxiv_fetcher

    @property
    def extractor(self):
        """Lazy initialization of AlgorithmExtractor."""
        if self._extractor is None:
            from qatlas.extractor.extractor import create_extractor

            # Default to OpenAI - can be made configurable
            self._extractor = create_extractor("openai")
        return self._extractor

    def ingest_paper(
        self,
        arxiv_id: str,
        fetch: bool = True,
        parse: bool = False,
        extract: bool = True,
        create_wiki: bool = True,
        llm_provider: str = "openai",
    ) -> Dict[str, Any]:
        """
        Full ingestion pipeline for an arXiv paper.

        Args:
            arxiv_id: arXiv paper ID
            fetch: Whether to fetch the PDF
            parse: Whether to parse PDF to markdown
            extract: Whether to extract algorithm info via LLM
            create_wiki: Whether to create wiki pages
            llm_provider: LLM provider for extraction ("openai" or "anthropic")

        Returns:
            Dict with ingestion results and file paths
        """
        result = {
            "arxiv_id": arxiv_id,
            "status": "pending",
            "steps": {},
            "wiki_pages": [],
            "errors": [],
        }

        try:
            # Step 1: Fetch paper
            if fetch:
                logger.info(f"Fetching paper {arxiv_id}...")
                pdf_path, metadata = self._fetch_paper(arxiv_id)
                result["steps"]["fetch"] = {
                    "pdf_path": str(pdf_path),
                    "metadata": metadata,
                }

            # Step 2: Parse PDF
            if parse:
                logger.info(f"Parsing PDF for {arxiv_id}...")
                markdown_path = self._parse_pdf(
                    arxiv_id,
                    metadata if fetch else None,
                    pdf_path=pdf_path if fetch else None,
                )
                result["steps"]["parse"] = {"markdown_path": str(markdown_path)}

            # Step 3: Extract algorithm info
            if extract:
                logger.info(f"Extracting algorithm info for {arxiv_id}...")
                algorithm_ir = self._extract_algorithm(arxiv_id, llm_provider)
                result["steps"]["extract"] = {
                    "algorithm_id": algorithm_ir.id,
                    "algorithm_name": algorithm_ir.name,
                    "primitives": algorithm_ir.primitives,
                }

            # Step 4: Create wiki pages
            if create_wiki:
                logger.info(f"Creating wiki pages for {arxiv_id}...")
                wiki_pages = self._create_wiki_pages(arxiv_id, result)
                result["wiki_pages"] = [p.frontmatter.id for p in wiki_pages]
                result["steps"]["wiki"] = {"pages_created": len(wiki_pages)}

            # Update index and log
            self.engine.update_index()
            self.engine.append_to_log(
                f"[INGEST] {arxiv_id}: Created {len(result.get('wiki_pages', []))} pages"
            )

            result["status"] = "success"

        except Exception as e:
            result["status"] = "error"
            result["errors"].append(str(e))
            logger.error(f"Ingestion failed for {arxiv_id}: {e}")
            self.engine.append_to_log(f"[ERROR] {arxiv_id}: {str(e)}")

        return result

    def _fetch_paper(self, arxiv_id: str) -> tuple:
        """Fetch paper from arXiv."""
        canonical = normalize_arxiv_identifier(arxiv_id)
        pdf_path, metadata = self.arxiv_fetcher.fetch(canonical)
        asset_id = normalize_arxiv_identifier(metadata.get("arxiv_id", canonical))

        # Save metadata to JSON
        json_path = self.engine.get_paper_asset_path("json", asset_id)
        json_path.parent.mkdir(parents=True, exist_ok=True)
        json_path.write_text(json.dumps(metadata, indent=2), encoding="utf-8")

        return pdf_path, metadata

    def _resolve_asset_path(
        self,
        kind: str,
        arxiv_id: str,
        metadata: Optional[Dict[str, Any]] = None,
    ) -> Path:
        """Resolve an asset path, preferring the exact version returned by arXiv."""
        if metadata and metadata.get("arxiv_id"):
            return self.engine.get_paper_asset_path(kind, metadata["arxiv_id"])

        resolved = resolve_paper_assets(self.engine.raw_dir, arxiv_id)
        existing = resolved.get(f"{kind}_path")
        if isinstance(existing, Path):
            return existing

        return self.engine.get_paper_asset_path(kind, normalize_arxiv_identifier(arxiv_id))

    def _parse_pdf(
        self,
        arxiv_id: str,
        metadata: Optional[Dict] = None,
        pdf_path: Optional[Path | str] = None,
    ) -> Path:
        """Local PDF parsing has been removed from the open-source build.

        The previous implementation used a third-party local PDF library; the
        only supported path now is to upload an existing markdown (e.g. one
        produced by ``qatlas mineru``) via ``qatlas upload markdown``.
        """
        raise NotImplementedError(
            "Local PDF parsing has been removed. "
            "Run `qatlas mineru <arxiv_id>` to obtain markdown, then call "
            "`qatlas upload markdown <arxiv_id>v<n> --markdown <path>`."
        )

    def _extract_algorithm(self, arxiv_id: str, llm_provider: str = "openai") -> Any:
        """Extract algorithm info using LLM."""
        markdown_path = self._resolve_asset_path("markdown", arxiv_id)

        if not markdown_path.exists():
            raise FileNotFoundError(f"Markdown not found: {markdown_path}")

        paper_text = markdown_path.read_text(encoding="utf-8")

        # Create extractor with specified provider
        from qatlas.extractor.extractor import create_extractor

        extractor = create_extractor(llm_provider)

        return extractor.extract_from_paper(arxiv_id, paper_text)

    def _create_wiki_pages(
        self,
        arxiv_id: str,
        ingest_result: Dict[str, Any],
    ) -> List[WikiPage]:
        """Create wiki pages from ingestion results."""
        pages = []

        # Create source paper page
        if "fetch" in ingest_result["steps"]:
            metadata = ingest_result["steps"]["fetch"]["metadata"]
            algorithms = []

            if "extract" in ingest_result["steps"]:
                extract_info = ingest_result["steps"]["extract"]
                algo_id = _wiki_algorithm_id(
                    extract_info.get("algorithm_id")
                    or extract_info.get("algorithm_name", "unknown")
                )
                algorithms.append(algo_id)

            paper_page = PageTemplate.source_paper(
                arxiv_id=arxiv_id,
                title=metadata.get("title", "Unknown Title"),
                authors=metadata.get("authors", []),
                abstract=metadata.get("abstract", ""),
                algorithms=algorithms,
                published=metadata.get("published"),
                doi=_doi_info_from_metadata(metadata),
                categories=metadata.get("categories"),
            )
            paper_page.frontmatter.status = "published"
            self.engine.save_page(paper_page, subdir="sources/papers")
            pages.append(paper_page)

        # Create algorithm entity page
        if "extract" in ingest_result["steps"]:
            extract_info = ingest_result["steps"]["extract"]

            # Determine algorithm ID
            algo_name = extract_info.get("algorithm_name", "unknown")
            algo_id = _wiki_algorithm_id(extract_info.get("algorithm_id") or algo_name)

            # Map primitive IDs to wiki format
            primitive_ids = [_wiki_primitive_id(p) for p in extract_info.get("primitives", [])]

            # Get complexity info if available
            complexity = extract_info.get("complexity") or {}
            if "algorithm_ir" in extract_info:
                ir = extract_info["algorithm_ir"]
                if hasattr(ir, "complexity"):
                    complexity = {
                        "time": getattr(ir.complexity, "time", "Unknown"),
                        "space": getattr(ir.complexity, "space", "Unknown"),
                        "gates": getattr(ir.complexity, "gate_count", "Unknown"),
                        "depth": getattr(ir.complexity, "circuit_depth", "Unknown"),
                        "qubits": getattr(ir.complexity, "qubit_count", "Unknown"),
                    }
                elif isinstance(ir, dict) and isinstance(ir.get("complexity"), dict):
                    ir_complexity = ir["complexity"]
                    complexity = {
                        "time": ir_complexity.get("time", "Unknown"),
                        "space": ir_complexity.get("space", "Unknown"),
                        "gates": ir_complexity.get("gate_count", "Unknown"),
                        "depth": ir_complexity.get("circuit_depth", "Unknown"),
                        "qubits": ir_complexity.get("qubit_count", "Unknown"),
                    }

            algo_page = PageTemplate.algorithm_entity(
                id=algo_id,
                name=extract_info.get("algorithm_name", algo_name),
                problem=extract_info.get("problem_type")
                or extract_info.get("problem")
                or "Unknown",
                description=extract_info.get("description")
                or f"Algorithm extracted from arXiv:{arxiv_id}",
                primitives=primitive_ids,
                paper_id=wiki_source_page_id(arxiv_id),
                complexity=complexity,
                pseudocode=extract_info.get("pseudocode"),
            )
            self.engine.save_page(algo_page, subdir="entities/algorithms")
            pages.append(algo_page)

        return pages

    def ingest_from_existing(self, arxiv_id: str) -> Dict[str, Any]:
        """
        Ingest from existing files without re-fetching.

        Creates wiki pages from existing raw files (PDF, markdown, JSON).

        Args:
            arxiv_id: arXiv paper ID

        Returns:
            Dict with ingestion results
        """
        result = {
            "arxiv_id": arxiv_id,
            "status": "pending",
            "wiki_pages": [],
            "errors": [],
        }

        # Check for existing files
        resolved = resolve_paper_assets(self.engine.raw_dir, arxiv_id)
        json_path = resolved["json_path"]
        markdown_path = resolved["markdown_path"]

        if not isinstance(json_path, Path) and not isinstance(markdown_path, Path):
            result["status"] = "error"
            result["errors"].append(f"No existing files found for {arxiv_id}")
            return result

        try:
            # Load metadata from JSON if exists
            metadata = {}
            if isinstance(json_path, Path) and json_path.exists():
                metadata = json.loads(json_path.read_text(encoding="utf-8"))

            # Create wiki pages
            paper_page = PageTemplate.source_paper(
                arxiv_id=arxiv_id,
                title=metadata.get("title", f"Paper {arxiv_id}"),
                authors=metadata.get("authors", []),
                abstract=metadata.get("abstract", ""),
                published=metadata.get("published"),
                doi=_doi_info_from_metadata(metadata),
                categories=metadata.get("categories"),
            )
            paper_page.frontmatter.status = "published"
            self.engine.save_page(paper_page, subdir="sources/papers")
            pages = [paper_page]

            result["wiki_pages"] = [p.frontmatter.id for p in pages]
            result["status"] = "success"

            self.engine.append_to_log(f"[MIGRATE] {arxiv_id}: Created {len(pages)} pages")

        except Exception as e:
            result["status"] = "error"
            result["errors"].append(str(e))

        return result
