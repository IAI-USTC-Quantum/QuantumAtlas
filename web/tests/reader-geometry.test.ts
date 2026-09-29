import { describe, expect, it } from 'vitest'
import { isCanvasRotation, normalizedBBoxToRect } from '@/lib/reader-geometry'

// Expected values derive from the mapping table in reader-geometry.ts: page
// space (origin top-left, normalized) → canvas space for each clockwise
// pdf.js rotation. Overlay frames must stay glued to the same normalized
// bbox under ANY zoom/rotation (plan §8 Q4 acceptance).
describe('normalizedBBoxToRect', () => {
  const bbox = [0.1, 0.1, 0.5, 0.4] as [number, number, number, number]
  // Unrotated canvas for a 612×792pt page.
  const W = 612
  const H = 792
  // 90°/270° rotations swap the canvas extents.
  const Wt = 792
  const Ht = 612

  it('maps identity at rotation 0', () => {
    const rect = normalizedBBoxToRect(bbox, 0, W, H)
    expect(rect.left).toBeCloseTo(0.1 * W)
    expect(rect.top).toBeCloseTo(0.1 * H)
    expect(rect.width).toBeCloseTo(0.4 * W)
    expect(rect.height).toBeCloseTo(0.3 * H)
  })

  it('rotates clockwise 90°: page top-left corner becomes canvas top-right', () => {
    const rect = normalizedBBoxToRect(bbox, 90, Wt, Ht)
    expect(rect.left).toBeCloseTo(0.6 * Wt)
    expect(rect.top).toBeCloseTo(0.1 * Ht)
    expect(rect.width).toBeCloseTo(0.3 * Wt)
    expect(rect.height).toBeCloseTo(0.4 * Ht)
  })

  it('rotates 180°', () => {
    const rect = normalizedBBoxToRect(bbox, 180, W, H)
    expect(rect.left).toBeCloseTo(0.5 * W)
    expect(rect.top).toBeCloseTo(0.6 * H)
    expect(rect.width).toBeCloseTo(0.4 * W)
    expect(rect.height).toBeCloseTo(0.3 * H)
  })

  it('rotates 270°: page top-left corner becomes canvas bottom-left', () => {
    const rect = normalizedBBoxToRect(bbox, 270, Wt, Ht)
    expect(rect.left).toBeCloseTo(0.1 * Wt)
    expect(rect.top).toBeCloseTo(0.5 * Ht)
    expect(rect.width).toBeCloseTo(0.3 * Wt)
    expect(rect.height).toBeCloseTo(0.4 * Ht)
  })

  it('preserves area across every rotation', () => {
    for (const rotation of [0, 90, 180, 270] as const) {
      const rect = normalizedBBoxToRect(bbox, rotation, rotation % 180 === 0 ? W : Wt, rotation % 180 === 0 ? H : Ht)
      const area = (rect.width * rect.height) / (W * H)
      expect(area).toBeCloseTo(0.4 * 0.3)
    }
  })

  it('tolerates inverted bbox corners without a negative-size frame', () => {
    const inverted = [0.5, 0.4, 0.1, 0.1] as [number, number, number, number]
    const rect = normalizedBBoxToRect(inverted, 0, W, H)
    expect(rect).toEqual(normalizedBBoxToRect(bbox, 0, W, H))
  })

  it('returns a zero rect for a zero-sized canvas instead of throwing', () => {
    expect(normalizedBBoxToRect(bbox, 0, 0, 0)).toEqual({ left: 0, top: 0, width: 0, height: 0 })
  })

  it('validates rotation values', () => {
    expect(isCanvasRotation(0)).toBe(true)
    expect(isCanvasRotation(270)).toBe(true)
    expect(isCanvasRotation(90)).toBe(true)
    expect(isCanvasRotation(45)).toBe(false)
  })
})
