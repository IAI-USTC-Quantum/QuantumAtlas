---
id: prim-block_encoding
title: Block Encoding
type: entity
category: primitive
tags:
- linear-algebra
- matrix
- hhl
- qml
- state-preparation
created_at: '2026-04-17'
version: 1
status: published
related:
- paper-arxiv-1511.02306
- paper-arxiv-1806.01838
neo4j_synced: false
neo4j_id: null
---

## Summary

Block encoding is a technique to embed a non-unitary matrix A into a larger unitary 
matrix U such that A appears as a submatrix (block) of U. This is essential for 
quantum linear algebra algorithms and quantum machine learning.


## Definition

A (s, α, ε)-block encoding of A satisfies:
||A - α(⟨0|^⊗a ⊗ I)U(|0⟩^⊗a ⊗ I)|| ≤ ε
where s = log N, a is ancilla count, α is subnormalization factor.


## Complexity

- **Gate Count**: Depends on matrix structure, typically O(poly(log N, κ)) where κ is condition number
- **Depth**: O(poly(log N))
- **Qubits**: O(log N + a) where a is ancilla qubits

## References

- [[paper-arxiv-1511.02306]]
- [[paper-arxiv-1806.01838]]

