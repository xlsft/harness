// Annotating an attached image: shapes in the image's own pixels, drawn the same on the editor's canvas and into the
// saved file. Nothing here needs a DOM: a canvas context is all it paints on.

export type ShapeKind = 'line' | 'arrow' | 'rect' | 'ellipse' | 'redact'
export type Tool = 'pen' | 'text' | 'step' | ShapeKind

export interface Point { x: number, y: number }

export type Shape
  = | { kind: 'pen', color: string, width: number, points: Point[] }
    | { kind: ShapeKind, color: string, width: number, from: Point, to: Point }
    | { kind: 'step', color: string, at: Point, n: number, radius: number }
    | { kind: 'text', color: string, at: Point, text: string, size: number, font: string }

// All opaque: a redaction leaves nothing of what was under it.
export const COLORS = [
  { name: 'Red', value: '#ef4444' },
  { name: 'Blue', value: '#3b82f6' },
  { name: 'Green', value: '#22c55e' },
  { name: 'Black', value: '#000000' },
  { name: 'White', value: '#ffffff' }
]
// On screen, in CSS pixels: a shape's width in the image is this times the image's scale at the time.
export const STROKES = [2, 4, 8, 14]
const LINE_HEIGHT = 1.25

export type Painter = Pick<CanvasRenderingContext2D,
  'save' | 'restore' | 'beginPath' | 'moveTo' | 'lineTo' | 'closePath' | 'stroke' | 'fill' | 'arc' | 'ellipse' | 'strokeRect' | 'fillRect' | 'fillText' | 'drawImage' | 'clearRect'
  | 'strokeStyle' | 'fillStyle' | 'lineWidth' | 'lineCap' | 'lineJoin' | 'font' | 'textAlign' | 'textBaseline' | 'globalAlpha' | 'globalCompositeOperation'>

/** The integer pixels a redaction covers: every pixel it touches, within the image. */
export function pixelBounds(a: Point, b: Point, width: number, height: number) {
  const x = Math.max(0, Math.floor(Math.min(a.x, b.x)))
  const y = Math.max(0, Math.floor(Math.min(a.y, b.y)))
  const right = Math.min(width, Math.ceil(Math.max(a.x, b.x)))
  const bottom = Math.min(height, Math.ceil(Math.max(a.y, b.y)))
  return { x, y, w: Math.max(0, right - x), h: Math.max(0, bottom - y) }
}

/** The arrow's head: where the shaft ends and the three corners, so the tip is sharp whatever the width. */
export function arrowHead(from: Point, to: Point, width: number) {
  const angle = Math.atan2(to.y - from.y, to.x - from.x)
  const length = Math.min(Math.max(width * 4, 10), Math.hypot(to.x - from.x, to.y - from.y))
  const corner = (turn: number) => ({ x: to.x - length * Math.cos(angle + turn), y: to.y - length * Math.sin(angle + turn) })
  return {
    tip: to,
    left: corner(Math.PI / 7),
    right: corner(-Math.PI / 7),
    shaftEnd: { x: to.x - length * 0.8 * Math.cos(angle), y: to.y - length * 0.8 * Math.sin(angle) }
  }
}

/** The number of the next step circle: they count up, and undoing one gives its number back. */
export function nextStep(shapes: Shape[]): number {
  return shapes.reduce((n, s) => s.kind === 'step' ? Math.max(n, s.n) : n, 0) + 1
}

/** Black or white, whichever reads on the colour (a step's number). */
export function contrastOn(color: string): string {
  const v = Number.parseInt(color.slice(1), 16)
  const luma = 0.299 * (v >> 16 & 255) + 0.587 * (v >> 8 & 255) + 0.114 * (v & 255)
  return luma > 150 ? '#000000' : '#ffffff'
}

/** Whether a finished drag drew anything: a click with a drag tool leaves no shape. */
export function drawsSomething(s: Shape): boolean {
  if (s.kind === 'pen' || s.kind === 'step') return true
  if (s.kind === 'text') return s.text.trim().length > 0
  return Math.abs(s.to.x - s.from.x) >= 2 || Math.abs(s.to.y - s.from.y) >= 2
}

function polyline(ctx: Painter, points: Point[]) {
  ctx.beginPath()
  points.forEach((p, i) => i ? ctx.lineTo(p.x, p.y) : ctx.moveTo(p.x, p.y))
  // A click with the pen is a dot.
  if (points.length === 1) ctx.lineTo(points[0]!.x + 0.01, points[0]!.y)
  ctx.stroke()
}

/** Paints one shape. `bounds` is the image's size, which a redaction is cut to. */
export function drawShape(ctx: Painter, s: Shape, bounds: { width: number, height: number }) {
  ctx.save()
  ctx.globalAlpha = 1
  ctx.globalCompositeOperation = 'source-over'
  ctx.strokeStyle = s.color
  ctx.fillStyle = s.color
  ctx.lineCap = 'round'
  ctx.lineJoin = 'round'
  switch (s.kind) {
    case 'pen':
      ctx.lineWidth = s.width
      polyline(ctx, s.points)
      break
    case 'line':
      ctx.lineWidth = s.width
      polyline(ctx, [s.from, s.to])
      break
    case 'arrow': {
      const head = arrowHead(s.from, s.to, s.width)
      ctx.lineWidth = s.width
      polyline(ctx, [s.from, head.shaftEnd])
      ctx.beginPath()
      ctx.moveTo(head.tip.x, head.tip.y)
      ctx.lineTo(head.left.x, head.left.y)
      ctx.lineTo(head.right.x, head.right.y)
      ctx.closePath()
      ctx.fill()
      break
    }
    case 'rect':
      ctx.lineWidth = s.width
      ctx.strokeRect(Math.min(s.from.x, s.to.x), Math.min(s.from.y, s.to.y), Math.abs(s.to.x - s.from.x), Math.abs(s.to.y - s.from.y))
      break
    case 'ellipse':
      ctx.lineWidth = s.width
      ctx.beginPath()
      ctx.ellipse((s.from.x + s.to.x) / 2, (s.from.y + s.to.y) / 2, Math.abs(s.to.x - s.from.x) / 2, Math.abs(s.to.y - s.from.y) / 2, 0, 0, Math.PI * 2)
      ctx.stroke()
      break
    case 'redact': {
      // Whole pixels at full opacity: an edge left half covered would keep a trace of what was there.
      const r = pixelBounds(s.from, s.to, bounds.width, bounds.height)
      ctx.fillRect(r.x, r.y, r.w, r.h)
      break
    }
    case 'step':
      ctx.beginPath()
      ctx.arc(s.at.x, s.at.y, s.radius, 0, Math.PI * 2)
      ctx.fill()
      ctx.fillStyle = contrastOn(s.color)
      ctx.font = `600 ${s.radius * 1.1}px sans-serif`
      ctx.textAlign = 'center'
      ctx.textBaseline = 'middle'
      ctx.fillText(String(s.n), s.at.x, s.at.y)
      break
    case 'text':
      ctx.font = s.font
      ctx.textAlign = 'left'
      ctx.textBaseline = 'top'
      s.text.split('\n').forEach((line, i) => ctx.fillText(line, s.at.x, s.at.y + i * s.size * LINE_HEIGHT))
      break
  }
  ctx.restore()
}

/** The image at its own size and the shapes over it, in the order they were drawn. */
export function flatten(ctx: Painter, image: CanvasImageSource, size: { width: number, height: number }, shapes: Shape[]) {
  ctx.clearRect(0, 0, size.width, size.height)
  ctx.drawImage(image, 0, 0, size.width, size.height)
  for (const s of shapes) drawShape(ctx, s, size)
}

/** What an edited image is saved as: a photo stays small, the rest (a GIF's first frame too) loses nothing. */
export function savedType(type: string): string {
  return type === 'image/jpeg' || type === 'image/webp' ? type : 'image/png'
}

/** The file's name for what it was actually saved as (a webview may not encode WebP). */
export function savedName(name: string, type: string): string {
  const ext = { 'image/jpeg': /\.jpe?g$/i, 'image/webp': /\.webp$/i, 'image/png': /\.png$/i }[type]
  if (ext?.test(name)) return name
  return `${name.replace(/\.[^./\\]+$/, '') || 'image'}.${type.split('/').pop()}`
}
