---
id: prim-amplitude_amplification
title: Amplitude Amplification
type: entity
category: primitive
tags:
- search
- grover
- oracle
- quadratic-speedup
- optimization
created_at: '2026-04-17'
version: 1
status: published
related:
- arxiv-quant-ph/0005055
- Grover1996
- oracle_construction
- diffusion_operator
neo4j_synced: false
neo4j_id: null
---

## Summary

Amplitude amplification is a generalization of Grover's algorithm that increases 
the probability of obtaining a desired quantum state. It provides a quadratic 
speedup over classical brute-force search for unstructured databases.


## Definition

Given state |ψ⟩ = sin(θ)|good⟩ + cos(θ)|bad⟩,
amplitude amplification performs ≈ π/(4θ) iterations of the 
Grover operator to amplify |good⟩ amplitude to ≈ 1.


## Complexity

- **Gate Count**: O(√N) iterations for N items
- **Depth**: O(√N · oracle_depth)
- **Qubits**: n (for N = 2^n items)

## References

- [[arxiv-quant-ph/0005055]]
- [[Grover1996]]

## Prerequisites

- [[oracle_construction]]
- [[diffusion_operator]]

