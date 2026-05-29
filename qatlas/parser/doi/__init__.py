"""DOI resolution: find the formally published DOI for an arXiv paper.

The actual matching strategies live in submodules. This package only owns
the data contract (`PaperContext` / `DOIMatch` / `DOIResolver`) plus a small
helper for normalizing titles, so users can plug in third-party tools
behind the same interface as Crossref / OpenAlex.

Why bare-DOI strings? The frontmatter `doi` field stores the canonical DOI
without scheme/host (e.g. `10.1103/PhysRevLett.103.150502`). Templates,
Neo4j sync, and external integrations are free to rewrite `https://doi.org/{doi}`
themselves.

Resolvers are intentionally strict: we only auto-fill DOI when title +
author cross-check pass. Fuzzy / Levenshtein matching is explicitly out of
scope; ambiguous results return None and the user can supply DOIs manually.
"""

from .protocol import DOIMatch, DOIResolver, PaperContext, normalize_doi, normalize_title
from .arxiv_self import ArxivSelfReportedResolver
from .crossref import CrossrefResolver
from .openalex import OpenAlexResolver
from .chain import ChainResolver

__all__ = [
    "DOIMatch",
    "DOIResolver",
    "PaperContext",
    "ArxivSelfReportedResolver",
    "CrossrefResolver",
    "OpenAlexResolver",
    "ChainResolver",
    "normalize_doi",
    "normalize_title",
]
