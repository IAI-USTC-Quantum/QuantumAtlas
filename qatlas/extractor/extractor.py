"""
Algorithm Extractor

Main class for extracting algorithm information from papers using LLM.
"""

import os
from typing import Any, Dict, List, Optional

from .algorithm_ir import AlgorithmIR
from .llm_interface import LLMInterface, TokenUsage, create_llm


class AlgorithmExtractor:
    """
    Extracts algorithm information from papers and manages the extraction pipeline.

    This class orchestrates the extraction process from paper text to
    Algorithm IR. Persisting results to the knowledge graph is a server-side
    concern (the Go ``qatlasd`` owns the Neo4j connection); the client
    produces an :class:`AlgorithmIR` and exports it (e.g. to YAML).

    Usage:
        llm = create_llm("openai")
        extractor = AlgorithmExtractor(llm)

        # Extract from paper text
        algorithm_ir = extractor.extract_from_paper("arxiv:9508027", paper_text)

        # Export the structured result
        extractor.export_to_yaml(algorithm_ir, "algorithm.yaml")
    """
    
    def __init__(self, llm: LLMInterface):
        """
        Initialize the extractor with an LLM interface.
        
        Args:
            llm: LLM interface instance (OpenAIProvider, ClaudeProvider, etc.)
        """
        self.llm = llm
        self.extraction_history: List[Dict[str, Any]] = []
    
    def extract_from_paper(
        self,
        arxiv_id: str,
        paper_text: str,
        available_primitives: Optional[List[str]] = None
    ) -> AlgorithmIR:
        """
        Extract complete algorithm information from paper text.
        
        Args:
            arxiv_id: arXiv paper ID
            paper_text: Full text of the paper (Markdown format)
            available_primitives: Optional list of available primitive IDs
            
        Returns:
            AlgorithmIR: Structured algorithm representation
            
        Raises:
            ExtractionError: If extraction fails
        """
        # Get available primitives from knowledge graph if not provided
        if available_primitives is None:
            available_primitives = self._get_default_primitives()
        
        # Step 1: Extract metadata
        print("Extracting metadata...")
        metadata_result = self.llm.extract_metadata(paper_text)
        if not metadata_result.success:
            raise ExtractionError(f"Metadata extraction failed: {metadata_result.error}")
        
        # Step 2: Extract pseudocode
        print("Extracting pseudocode...")
        pseudocode_result = self.llm.extract_pseudocode(paper_text)
        if not pseudocode_result.success:
            raise ExtractionError(f"Pseudocode extraction failed: {pseudocode_result.error}")
        
        # Step 3: Extract complexity
        print("Extracting complexity...")
        complexity_result = self.llm.extract_complexity(paper_text)
        if not complexity_result.success:
            raise ExtractionError(f"Complexity extraction failed: {complexity_result.error}")
        
        # Step 4: Identify primitives
        print("Identifying primitives...")
        primitives_result = self.llm.identify_primitives(paper_text, available_primitives)
        if not primitives_result.success:
            raise ExtractionError(f"Primitive identification failed: {primitives_result.error}")
        
        # Assemble Algorithm IR
        algorithm_ir = AlgorithmIR.from_extraction_results(
            arxiv_id=arxiv_id,
            metadata=metadata_result.data,
            pseudocode=pseudocode_result.data,
            complexity=complexity_result.data,
            primitives=primitives_result.data,
        )
        
        # Record extraction
        self.extraction_history.append({
            "arxiv_id": arxiv_id,
            "algorithm_id": algorithm_ir.id,
            "success": True,
            "token_usage": self.llm.get_total_usage().to_dict(),
        })
        
        return algorithm_ir

    def get_extraction_stats(self) -> Dict[str, Any]:
        """Get statistics about extractions performed."""
        total_usage = self.llm.get_total_usage()
        return {
            "total_extractions": len(self.extraction_history),
            "successful_extractions": sum(1 for h in self.extraction_history if h["success"]),
            "total_tokens": total_usage.total_tokens,
            "estimated_cost_usd": getattr(total_usage, 'estimated_cost', 0.0),
        }

    def get_total_token_usage(self) -> "TokenUsage":
        """Get total token usage from LLM."""
        return self.llm.get_total_usage()

    def export_to_yaml(self, algorithm_ir: AlgorithmIR, filepath: str) -> None:
        """
        Export AlgorithmIR to YAML file.

        Args:
            algorithm_ir: Algorithm IR to export
            filepath: Path to save YAML file
        """
        yaml_content = algorithm_ir.to_yaml()
        with open(filepath, 'w', encoding='utf-8') as f:
            f.write(yaml_content)
    
    def _get_default_primitives(self) -> List[str]:
        """Get default list of primitive IDs."""
        return [
            "primitive_qft",
            "primitive_qpe",
            "primitive_block_encoding",
            "primitive_amplitude_amplification",
            "primitive_hamiltonian_simulation",
            "primitive_variational_circuit",
            "primitive_quantum_walk",
        ]


class ExtractionError(Exception):
    """Exception raised when algorithm extraction fails."""
    pass


def create_extractor(provider: str = "openai", api_key: Optional[str] = None) -> AlgorithmExtractor:
    """
    Factory function to create an AlgorithmExtractor with specified LLM provider.
    
    Args:
        provider: LLM provider name ("openai" or "anthropic")
        api_key: Optional API key (will use environment variable if not provided)
        
    Returns:
        AlgorithmExtractor instance
        
    Raises:
        ValueError: If API key is not provided and not in environment
    """
    llm = create_llm(provider, api_key=api_key)
    return AlgorithmExtractor(llm)
