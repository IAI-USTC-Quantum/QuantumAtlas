"""
Page Templates

Provides template functions for creating different types of wiki pages.
Each template generates a WikiPage with appropriate frontmatter and content structure.
"""

from datetime import datetime
from typing import Any, Dict, List, Optional

from .page import WikiPage, WikiFrontmatter


class PageTemplate:
    """
    Factory class for creating wiki page templates.

    Each static method creates a properly structured WikiPage
    for a specific page type.
    """

    @staticmethod
    def concept(
        id: str,
        title: str,
        summary: str,
        definition: str,
        examples: Optional[List[str]] = None,
        related: Optional[List[str]] = None,
        tags: Optional[List[str]] = None,
        see_also: Optional[List[str]] = None,
    ) -> WikiPage:
        """
        Create a concept page template.

        Args:
            id: Unique page identifier (e.g., "concept-qft")
            title: Display title
            summary: Brief explanation of the concept
            definition: Formal or mathematical definition
            examples: List of example applications
            related: Related page IDs
            tags: Classification tags
            see_also: Additional related pages

        Returns:
            WikiPage configured as a concept page
        """
        content = f"""## Summary

{summary}

## Definition

{definition}
"""

        if examples:
            content += "\n## Examples\n\n"
            for ex in examples:
                content += f"- {ex}\n"

        if see_also or related:
            content += "\n## See Also\n\n"
            for rel in (see_also or related or []):
                content += f"- [[{rel}]]\n"

        return WikiPage(
            frontmatter=WikiFrontmatter(
                id=id,
                title=title,
                type="concept",
                tags=tags or [],
                related=related or [],
                status="draft",
            ),
            content=content,
        )

    @staticmethod
    def algorithm_entity(
        id: str,
        name: str,
        problem: str,
        description: str,
        primitives: Optional[List[str]] = None,
        paper_id: Optional[str] = None,
        complexity: Optional[Dict[str, str]] = None,
        pseudocode: Optional[str] = None,
        tags: Optional[List[str]] = None,
        authors: Optional[List[str]] = None,
        year: Optional[int] = None,
    ) -> WikiPage:
        """
        Create an algorithm entity page.

        Args:
            id: Unique page identifier (e.g., "algo-shors")
            name: Algorithm name
            problem: Problem the algorithm solves
            description: Detailed description
            primitives: List of primitive IDs used
            paper_id: Source paper page ID
            complexity: Dict with time, space, gates complexity
            pseudocode: Algorithm pseudocode
            tags: Classification tags
            authors: Algorithm authors
            year: Publication year

        Returns:
            WikiPage configured as an algorithm entity
        """
        complexity = complexity or {}
        primitives = primitives or []

        content = f"""## Overview

**Problem**: {problem}

**Complexity**:
- Time: {complexity.get('time', 'Unknown')}
- Space: {complexity.get('space', 'Unknown')}
- Gates: {complexity.get('gates', 'Unknown')}
- Depth: {complexity.get('depth', 'Unknown')}
- Qubits: {complexity.get('qubits', 'Unknown')}

"""

        if authors:
            content += f"**Authors**: {', '.join(authors)}\n\n"
        if year:
            content += f"**Year**: {year}\n\n"

        content += f"""## Description

{description}

"""

        if primitives:
            content += """## Primitives Used

"""
            for p in primitives:
                content += f"- [[{p}]]\n"
            content += "\n"

        if pseudocode:
            content += f"""## Pseudocode

```
{pseudocode}
```

"""

        if paper_id:
            content += f"""## Source

- [[{paper_id}]]

"""

        content += """## Implementations

*Auto-generated from knowledge graph*

"""

        return WikiPage(
            frontmatter=WikiFrontmatter(
                id=id,
                title=name,
                type="entity",
                category="algorithm",
                tags=tags or ["quantum-algorithm"],
                related=primitives + ([paper_id] if paper_id else []),
                status="draft",
            ),
            content=content,
        )

    @staticmethod
    def primitive_entity(
        id: str,
        name: str,
        summary: str,
        definition: Optional[str] = None,
        complexity: Optional[Dict[str, str]] = None,
        references: Optional[List[str]] = None,
        prerequisites: Optional[List[str]] = None,
        tags: Optional[List[str]] = None,
    ) -> WikiPage:
        """
        Create a primitive entity page.

        Args:
            id: Unique page identifier (e.g., "prim-qft")
            name: Primitive name
            summary: Brief description
            definition: Mathematical definition
            complexity: Dict with gate_count, depth, qubits
            references: Reference paper/page IDs
            prerequisites: Required primitive IDs
            tags: Classification tags

        Returns:
            WikiPage configured as a primitive entity
        """
        complexity = complexity or {}
        references = references or []
        prerequisites = prerequisites or []

        content = f"""## Summary

{summary}

"""

        if definition:
            content += f"""## Definition

{definition}

"""

        content += f"""## Complexity

- **Gate Count**: {complexity.get('gate_count', 'Unknown')}
- **Depth**: {complexity.get('depth', 'Unknown')}
- **Qubits**: {complexity.get('qubits', 'Unknown')}

"""

        if references:
            content += """## References

"""
            for ref in references:
                content += f"- [[{ref}]]\n"
            content += "\n"

        if prerequisites:
            content += """## Prerequisites

"""
            for pre in prerequisites:
                content += f"- [[{pre}]]\n"
            content += "\n"

        return WikiPage(
            frontmatter=WikiFrontmatter(
                id=id,
                title=name,
                type="entity",
                category="primitive",
                tags=tags or ["primitive"],
                related=references + prerequisites,
                status="draft",
            ),
            content=content,
        )

    @staticmethod
    def source_paper(
        arxiv_id: str,
        title: str,
        authors: List[str],
        abstract: str,
        algorithms: Optional[List[str]] = None,
        published: Optional[str] = None,
        doi: Optional[str] = None,
        categories: Optional[List[str]] = None,
    ) -> WikiPage:
        """
        Create a source paper page.

        Args:
            arxiv_id: arXiv paper ID
            title: Paper title
            authors: List of author names
            abstract: Paper abstract
            algorithms: Algorithm page IDs introduced by this paper
            published: Publication date string
            doi: DOI if available
            categories: arXiv categories

        Returns:
            WikiPage configured as a source paper
        """
        algorithms = algorithms or []
        categories = categories or []

        content = f"""## Metadata

- **arXiv ID**: [{arxiv_id}](https://arxiv.org/abs/{arxiv_id})
- **Authors**: {', '.join(authors)}
"""

        if published:
            content += f"- **Published**: {published}\n"
        if doi:
            content += f"- **DOI**: [{doi}](https://doi.org/{doi})\n"
        if categories:
            content += f"- **Categories**: {', '.join(categories)}\n"

        content += f"""

## Abstract

{abstract}

"""

        if algorithms:
            content += """## Algorithms Introduced

"""
            for algo in algorithms:
                content += f"- [[{algo}]]\n"
            content += "\n"

        content += """## Key Insights

*Key insights extracted from the paper will be added here*

## See Also

*Related papers and concepts will be linked here*

"""

        return WikiPage(
            frontmatter=WikiFrontmatter(
                id=f"arxiv-{arxiv_id}",
                title=title,
                type="source",
                category="paper",
                tags=categories or ["arxiv"],
                related=algorithms,
                status="draft",
            ),
            content=content,
        )

    @staticmethod
    def comparison(
        id: str,
        title: str,
        description: str,
        entities: List[str],
        criteria: Optional[List[str]] = None,
        comparison_table: Optional[Dict[str, Dict[str, str]]] = None,
        analysis: Optional[str] = None,
        tags: Optional[List[str]] = None,
    ) -> WikiPage:
        """
        Create a comparison page.

        Args:
            id: Unique page identifier (e.g., "comp-algorithm-speed")
            title: Comparison title
            description: What's being compared
            entities: Entity page IDs being compared
            criteria: List of comparison criteria
            comparison_table: Dict of entity_id -> {criterion: value}
            analysis: Detailed analysis text
            tags: Classification tags

        Returns:
            WikiPage configured as a comparison
        """
        criteria = criteria or []
        comparison_table = comparison_table or {}

        content = f"""## Overview

{description}

"""

        if entities:
            content += """## Entities Compared

"""
            for ent in entities:
                content += f"- [[{ent}]]\n"
            content += "\n"

        if criteria and comparison_table:
            content += """## Comparison Table

| Criterion """
            for ent in entities:
                ent_title = ent.split("-", 1)[-1].replace("-", " ").title()
                content += f"| [[{ent}|{ent_title}]] "
            content += "|\n"

            content += "|----------"
            for _ in entities:
                content += "|----------"
            content += "|\n"

            for criterion in criteria:
                content += f"| **{criterion}** "
                for ent in entities:
                    value = comparison_table.get(ent, {}).get(criterion, "N/A")
                    content += f"| {value} "
                content += "|\n"
            content += "\n"

        if analysis:
            content += f"""## Analysis

{analysis}

"""

        content += """## Recommendations

*Recommendations based on the comparison will be added here*

"""

        return WikiPage(
            frontmatter=WikiFrontmatter(
                id=id,
                title=title,
                type="comparison",
                tags=tags or ["comparison"],
                related=entities,
                status="draft",
            ),
            content=content,
        )

    @staticmethod
    def person_entity(
        id: str,
        name: str,
        affiliation: Optional[str] = None,
        papers: Optional[List[str]] = None,
        algorithms: Optional[List[str]] = None,
        bio: Optional[str] = None,
    ) -> WikiPage:
        """
        Create a person entity page.

        Args:
            id: Unique page identifier (e.g., "person-peter-shor")
            name: Person's name
            affiliation: Current affiliation
            papers: Paper page IDs authored
            algorithms: Algorithm page IDs contributed to
            bio: Brief biography

        Returns:
            WikiPage configured as a person entity
        """
        papers = papers or []
        algorithms = algorithms or []

        content = f"""## Overview

**Name**: {name}
"""
        if affiliation:
            content += f"**Affiliation**: {affiliation}\n"
        content += "\n"

        if bio:
            content += f"""## Biography

{bio}

"""

        if papers:
            content += """## Papers

"""
            for paper in papers:
                content += f"- [[{paper}]]\n"
            content += "\n"

        if algorithms:
            content += """## Algorithms

"""
            for algo in algorithms:
                content += f"- [[{algo}]]\n"
            content += "\n"

        return WikiPage(
            frontmatter=WikiFrontmatter(
                id=id,
                title=name,
                type="entity",
                category="person",
                tags=["researcher"],
                related=papers + algorithms,
                status="draft",
            ),
            content=content,
        )
