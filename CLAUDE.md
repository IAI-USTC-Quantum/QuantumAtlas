# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

QuantumAtlas is an AI-driven quantum algorithm implementation framework that converts quantum algorithm research papers into executable quantum circuit code. It provides an end-to-end pipeline from paper parsing to code generation.

## Common Commands

### Development Setup
```bash
# Install dependencies (Poetry-based)
pip install -e ".[dev]"

# Start Neo4j (required for knowledge graph)
docker-compose up -d

# Verify Neo4j connection
python scripts/verify_neo4j.py

# Initialize knowledge graph with primitives
python scripts/init_primitives.py
```

### Testing
```bash
# Run all tests
pytest

# Run specific module tests
pytest tests/parser/ -v
pytest tests/codegen/ -v

# Run integration tests (requires network/Neo4j)
pytest -m integration

# Run with coverage
pytest --cov=atlas --cov-report=html
```

### Code Quality
```bash
# Format code
black atlas tests

# Check style
ruff check atlas tests

# Type checking
mypy atlas

# Sort imports
isort atlas tests
```

### Module CLI Commands
```bash
# Parse arXiv paper and import to Neo4j
python -m atlas.parser 9508027 -m -j --import-to-neo4j

# Design circuit from algorithm
python -m atlas.designer algorithm_id --output circuit.json --visualize

# Generate executable code from Quantum IR
python -m atlas.codegen circuit.json --backend qiskit --output circuit.py

# Validate circuit correctness
python -m atlas.validator circuit.json --reference ref_circuit.json

# Generate resource estimation report
python -m atlas.estimator circuit.json --format markdown --output report.md
```

## Architecture Overview

### Module Structure

The codebase follows a pipeline architecture with 7 core modules:

```
atlas/
├── parser/         # arXiv paper fetching and PDF parsing
├── knowledge/      # Neo4j client and Pydantic data models
├── extractor/      # LLM-based algorithm information extraction
├── designer/       # Quantum circuit design and optimization
├── codegen/        # Code generation (QPanda/Qiskit)
├── validator/      # Circuit validation and equivalence checking
├── estimator/      # Resource estimation and reporting
└── knowledge_graph/  # Primitive definitions (YAML) and schemas
```

### Data Flow

1. **Paper Parser** (`atlas/parser/`) downloads and parses arXiv papers into structured data
2. **Knowledge Graph** (`atlas/knowledge/`) stores Primitives, Algorithms, Papers, and Implementations in Neo4j
3. **Extractor** (`atlas/extractor/`) uses LLMs to extract algorithm metadata from papers
4. **Circuit Designer** (`atlas/designer/`) composes quantum circuits from primitives, outputs Quantum IR
5. **Code Generator** (`atlas/codegen/`) converts Quantum IR to executable QPanda/Qiskit code
6. **Validator** (`atlas/validator/`) verifies circuit correctness via equivalence checking and testing
7. **Estimator** (`atlas/estimator/`) generates resource reports (gate count, depth, qubits)

### Key Data Models

All models use Pydantic and support Neo4j serialization:

- `Primitive` - Quantum building blocks (QFT, QPE, etc.)
- `Algorithm` - Algorithm definitions with primitive dependencies
- `Paper` - Research paper metadata
- `Implementation` - Code implementation of an algorithm
- `QuantumIR` - Intermediate representation for quantum circuits
- `QuantumCircuit` - Circuit with gates, qubits, classical bits

### Quantum IR Format

The `QuantumIR` class (in `atlas/designer/quantum_ir.py`) is the central interchange format:
- Serialized as JSON with circuit metadata
- Supports export to QASM, Qiskit code, and QPanda dict
- Contains gate list with targets, controls, and parameters

### Neo4j Schema

Node types: `Primitive`, `Algorithm`, `Paper`, `Implementation`

Key relationships:
- `(Paper)-[:PUBLISHES]->(Algorithm)`
- `(Algorithm)-[:DEPENDS_ON]->(Primitive)`
- `(Algorithm)-[:IMPLEMENTED_AS]->(Implementation)`
- `(Paper)-[:CITES]->(Paper)`

### Primitive System

Primitives are defined in YAML files (`atlas/knowledge_graph/primitives/`):
- Each primitive has id, name, category, complexity, input/output qubits
- Categories: transform, oracle, state_preparation, simulation, variational
- Loaded into Neo4j via `scripts/init_primitives.py`

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

## Testing Guidelines

- Unit tests in `tests/<module>/`
- Integration tests marked with `@pytest.mark.integration`
- Mock Neo4j for unit tests, use real Neo4j for integration tests
- Circuit tests use small qubit counts (2-4 qubits) for efficiency

## Code Style

- Python 3.11+ with type annotations required
- Line length: 100 characters (Black config)
- All public functions must have docstrings
- Use Pydantic models for data validation
- Prefer dataclasses for internal representations
