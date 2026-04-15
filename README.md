# QuantumAtlas

Systematic large-scale implementation of quantum algorithms via agentic AI. Bridging the gap from paper to executable code and resource estimation.

## Overview

QuantumAtlas is a framework for:
- 📄 **Paper Parsing**: Download and parse quantum computing papers from arXiv
- 🧠 **Knowledge Extraction**: Use LLM to extract algorithm information
- 🕸️ **Knowledge Graph**: Build a graph of quantum primitives and algorithms
- 💻 **Code Generation**: Generate executable quantum code
- ✅ **Validation**: Verify correctness of implementations
- 📊 **Resource Estimation**: Estimate gate counts, depth, and qubit requirements

## Quick Start

### Prerequisites

- Python 3.10+
- Docker (for Neo4j)

### 1. Clone Repository

```bash
git clone https://github.com/Agony5757/QuantumAtlas.git
cd QuantumAtlas
```

### 2. Set Up Environment

```bash
# Create virtual environment
python -m venv .venv
source .venv/bin/activate  # On Windows: .venv\Scripts\activate

# Install dependencies
pip install -e ".[dev]"
```

### 3. Start Neo4j

```bash
# Start Neo4j with Docker
docker-compose up -d

# Wait for Neo4j to be ready (≈30 seconds)
sleep 30

# Verify connection
python scripts/verify_neo4j.py
```

Neo4j Browser will be available at http://localhost:7474
- Username: `neo4j`
- Password: `quantum-atlas`

### 4. Test Paper Parser

```bash
# Download and parse a paper
python -m atlas.parser 9508027 -m -j --import-to-neo4j

# Or use the CLI
atlas-parser 9508027 -m -j
```

## Project Structure

```
QuantumAtlas/
├── atlas/                      # Core package
│   ├── parser/                 # Paper parsing module
│   │   ├── arxiv_fetcher.py   # arXiv download
│   │   ├── pdf_parser.py      # PDF to text/Markdown
│   │   └── __main__.py        # CLI entry point
│   ├── knowledge/              # Knowledge graph module
│   │   ├── neo4j_client.py    # Neo4j operations
│   │   └── models.py          # Data models
│   ├── extractor/              # LLM extraction module
│   │   └── llm_interface.py   # LLM interface (TODO)
│   ├── designer/               # Circuit designer (TODO)
│   ├── codegen/                # Code generator (TODO)
│   ├── validator/              # Circuit validator (TODO)
│   ├── estimator/              # Resource estimator (TODO)
│   └── knowledge_graph/        # Knowledge graph schemas
│       ├── schemas/           # Node & relationship schemas
│       └── primitives/        # Primitive definitions (YAML)
├── tests/                      # Test suite
├── papers/                     # Downloaded papers (gitignored)
├── docker-compose.yml          # Neo4j Docker setup
├── pyproject.toml             # Project configuration
└── README.md                  # This file
```

## Modules

### Parser (`atlas.parser`)

Fetches papers from arXiv and converts PDFs to structured text.

```python
from atlas.parser import ArxivFetcher, PDFParser

# Fetch paper
fetcher = ArxivFetcher()
pdf_path, metadata = fetcher.fetch("9508027")

# Parse PDF
parser = PDFParser()
paper = parser.parse(pdf_path, metadata)
print(paper.to_markdown())
```

CLI usage:
```bash
python -m atlas.parser <arxiv_id> [options]

Options:
  -o, --output-dir     Output directory
  -m, --save-markdown  Save as Markdown
  -j, --save-json      Save as JSON
  --import-to-neo4j    Import to knowledge graph
```

### Knowledge Graph (`atlas.knowledge`)

Manages the Neo4j-based knowledge graph.

```python
from atlas.knowledge import Neo4jClient, Primitive

# Connect to Neo4j
client = Neo4jClient()
client.connect()

# Create primitive
primitive = Primitive(
    id="primitive_qft",
    name="Quantum Fourier Transform",
    category="transformation",
    complexity={"gate_count": "O(n^2)", "depth": "O(n)"}
)
client.create_primitive(primitive)

# Query primitives
primitives = client.get_all_primitives()
```

### Primitives

Predefined quantum primitives in `atlas/knowledge_graph/primitives/`:

- `qft.yaml` - Quantum Fourier Transform
- `qpe.yaml` - Quantum Phase Estimation
- `block_encoding.yaml` - Block Encoding
- `amplitude_amplification.yaml` - Amplitude Amplification
- `hamiltonian_simulation.yaml` - Hamiltonian Simulation
- `variational_circuit.yaml` - Variational Quantum Circuit
- `quantum_walk.yaml` - Quantum Walk

## Development

### Running Tests

```bash
pytest
```

### Code Formatting

```bash
black atlas tests
ruff check atlas tests
```

### Type Checking

```bash
mypy atlas
```

## Roadmap

### Phase 1 (Current)
- [x] Project skeleton
- [x] Paper parser (arXiv + PDF)
- [x] Knowledge graph schema
- [x] Neo4j integration
- [x] Primitive definitions
- [ ] LLM extractor implementation

### Phase 2
- [ ] Algorithm extraction pipeline
- [ ] Circuit designer
- [ ] Code generation (QPanda, Qiskit)

### Phase 3
- [ ] Circuit validation
- [ ] Resource estimation
- [ ] Web interface

## License

Apache License 2.0 - See [LICENSE](LICENSE)

## Contributing

TODO: Add contribution guidelines

## Acknowledgments

This project is built on:
- [Neo4j](https://neo4j.com/) - Graph database
- [PyMuPDF](https://pymupdf.readthedocs.io/) - PDF parsing
- [Pydantic](https://docs.pydantic.dev/) - Data validation
