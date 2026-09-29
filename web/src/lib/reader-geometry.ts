// Overlay geometry for normalized block bboxes (plan §5.1: new Middle JSON
// PDF top-level bbox is [x0, y0, x1, y1] in [0,1] page-fraction space,
// origin top-left of the source page).
//
// The overlay must stay glued to the block under zoom AND rotation: we
// derive the frame from the same normalized coordinates the canvas was
// rendered from, mapping page space onto canvas space for each of the four
// pdf.js rotations (0/90/180/270, clockwise). Canvas here means the CSS
// size of the canvas element, which is what the overlay div aligns to.
export type NormalizedBBox = [number, number, number, number]

export type Rect = { left: number; top: number; width: number; height: number }

const ROTATIONS = [0, 90, 180, 270] as const
export type CanvasRotation = (typeof ROTATIONS)[number]

export function isCanvasRotation(value: number): value is CanvasRotation {
  return (ROTATIONS as readonly number[]).includes(value)
}

// Map one normalized page point (origin top-left) to normalized canvas
// coordinates (origin top-left of the rotated canvas).
function mapPoint(x: number, y: number, rotation: CanvasRotation): { u: number; v: number } {
  switch (rotation) {
    case 0:
      return { u: x, v: y }
    case 90: // page rotated clockwise: top-left of page → top-right of canvas
      return { u: 1 - y, v: x }
    case 180:
      return { u: 1 - x, v: 1 - y }
    case 270: // top-left of page → bottom-left of canvas
      return { u: y, v: 1 - x }
  }
}

export function normalizedBBoxToRect(
  bbox: NormalizedBBox,
  rotation: CanvasRotation,
  canvasWidth: number,
  canvasHeight: number,
): Rect {
  if (!(canvasWidth > 0) || !(canvasHeight > 0)) {
    return { left: 0, top: 0, width: 0, height: 0 }
  }
  const [x0, y0, x1, y1] = bbox
  const a = mapPoint(Math.min(x0, x1), Math.min(y0, y1), rotation)
  const b = mapPoint(Math.max(x0, x1), Math.max(y0, y1), rotation)
  const left = Math.min(a.u, b.u) * canvasWidth
  const top = Math.min(a.v, b.v) * canvasHeight
  const width = Math.abs(a.u - b.u) * canvasWidth
  const height = Math.abs(a.v - b.v) * canvasHeight
  return { left, top, width, height }
}
