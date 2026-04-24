---
id: prim-quantum_walk
title: Quantum Walk
type: entity
category: primitive
tags:
- walk
- search
- graph
- spatial
- simulation
created_at: '2026-04-17'
version: 1
status: published
related:
- paper-arxiv-quant-ph-0205083
- paper-arxiv-0706.0304
neo4j_synced: false
neo4j_id: null
---

## Summary

Quantum walks are the quantum analog of classical random walks. They exhibit 
different statistical properties due to quantum superposition and interference, 
often providing polynomial or exponential speedups for certain problems.


## Definition

Discrete-time quantum walk: Uses coin operator + shift operator
Continuous-time quantum walk: Evolves under Hamiltonian H (adjacency matrix)


## Complexity

- **Gate Count**: O(t · log N) for t steps on N nodes
- **Depth**: O(t)
- **Qubits**: O(log N)

## References

- [[paper-arxiv-quant-ph-0205083]]
- [[paper-arxiv-0706.0304]]
