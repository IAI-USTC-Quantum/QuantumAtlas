---
id: prim-variational_circuit
title: Variational Quantum Circuit
type: entity
category: primitive
tags:
- vqa
- vqe
- qaoa
- optimization
- machine-learning
- parameterized
created_at: '2026-04-17'
version: 1
status: published
related:
- arxiv-1304.3061
- arxiv-1411.4028
neo4j_synced: false
neo4j_id: null
---

## Summary

Variational quantum circuits (also called parameterized quantum circuits or 
ansätze) are quantum circuits with tunable parameters. They form the basis 
of variational quantum algorithms (VQA) like VQE and QAOA.


## Definition

A variational circuit U(θ) = Π_l U_l(θ_l) consists of:
- Single-qubit rotation layers
- Entangling gates (typically CNOT/CZ)
- Classical optimization loop


## Complexity

- **Gate Count**: Depends on ansatz structure
- **Depth**: O(p) where p is number of layers
- **Qubits**: n (problem-dependent)

## References

- [[arxiv-1304.3061]]
- [[arxiv-1411.4028]]

