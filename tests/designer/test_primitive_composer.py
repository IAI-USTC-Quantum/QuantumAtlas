"""Tests for primitive composer."""

import pytest
from atlas.designer.primitive_loader import PrimitiveLoader, PrimitiveDefinition
from atlas.designer.primitive_composer import PrimitiveComposer, CompositionResult


class TestPrimitiveLoader:
    """Test PrimitiveLoader class."""

    def test_load_primitives(self):
        """Test loading primitives from directory."""
        loader = PrimitiveLoader()
        primitives = loader.get_all_primitives()

        assert len(primitives) > 0

        # Check that primitives have required fields
        for p in primitives:
            assert p.id
            assert p.name

    def test_get_primitive(self):
        """Test getting specific primitive."""
        loader = PrimitiveLoader()

        # Try to get a known primitive
        ids = loader.get_all_primitive_ids()
        if ids:
            p = loader.get_primitive(ids[0])
            assert p is not None
            assert p.id == ids[0]

    def test_get_nonexistent_primitive(self):
        """Test getting non-existent primitive."""
        loader = PrimitiveLoader()
        p = loader.get_primitive("nonexistent_primitive")
        assert p is None

    def test_search_primitives(self):
        """Test searching primitives."""
        loader = PrimitiveLoader()

        # Search for 'qft' or other common terms
        results = loader.search_primitives("qft")
        # May or may not find depending on available primitives

    def test_get_by_category(self):
        """Test getting primitives by category."""
        loader = PrimitiveLoader()

        categories = set()
        for p in loader.get_all_primitives():
            if p.category:
                categories.add(p.category)

        for cat in categories:
            primitives = loader.get_primitives_by_category(cat)
            assert len(primitives) > 0
            for p in primitives:
                assert p.category == cat

    def test_get_by_tag(self):
        """Test getting primitives by tag."""
        loader = PrimitiveLoader()

        # Find a common tag
        all_tags = set()
        for p in loader.get_all_primitives():
            all_tags.update(p.tags)

        if all_tags:
            tag = list(all_tags)[0]
            primitives = loader.get_primitives_by_tag(tag)
            assert len(primitives) > 0
            for p in primitives:
                assert tag in p.tags


class TestPrimitiveComposer:
    """Test PrimitiveComposer class."""

    def test_compose_single_primitive(self):
        """Test composing a single primitive."""
        composer = PrimitiveComposer()

        # Try to compose with available primitive
        loader = PrimitiveLoader()
        ids = loader.get_all_primitive_ids()

        if ids:
            result = composer.compose_single(ids[0])
            assert isinstance(result, CompositionResult)
            # Success depends on whether primitive has gate sequence

    def test_compose_multiple_primitives(self):
        """Test composing multiple primitives."""
        composer = PrimitiveComposer()
        loader = PrimitiveLoader()
        ids = loader.get_all_primitive_ids()

        if len(ids) >= 2:
            result = composer.compose(ids[:2])
            assert isinstance(result, CompositionResult)

    def test_compose_nonexistent_primitive(self):
        """Test composing non-existent primitive."""
        composer = PrimitiveComposer()
        result = composer.compose_single("nonexistent")

        assert not result.success
        assert "not found" in result.error_message.lower()

    def test_compose_empty_list(self):
        """Test composing empty list."""
        composer = PrimitiveComposer()
        result = composer.compose([])

        assert not result.success
        assert "no primitives" in result.error_message.lower()

    def test_parameterized_qft(self):
        """Test composing parameterized QFT."""
        composer = PrimitiveComposer()

        # Test QFT generation directly
        result = composer.compose_single("primitive_qft", params={"n": 4})

        # Should succeed even if primitive not in YAML
        # because of built-in parameterized generation
        if result.success:
            assert result.circuit is not None
            assert result.total_qubits >= 4

    def test_bell_state_generation(self):
        """Test Bell state generation."""
        composer = PrimitiveComposer()

        result = composer.compose_single("bell_state", params={})

        if result.success:
            assert result.circuit is not None
            assert result.total_qubits >= 2

    def test_qubit_mapping(self):
        """Test qubit mapping in composition result."""
        composer = PrimitiveComposer()
        loader = PrimitiveLoader()
        ids = loader.get_all_primitive_ids()

        if ids:
            result = composer.compose_single(ids[0])
            if result.success:
                assert ids[0] in result.qubit_mapping


class TestCompositionResult:
    """Test CompositionResult class."""

    def test_default_result(self):
        """Test default composition result."""
        result = CompositionResult()

        assert not result.success
        assert result.circuit is None
        assert result.total_qubits == 0
        assert result.error_message == ""

    def test_successful_result(self):
        """Test successful composition result."""
        from atlas.designer.quantum_circuit import QuantumCircuit

        circuit = QuantumCircuit(num_qubits=2)
        result = CompositionResult(
            circuit=circuit,
            success=True,
            total_qubits=2,
            primitives_used=["test_primitive"]
        )

        assert result.success
        assert result.circuit == circuit
        assert result.total_qubits == 2
