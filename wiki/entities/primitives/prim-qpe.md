---
id: prim-qpe
title: Quantum Phase Estimation
type: entity
category: primitive
tags:
- phase-estimation
- eigenvalue
- fundamental
- shor
- simulation
created_at: '2026-04-17'
version: 1
status: published
related:
- arxiv-9508027
- NielsenChuang
- prim-qft
- controlled_unitary
neo4j_synced: false
neo4j_id: null
---

## Summary

Quantum Phase Estimation (QPE) is a quantum algorithm that estimates the eigenvalue 
(phase) of an eigenvector of a unitary operator. It is a core subroutine in many 
quantum algorithms including Shor's algorithm, quantum simulation, and quantum counting.


## Definition

Given unitary U and eigenstate |ψ⟩ such that U|ψ⟩ = e^(2πiφ)|ψ⟩,
QPE estimates φ to t bits of precision.


## Complexity

- **Gate Count**: O(t^2) where t is precision bits
- **Depth**: O(t)
- **Qubits**: t + n (t precision qubits + n for eigenvector)

## References

- [[arxiv-9508027]]
- [[NielsenChuang]]

## Prerequisites

- [[prim-qft]]
- [[controlled_unitary]]

