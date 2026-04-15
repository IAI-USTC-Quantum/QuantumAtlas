"""
Paper Parser CLI

Usage:
    python -m atlas.parser <arxiv_id>
    python -m atlas.parser 9508027
    python -m atlas.parser arXiv:9508027

Options:
    --output-dir, -o    Output directory for downloaded files
    --no-pdf            Skip PDF download
    --save-markdown, -m Save parsed content as Markdown
    --save-json, -j     Save parsed content as JSON
"""

import sys
import argparse
from pathlib import Path

from .arxiv_fetcher import ArxivFetcher
from .pdf_parser import PDFParser


def main():
    """Main entry point."""
    parser = argparse.ArgumentParser(
        description="Fetch and parse arXiv papers",
        prog="atlas-parser"
    )
    
    parser.add_argument(
        "arxiv_id",
        help="arXiv paper ID (e.g., 9508027 or arXiv:9508027)"
    )
    
    parser.add_argument(
        "-o", "--output-dir",
        default="./papers",
        help="Output directory for downloaded files (default: ./papers)"
    )
    
    parser.add_argument(
        "--no-pdf",
        action="store_true",
        help="Skip PDF download"
    )
    
    parser.add_argument(
        "-m", "--save-markdown",
        action="store_true",
        help="Save parsed content as Markdown"
    )
    
    parser.add_argument(
        "-j", "--save-json",
        action="store_true",
        help="Save parsed content as JSON"
    )
    
    parser.add_argument(
        "--import-to-neo4j",
        action="store_true",
        help="Import parsed paper to Neo4j knowledge graph"
    )
    
    args = parser.parse_args()
    
    print(f"🔬 QuantumAtlas Paper Parser")
    print(f"=" * 50)
    
    # Step 1: Fetch from arXiv
    print(f"\n📥 Fetching paper: {args.arxiv_id}")
    
    try:
        fetcher = ArxivFetcher(output_dir=args.output_dir)
        pdf_path, metadata = fetcher.fetch(
            args.arxiv_id,
            download_pdf=not args.no_pdf
        )
    except Exception as e:
        print(f"❌ Error fetching paper: {e}")
        sys.exit(1)
    
    print(f"✅ Title: {metadata['title']}")
    print(f"✅ Authors: {', '.join(metadata['authors'])}")
    print(f"✅ Categories: {', '.join(metadata['categories'])}")
    
    if pdf_path:
        print(f"✅ PDF saved to: {pdf_path}")
    
    # Step 2: Parse PDF
    if pdf_path and pdf_path.exists():
        print(f"\n📄 Parsing PDF...")
        
        try:
            pdf_parser = PDFParser()
            paper = pdf_parser.parse(pdf_path, arxiv_metadata=metadata)
            
            print(f"✅ Parsed {len(paper.sections)} sections")
            
            # Save outputs
            output_base = Path(args.output_dir) / metadata['arxiv_id']
            
            if args.save_markdown:
                md_path = pdf_parser.save_markdown(paper, f"{output_base}.md")
                print(f"✅ Markdown saved to: {md_path}")
            
            if args.save_json:
                json_path = pdf_parser.save_json(paper, f"{output_base}.json")
                print(f"✅ JSON saved to: {json_path}")
            
            # Step 3: Import to Neo4j
            if args.import_to_neo4j:
                print(f"\n🔄 Importing to Neo4j...")
                
                try:
                    from ..knowledge.neo4j_client import Neo4jClient
                    from ..knowledge.models import Paper
                    
                    client = Neo4jClient()
                    client.connect()
                    
                    # Create Paper node
                    paper_node = Paper(
                        id=f"paper_{metadata['arxiv_id']}",
                        title=metadata['title'],
                        arxiv_id=metadata['arxiv_id'],
                        authors=metadata['authors'],
                        year=metadata.get('published', '')[:4] if metadata.get('published') else None,
                        abstract=metadata['abstract'],
                        pdf_url=str(pdf_path.absolute()),
                    )
                    
                    client.create_paper(paper_node)
                    print(f"✅ Paper imported to Neo4j with ID: {paper_node.id}")
                    
                    client.close()
                    
                except Exception as e:
                    print(f"⚠️ Error importing to Neo4j: {e}")
                    print("   Make sure Neo4j is running (docker-compose up -d)")
        
        except Exception as e:
            print(f"❌ Error parsing PDF: {e}")
            import traceback
            traceback.print_exc()
    
    print(f"\n✨ Done!")


if __name__ == "__main__":
    main()
