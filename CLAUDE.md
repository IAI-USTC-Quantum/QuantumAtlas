# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

QuantumAtlas is an AI-driven quantum algorithm implementation framework with a **three-layer knowledge base architecture** (Raw → Wiki → Neo4j). It converts quantum algorithm research papers into executable quantum circuit code and maintains a structured, evolving knowledge base.

Core insight: **Classification and relationship are different things.** Wiki handles "what is it" (definitions, taxonomy), while Neo4j handles "what relates to it" (dependencies, citations).

## Common Commands

### Development Setup
```bash
# Install dependencies
pip install -e ".[dev]"

# Start Neo4j (required for knowledge graph)
docker-compose up -d

# Verify Neo4j connection
python scripts/verify_neo4j.py

# Initialize knowledge graph with primitives
python scripts/init_primitives.py

# Migrate YAML primitives to Wiki pages
python scripts/migrate_to_wiki.py

# Start Web interface
uvicorn atlas.server.main:app --reload --port 8000
```

### Testing
```bash
# Run all tests
pytest

# Run specific module tests
pytest tests/parser/ -v
pytest tests/wiki/ -v
pytest tests/server/ -v
pytest tests/codegen/ -v
pytest tests/designer/ -v
pytest tests/validator/ -v
pytest tests/estimator/ -v

# Run integration tests (requires network/Neo4j)
pytest -m integration

# Run with coverage
pytest --cov=atlas --cov-report=html
```

### Code Quality
```bash
# Format code
black atlas tests
isort atlas tests

# Check style
ruff check atlas tests

# Type checking
mypy atlas
```

### Module CLI Commands
```bash
# Parse arXiv paper and import to Wiki/Neo4j
python -m atlas.parser 9508027 --wiki --sync-neo4j

# Wiki operations
python -m atlas.wiki ingest {arxiv_id}
python -m atlas.wiki query {search_term}
python -m atlas.wiki lint --fix

# Design circuit from algorithm
python -m atlas.designer algorithm_id --output circuit.json --visualize

# Generate executable code from Quantum IR
python -m atlas.codegen circuit.json --backend qiskit --output circuit.py

# Validate circuit correctness
python -m atlas.validator circuit.json --reference ref_circuit.json

# Generate resource estimation report
python -m atlas.estimator circuit.json --format markdown --output report.md

# Run demo pipeline (no LLM API needed)
python examples/demo_pipeline.py --algorithm qft --backend qiskit --save-code
```

## Architecture Overview

### Three-Layer Knowledge Base

| Layer | Directory | Purpose | Mutability |
|-------|-----------|---------|------------|
| **Raw** | `raw/` | Original sources (PDFs, parsed markdown, JSON) | Immutable |
| **Wiki** | `wiki/` | Structured Markdown pages with YAML frontmatter | Editable |
| **Graph** | Neo4j | Entity relationships and graph queries | Auto-synced from Wiki |

**Wiki is the source of truth for entity data.** Neo4j sync is one-way: Wiki → Neo4j.

### Module Structure (10 modules)

```
atlas/
├── parser/           # arXiv paper fetching and PDF parsing
│   ├── arxiv_fetcher.py
│   └── pdf_parser.py
├── wiki/             # Wiki engine (NEW)
│   ├── engine.py     # Core WikiEngine facade
│   ├── page.py       # WikiPage + WikiFrontmatter models
│   ├── ingester.py   # Paper ingestion workflow
│   ├── querier.py    # Query and search
│   ├── linter.py     # Health checks (broken links, orphans, etc.)
│   ├── templates.py  # Page template generation
│   └── sync/
│       └── neo4j_sync.py  # Wiki → Neo4j sync
├── server/           # Web interface (NEW)
│   ├── main.py       # FastAPI app (v0.2.0)
│   ├── config.py     # ServerConfig from env vars
│   ├── routers/
│   │   ├── wiki.py   # /wiki/* routes (browse, edit, create, search)
│   │   ├── graph.py  # /graph/* routes (D3.js visualization)
│   │   └── api.py    # /api/* REST endpoints
│   └── templates/    # Jinja2 HTML templates
│       ├── wiki/     # list, page, edit, new, search
│       └── graph/    # explorer, node
├── knowledge/        # Neo4j client and Pydantic data models
│   ├── neo4j_client.py
│   └── models.py
├── extractor/        # LLM-based algorithm information extraction
│   ├── extractor.py
│   ├── llm_interface.py
│   └── algorithm_ir.py
├── designer/         # Quantum circuit design and optimization
│   ├── designer.py
│   ├── quantum_ir.py
│   ├── quantum_circuit.py
│   ├── primitive_loader.py
│   ├── primitive_composer.py
│   ├── optimizer.py
│   └── parameter_mapper.py
├── codegen/          # Code generation (Qiskit/QPanda)
│   ├── generator.py
│   ├── qiskit_generator.py
│   ├── qpanda_generator.py
│   ├── template_engine.py
│   └── formatter.py
├── validator/        # Circuit validation and equivalence checking
│   ├── validator.py
│   ├── equivalence_checker.py
│   ├── reference_comparison.py
│   └── test_framework.py
├── estimator/        # Resource estimation and reporting
│   ├── estimator.py
│   ├── resource_analyzer.py
│   └── report_generator.py
└── knowledge_graph/  # Primitive YAML definitions and schemas
    ├── primitives/
    └── schemas/
```

### Data Flow

1. **Paper Parser** (`atlas/parser/`) downloads and parses arXiv papers into `raw/`
2. **Wiki Engine** (`atlas/wiki/`) creates structured Wiki pages in `wiki/`
3. **Knowledge Graph** (`atlas/knowledge/`) stores entities in Neo4j, synced from Wiki
4. **Extractor** (`atlas/extractor/`) uses LLMs to extract algorithm metadata from papers
5. **Circuit Designer** (`atlas/designer/`) composes quantum circuits from primitives, outputs Quantum IR
6. **Code Generator** (`atlas/codegen/`) converts Quantum IR to executable QPanda/Qiskit code
7. **Validator** (`atlas/validator/`) verifies circuit correctness via equivalence checking and testing
8. **Estimator** (`atlas/estimator/`) generates resource reports (gate count, depth, qubits)
9. **Web Server** (`atlas/server/`) provides browser-based UI for Wiki browsing and graph visualization

### Key Data Models

All models use Pydantic and support Neo4j serialization:

- `WikiPage` / `WikiFrontmatter` - Wiki page with YAML frontmatter and Markdown content
- `Primitive` - Quantum building blocks (QFT, QPE, etc.)
- `Algorithm` - Algorithm definitions with primitive dependencies
- `Paper` - Research paper metadata
- `Implementation` - Code implementation of an algorithm
- `QuantumIR` - Intermediate representation for quantum circuits
- `QuantumCircuit` - Circuit with gates, qubits, classical bits
- `AlgorithmIR` - LLM-extracted algorithm metadata

### Quantum IR Format

The `QuantumIR` class (in `atlas/designer/quantum_ir.py`) is the central interchange format:
- Serialized as JSON with circuit metadata
- Supports export to QASM, Qiskit code, and QPanda dict
- Contains gate list with targets, controls, and parameters

### Wiki Page System

Wiki pages are stored in `wiki/` with YAML frontmatter:
- **Page types**: concept, entity, source, comparison
- **Entity subtypes**: algorithm, primitive, people
- **Links**: `[[page-id]]` bidirectional wiki-links
- **Frontmatter fields**: id, title, type, category, tags, status, related, neo4j_synced, neo4j_id
- **Lint checks**: W001-W008 (missing fields, orphans, broken links, contradictions)

See `QUANTUM_ATLAS.md` for complete Wiki conventions and frontmatter schema.

### Neo4j Schema

Node types: `Primitive`, `Algorithm`, `Paper`, `Implementation`

Key relationships:
- `(Paper)-[:PUBLISHES]->(Algorithm)`
- `(Algorithm)-[:DEPENDS_ON]->(Primitive)`
- `(Algorithm)-[:IMPLEMENTED_AS]->(Implementation)`
- `(Paper)-[:CITES]->(Paper)`

Wiki-Neo4j sync mapping:
| Wiki Page Type | Neo4j Node | Sync Direction |
|---|---|---|
| `entity/algorithm` | Algorithm | Wiki → Neo4j |
| `entity/primitive` | Primitive | Wiki → Neo4j |
| `entity/people` | Author | Wiki → Neo4j |
| `source/paper` | Paper | Wiki → Neo4j |

### REST API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/pages` | List wiki pages |
| GET | `/api/pages/{id}` | Get page by ID |
| GET | `/api/search?q=` | Search pages |
| GET | `/api/stats` | Wiki statistics |
| POST | `/api/ingest/paper` | Ingest arXiv paper |
| GET | `/api/lint` | Run lint checks |
| GET | `/api/graph/stats` | Neo4j statistics |
| GET | `/api/graph/schema` | Graph schema |
| POST | `/api/graph/query` | Execute Cypher query |

### Code Generation Architecture

The code generator uses a backend pattern:
- `CodeGenerator` class dispatches to backend-specific generators
- `QiskitGenerator` and `QPandaGenerator` implement backend logic
- `TemplateEngine` for code templates, `Formatter` for code formatting

## Environment Variables

Required:
- `NEO4J_PASSWORD` - Neo4j password (default: bolt://localhost:7687, user: neo4j)

Optional:
- `NEO4J_URI` - Neo4j Bolt URI
- `NEO4J_USER` - Neo4j username
- `WIKI_DIR` - Wiki pages directory (default: `./wiki`)
- `RAW_DIR` - Raw sources directory (default: `./raw`)
- `HOST` - Server host (default: `0.0.0.0`)
- `PORT` - Server port (default: `8000`)

## Testing Guidelines

- Unit tests in `tests/<module>/`
- Integration tests marked with `@pytest.mark.integration`
- Mock Neo4j for unit tests, use real Neo4j for integration tests
- Circuit tests use small qubit counts (2-4 qubits) for efficiency
- 2 test files have collection errors (LLM/PDF parser tests requiring external deps)
- `examples/demo_pipeline.py` provides a full pipeline demo without LLM APIs

## Code Style

- Python 3.11+ with type annotations required
- Line length: 100 characters (Black config)
- All public functions must have docstrings
- Use Pydantic models for data validation
- Prefer dataclasses for internal representations
- Commit format: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`