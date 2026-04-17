---
id: prim-hamiltonian_simulation
title: Hamiltonian Simulation
type: entity
category: primitive
tags:
- simulation
- chemistry
- physics
- trotterization
- qpe-based
created_at: '2026-04-17'
version: 1
status: published
related:
- arxiv-quant-ph/0508139
- arxiv-1412.4687
- Lloyd1996
neo4j_synced: false
neo4j_id: null
---

## Summary

Hamiltonian simulation is the problem of simulating the time evolution of a 
quantum system described by a Hamiltonian H. It is a fundamental problem in 
quantum computing with applications in quantum chemistry, materials science, 
and quantum field theory.


## Definition

Given Hamiltonian H = Σ_j h_j H_j and time t, 
implement unitary U = e^(-iHt) up to precision ε.


## Complexity

- **Gate Count**: O(t · poly(log N, log(1/ε))) for time t, precision ε
- **Depth**: O(t)
- **Qubits**: O(log N + log(1/ε))

## References

- [[arxiv-quant-ph/0508139]]
- [[arxiv-1412.4687]]
- [[Lloyd1996]]

